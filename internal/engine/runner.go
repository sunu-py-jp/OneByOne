package engine

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func (s *Service) run(ctx context.Context, limit int) error {
	s.mu.Lock()
	cfg := s.state.Config
	oldHash := s.meta.RuleHash
	s.mu.Unlock()
	if cfg.AuthMode == "oauth" {
		var err error
		cfg, err = s.oauthRunConfig(ctx, cfg)
		if err != nil {
			return err
		}
	}
	unlock, e := lockSharedQueue(cfg)
	if e != nil {
		return e
	}
	defer unlock()
	cat, e := catalog.Load(ctx, cfg)
	if e != nil {
		return e
	}
	if oldHash != "" && cat.Hash != oldHash {
		return fmt.Errorf("ルール定義が変更されています。対象抽出を再実行して候補を更新してください")
	}
	s.mu.Lock()
	s.cat = cat
	s.state.Rules = append([]model.Rule(nil), cat.Rules...)
	s.meta.RuleHash = cat.Hash
	s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "catalog", nil, "", "")
	s.recountLocked()
	s.mu.Unlock()
	if e = s.prepareWorktree(ctx, cfg); e != nil {
		return e
	}
	if e = s.recover(ctx); e != nil {
		return e
	}
	return s.runFiles(ctx, cfg, cat, limit)
}

func (s *Service) processOne(parent context.Context, index int, cfg model.Config, cat *catalog.Catalog, budgets map[string]agent.ExecutionBudgetBaseline, writer *executionWriter) error {
	s.mu.Lock()
	t := copyTask(s.state.Tasks[index])
	source := s.sourceDirLocked()
	worktree := s.state.Worktree
	rel := filepath.ToSlash(filepath.Join(s.meta.SourceRelative, t.File))
	sourceRelative := s.meta.SourceRelative
	s.mu.Unlock()
	s.filePhase(t.File, "running")
	defer s.filePhase(t.File, "")
	var scopeErr error
	cat, scopeErr = cat.ForRules(t.Rules)
	if scopeErr != nil {
		return fmt.Errorf("対象ファイルのルールを確認できません: %w", scopeErr)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	head := writer.referenceHead
	var e error
	allowResume := t.ResumeRequested
	var checkpoint *repairCheckpoint
	if len(t.History) > 0 {
		checkpoint, e = loadLatestRepairCheckpoint(cfg, repairHistory(t))
		if e != nil {
			return e
		}
	}
	resumeAttempt := allowResume && unfinishedRepair(t) && repairProvenanceMatches(checkpoint, cfg, cat, t.File, head, t.InputHash)
	h := model.Attempt{ID: uid(), Number: t.Attempts + 1, StartedAt: now(), BaseCommit: head, Checks: []model.Check{}, RulesApplied: []string{}}
	if resumeAttempt {
		h = t.History[len(t.History)-1]
		h.BaseCommit, h.FinishedAt, h.Outcome, h.Commit = head, "", "", ""
		h.Partial = false
		h.OutputHash, h.CommitBase = "", ""
		h.Changes = nil
	} else {
		t.Attempts++
		t.History = append(t.History, h)
	}
	s.mu.Lock()
	if s.activeExecution != nil {
		h.ExecutionID = s.activeExecution.Run.ID
	}
	s.mu.Unlock()
	t.ResumeRequested = false
	t.Status = "running"
	t.UpdatedAt = now()
	t.History[len(t.History)-1] = h
	saveTask := func() error {
		return writer.apply(func() error { s.updateTask(index, t); return s.persist() })
	}
	if e = saveTask(); e != nil {
		return e
	}
	s.log("info", fmt.Sprintf("%s · 試行 %d", t.File, t.Attempts))
	finishNow := func(status, note string) error {
		h.Outcome = status
		h.Note = note
		h.FinishedAt = now()
		h.Changes = buildRecordedChangeReport(cfg, h, checkpoint)
		t.Status = status
		t.Note = note
		t.UpdatedAt = now()
		if h.AdoptedChanges() {
			seen := map[string]bool{}
			for _, id := range t.RulesApplied {
				seen[id] = true
			}
			for _, id := range h.RulesApplied {
				if !seen[id] {
					t.RulesApplied = append(t.RulesApplied, id)
					seen[id] = true
				}
			}
		}
		t.History[len(t.History)-1] = h
		// A retry can confirm earlier adopted changes without producing a new
		// edit. Keep the attempt honest (skipped) while the file stays complete.
		if status == "skipped" && canDiscardChanges(t) {
			t.Status = "done"
		}
		s.updateTask(index, t)
		if err := s.persist(); err != nil {
			return err
		}
		s.log(map[bool]string{true: "info", false: "warn"}[status == "done" || status == "skipped"], t.File+" · "+t.Status+" · "+note)
		return nil
	}
	finish := func(status, note string) error { return writer.apply(func() error { return finishNow(status, note) }) }
	path, e := catalog.PathWithin(source, t.File)
	if e != nil {
		return finish("needs_human", e.Error())
	}
	if forbiddenContext(t.File) {
		return finish("needs_human", "設定・認証ファイルは自動編集の対象外です")
	}
	before, e := readSnapshotFile(ctx, worktree, head, rel)
	if e != nil {
		return finish("needs_human", e.Error())
	}
	h.InputHash = digest(before)
	if t.InputHash != "" && t.InputHash != h.InputHash {
		return finish("needs_human", "抽出時からファイルが変更されています。内容を確認して対象抽出をやり直してください")
	}
	if t.InputHash == "" {
		t.InputHash = h.InputHash
	}
	text, e := decodeSource(before)
	if e != nil {
		return finish("needs_human", e.Error())
	}
	artifact := filepath.Join(cfg.QueuePath+".artifacts", h.ID)
	if e = writeSharedArtifact(cfg, artifact+".before", before, filepath.Join(cfg.Root, filepath.FromSlash(t.File))); e != nil {
		return e
	}
	t.History[len(t.History)-1] = h
	if e = saveTask(); e != nil {
		return e
	}
	prior := sumUsage(t.History[:len(t.History)-1])
	if checkpoint == nil {
		checkpoint = &repairCheckpoint{Version: 1, State: model.RepairState{Version: 1, Usage: prior}}
	} else if !resumeAttempt {
		// Retain historical accounting but never reuse a completed plan.
		checkpoint.State.Plan = model.RepairPlan{}
		checkpoint.State.StagedEdits = nil
		checkpoint.State.LastCandidate = nil
		checkpoint.State.ReadRuleIDs = nil
		checkpoint.State.RuleReadOffsets = nil
		checkpoint.State.Reviews = nil
	}
	checkpoint.AttemptID, checkpoint.PriorUsage = h.ID, prior
	retainKnownUsage(&checkpoint.State, sumUsage(t.History))
	if _, started := budgets[t.File]; !started {
		budgets[t.File] = agent.BudgetBaselineFor(checkpoint.State)
	}
	if resetRepairPlan(checkpoint, cfg, cat, t.File, head, h.InputHash) && resumeAttempt {
		s.log("info", t.File+" · 元コード・ルール・設定の変更により計画を作り直します（使用量は保持）")
	}
	if checkpoint.State.RequestPending {
		checkpoint.State.Usage.Uncertain = true
	}
	writtenReviews := map[string]string{}
	persistRepair := func(state model.RepairState) error {
		return writer.apply(func() error {
			checkpoint.State = state
			var err error
			previousPath := h.RepairPath
			h.RepairPath, err = saveRepairCheckpoint(cfg, checkpoint)
			if err != nil {
				return err
			}
			for _, review := range state.Reviews {
				if review.FinishedAt == "" {
					continue
				}
				if _, err := hex.DecodeString(review.ID); err != nil || len(review.ID) != 32 {
					return fmt.Errorf("独立レビューの記録IDが不正です")
				}
				data, err := json.MarshalIndent(review, "", "  ")
				if err != nil {
					return err
				}
				fingerprint := digest(data)
				if writtenReviews[review.ID] == fingerprint {
					continue
				}
				if err := writeSharedArtifact(cfg, artifact+".review-"+review.ID+".json", data, filepath.Join(cfg.Root, filepath.FromSlash(t.File))); err != nil {
					return err
				}
				writtenReviews[review.ID] = fingerprint
			}
			h.Usage = usageSince(state.Usage, checkpoint.PriorUsage)
			h.Reviews = copyReviews(state.Reviews)
			h.Changes = buildRecordedChangeReport(cfg, h, checkpoint)
			t.History[len(t.History)-1] = h
			s.updateTask(index, t)
			// The review also runs between HTTP requests while it checks or records
			// findings. Only the current candidate can keep this phase active;
			// historical reviews must not affect a replacement candidate.
			reviewing := state.LastCandidate != nil && state.LastCandidate.Review != nil && state.LastCandidate.Review.Verdict == "running"
			if reviewing || (state.RequestPending && state.RequestKind == "review") {
				s.filePhase(t.File, "reviewing")
			} else {
				s.filePhase(t.File, "running")
			}
			// The sidecar is the durable per-turn journal. Only attach its path once;
			// rewriting a 10,000-file JSONL queue for every tool/HTTP turn is unnecessary.
			// Candidate publication, final adoption and completion still save the queue.
			if previousPath != h.RepairPath {
				return s.persist()
			}
			return nil
		})
	}
	if e = persistRepair(checkpoint.State); e != nil {
		return e
	}
	in := agent.Input{Config: cfg, SystemPrompt: cat.SystemPrompt, File: t.File, Content: text.text, BaseHash: h.InputHash, Rules: cat.Rules, CandidateRules: t.Rules, PreviousFailure: failureContext(cfg, t), RepairState: &checkpoint.State, BudgetBaseline: budgets[t.File], SaveRepairState: persistRepair, AllowUncertainResume: allowResume, ReadRule: cat.ReadRule, ReadContext: func(p string, start, end int) (string, error) {
		return readSnapshotContext(ctx, worktree, head, sourceRelative, p, start, end)
	}, Log: func(msg string) { s.log("info", t.File+" · "+msg) }}
	in.ValidateCandidate = func(validationCtx context.Context, request model.CandidateRequest) (model.CandidateValidation, error) {
		s.filePhase(t.File, "checking")
		if err := agent.CheckCandidateHolds(checkpoint.State.Plan, request, text.text); err != nil {
			return model.CandidateValidation{}, err
		}
		result, err := validateCandidate(validationCtx, candidateValidationInput{Config: cfg, Catalog: cat, Source: source, Worktree: worktree, Relative: rel, Head: head, Artifact: artifact, File: t.File, Before: before, InputHash: h.InputHash, Plan: checkpoint.State.Plan, Journal: func(result model.CandidateValidation) error {
			h.OutputHash = result.CandidateHash
			t.History[len(t.History)-1] = h
			return saveTask()
		}}, request)
		if err == nil {
			h.Checks = result.Checks
			if _, statErr := os.Stat(artifact + ".diff"); statErr == nil {
				h.DiffPath = artifact + ".diff"
			}
			t.History[len(t.History)-1] = h
			err = saveTask()
		}
		s.filePhase(t.File, "running")
		return result, err
	}
	proposal, e := s.propose(ctx, in)
	// Production usage is durably recorded per request; an injected proposer may
	// instead report it only once when it returns.
	if checkpoint.State.Usage.Turns == prior.Turns && proposal.Usage.Turns > 0 {
		checkpoint.State.Usage = sumUsage(append(append([]model.Attempt{}, t.History[:len(t.History)-1]...), model.Attempt{Usage: proposal.Usage}))
		if err := persistRepair(checkpoint.State); err != nil {
			return err
		}
	}
	h.Usage = usageSince(checkpoint.State.Usage, prior)
	if agent.IsUsageUnknown(e) {
		h.Usage.Uncertain = true
	}
	h.RulesApplied = proposal.RulesApplied
	if e != nil {
		status := "failed"
		if parent.Err() != nil || agent.IsUsageUnknown(e) || checkpoint.State.ToolCalls > 0 || checkpoint.State.Usage.Turns > prior.Turns {
			status = "needs_human"
		}
		if agent.IsFatal(e) {
			status = "needs_human"
		}
		if err := finish(status, e.Error()); err != nil {
			return err
		}
		if agent.IsFatal(e) {
			return e
		}
		return nil
	}
	s.filePhase(t.File, "applying")
	return writer.apply(func() error {
		finish := finishNow
		if ctx.Err() != nil {
			return finish("needs_human", "処理が停止されました。変更案は採用していません")
		}
		if writer.failure != nil {
			return finish("needs_human", "結果の反映処理が停止しました: "+writer.failure.Error())
		}
		for _, id := range proposal.RulesApplied {
			if _, err := cat.ReadRule(id); err != nil {
				return finish("failed", "報告されたルールIDが存在しません: "+id)
			}
		}
		switch proposal.Outcome {
		case "needs_human":
			return finish("needs_human", proposal.Note)
		case "skipped":
			if len(proposal.Edits) > 0 {
				return finish("failed", "修正不要の応答に変更が含まれています")
			}
			last := checkpoint.State.LastCandidate
			if !passedNoChangeReview(last, h.InputHash, checkpoint.State.Plan) || last.Result.CandidateID != proposal.CandidateID {
				return finish("needs_human", "変更不要の判断に一致する独立レビューの合格記録がありません")
			}
			fresh, err := catalog.Load(ctx, cfg)
			if err != nil || fresh.Hash != checkpoint.RuleHash || repairSettingsHash(cfg) != checkpoint.SettingsHash {
				return finish("needs_human", "レビュー後にルール・設定が変更されました。対象抽出をやり直してください")
			}
			headNow, headErr := git(ctx, worktree, "rev-parse", "HEAD")
			files, dirtyErr := changedFiles(ctx, worktree)
			current, readErr := os.ReadFile(path)
			if headErr != nil || dirtyErr != nil || readErr != nil || trim(headNow) != writer.acceptedHead || len(files) != 0 || digest(current) != h.InputHash {
				return finish("needs_human", "レビュー後に作業コピーが変更されました。変更を確認してください")
			}
			h.OutputHash = h.InputHash
			h.Checks = []model.Check{{Name: "独立レビュー", Status: "passed", Detail: last.Review.Summary}}
			if err := writeSharedArtifact(cfg, artifact+".after", before, filepath.Join(cfg.Root, filepath.FromSlash(t.File))); err != nil {
				return err
			}
			if err := writeSharedArtifact(cfg, artifact+".diff", []byte{}, filepath.Join(cfg.Root, filepath.FromSlash(t.File))); err != nil {
				return err
			}
			h.DiffPath = artifact + ".diff"
			return finish("skipped", proposal.Note)
		case "modified":
		default:
			return finish("failed", "応答のoutcomeが不正です")
		}
		afterText, e := applyEdits(text.text, proposal.Edits)
		if e != nil {
			return finish("failed", e.Error())
		}
		after, e := text.encode(afterText, proposal.Edits)
		if e != nil {
			return finish("failed", e.Error())
		}
		validatedCandidate := proposal.CandidateID != ""
		if validatedCandidate {
			last := checkpoint.State.LastCandidate
			if last == nil || last.NoChange || !last.Result.Passed || last.Result.AttributionVersion != model.LineAttributionVersion || last.Result.CandidateID != proposal.CandidateID || last.Result.PlanRevision != checkpoint.State.Plan.Revision || last.Request.BaseHash != h.InputHash || last.Result.CandidateHash != digest(after) || !checksPass(last.Result.Checks) {
				return finish("needs_human", "最終候補と保存済みの検証記録が一致しません")
			}
			if !passedIndependentReview(last, h.InputHash, digest(after), checkpoint.State.Plan) {
				return finish("needs_human", "最終候補に一致する独立レビューの合格記録がありません")
			}
			if err := agent.CheckCandidateHolds(checkpoint.State.Plan, last.Request, text.text); err != nil {
				return finish("needs_human", "保留箇所の保護を確認できません: "+err.Error())
			}
			if err := candidateHeldRangesUnchanged(checkpoint.State.Plan, last.Result.EditRanges); err != nil {
				return finish("needs_human", err.Error())
			}
			h.Partial = last.Review.Verdict == "passed_with_holds"
			// The current snapshot must still match the rules/check settings used for validation.
			fresh, err := catalog.Load(ctx, cfg)
			if err != nil || fresh.Hash != checkpoint.RuleHash || repairSettingsHash(cfg) != checkpoint.SettingsHash {
				return finish("needs_human", "検証後にルール・設定が変更されました。対象抽出をやり直してください")
			}
			headNow, err := git(ctx, worktree, "rev-parse", "HEAD")
			files, dirtyErr := changedFiles(ctx, worktree)
			if err != nil || dirtyErr != nil || trim(headNow) != writer.acceptedHead || len(files) != 0 {
				return finish("needs_human", "検証後に作業コピーが変更されました。変更を確認してください")
			}
			h.Checks = append([]model.Check{}, last.Result.Checks...)
		}
		adoptionHead, adoptionErr := git(ctx, worktree, "rev-parse", "HEAD")
		files, dirtyErr := changedFiles(ctx, worktree)
		if adoptionErr != nil || dirtyErr != nil || trim(adoptionHead) != writer.acceptedHead || len(files) != 0 {
			return finish("needs_human", "作業コピーに想定外の変更があります。変更案は採用していません")
		}
		h.CommitBase = writer.acceptedHead
		h.OutputHash = digest(after)
		if e = writeSharedArtifact(cfg, artifact+".after", after, filepath.Join(cfg.Root, filepath.FromSlash(t.File))); e != nil {
			return e
		}
		t.History[len(t.History)-1] = h
		s.updateTask(index, t)
		if e = s.persist(); e != nil {
			return e
		}
		// Verify the source again immediately before publication, not only before
		// the model call, which may take several minutes.
		path, e = catalog.PathWithin(source, t.File)
		if e != nil {
			return finish("needs_human", e.Error())
		}
		current, e := os.ReadFile(path)
		if e != nil || digest(current) != h.InputHash {
			return finish("needs_human", "AIの処理中にファイルが変更されました。変更案は保存しました")
		}
		info, e := os.Stat(path)
		if e != nil {
			return e
		}
		if e = store.WriteFile(path, after, info.Mode().Perm()); e != nil {
			return e
		}
		cleanup := func() error {
			c, stop := context.WithTimeout(context.Background(), 30*time.Second)
			defer stop()
			return rollbackAdoption(c, worktree, rel, h.CommitBase, h.InputHash, h.OutputHash)
		}
		diff, err := git(ctx, worktree, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "HEAD", "--", rel)
		if err != nil {
			if rb := cleanup(); rb != nil {
				return rb
			}
			return finish("failed", err.Error())
		}
		h.DiffPath = artifact + ".diff"
		if e = writeSharedArtifact(cfg, h.DiffPath, []byte(diff), filepath.Join(cfg.Root, filepath.FromSlash(t.File))); e != nil {
			_ = cleanup()
			return e
		}
		if !validatedCandidate {
			h.Checks = append(h.Checks, model.Check{Name: "変更案の適用", Status: "passed", Detail: "完全一致した箇所だけを変更し、文字コード・改行を保持しました"})
		}
		finalScope, scopeErr := scopeCheck(ctx, worktree, rel)
		if scopeErr != nil || finalScope.Status != "passed" {
			h.Checks = append(h.Checks, finalScope)
		}
		actual, readErr := os.ReadFile(path)
		if readErr != nil || digest(actual) != h.OutputHash {
			h.Checks = append(h.Checks, model.Check{Name: "採用前の内容", Status: "failed", Detail: "検証済み候補と一致しません"})
		}
		if !checksPass(h.Checks) || ctx.Err() != nil {
			if e = cleanup(); e != nil {
				return e
			}
			return finish("failed", checkSummary(h.Checks))
		}
		// Durably journal the exact validated bytes before committing. Recovery can
		// distinguish a completed commit from an interrupted, uncommitted attempt.
		h.Outcome = "validated"
		h.Note = proposal.Note
		t.History[len(t.History)-1] = h
		s.updateTask(index, t)
		if e = s.persist(); e != nil {
			_ = cleanup()
			return e
		}
		if _, e = git(ctx, worktree, "add", "--", rel); e != nil {
			if rb := cleanup(); rb != nil {
				return rb
			}
			return finish("failed", e.Error())
		}
		message := "Migrate " + t.File + "\n\nOneByOne-Attempt: " + h.ID + "\nOneByOne-Rules: " + strings.Join(proposal.RulesApplied, ",")
		if _, e = git(ctx, worktree, "commit", "-m", message); e != nil {
			// A canceled commit may still have completed; leave the validated journal
			// and reconcile it on resume instead of blindly resetting a commit.
			return fmt.Errorf("コミットの完了を確認できません。再開時に検証記録と照合します: %w", e)
		}
		commit, e := git(ctx, worktree, "rev-parse", "HEAD")
		if e != nil {
			return e
		}
		h.Commit = trim(commit)
		writer.acceptedHead = h.Commit
		if h.Partial {
			return finish("needs_human", "安全に修正できた箇所を保存しました。一部に要確認の箇所が残っています。\n"+proposal.Note)
		}
		return finish("done", proposal.Note)
	})
}

func (s *Service) updateTask(index int, t model.Task) {
	s.mu.Lock()
	s.state.Tasks[index] = copyTask(t)
	s.recountLocked()
	s.mu.Unlock()
}
func checksPass(checks []model.Check) bool {
	for _, c := range checks {
		if c.Status != "passed" && c.Status != "skipped" {
			return false
		}
	}
	return true
}
func checkSummary(checks []model.Check) string {
	parts := []string{}
	for _, c := range checks {
		if c.Status == "failed" {
			parts = append(parts, c.Name+": "+c.Detail)
		}
	}
	if len(parts) == 0 {
		return "検証または処理が中断しました"
	}
	return strings.Join(parts, "\n")
}

func forbiddenContext(path string) bool {
	for _, part := range strings.Split(strings.ReplaceAll(path, "\\", "/"), "/") {
		p := strings.ToLower(part)
		if p == ".git" || p == ".codex" || p == ".agents" || p == ".claude" || p == ".ssh" || p == ".aws" || p == ".azure" || p == ".npmrc" || p == ".pypirc" || p == ".netrc" || p == "gemini.md" || p == "skill.md" || p == "agents.md" || p == "claude.md" || p == ".env" || strings.HasPrefix(p, ".env.") || strings.HasSuffix(p, ".pem") || strings.HasSuffix(p, ".key") || strings.HasSuffix(p, ".p12") || strings.HasSuffix(p, ".pfx") {
			return true
		}
	}
	return false
}

func readContext(root, path string, start, end int) (string, error) {
	if forbiddenContext(path) {
		return "", fmt.Errorf("エージェント設定・認証ファイルは参照できません")
	}
	if start < 1 || end < start {
		return "", fmt.Errorf("行番号は1以上、終了行は開始行以上で指定してください")
	}
	p, e := catalog.PathWithin(root, path)
	if e != nil {
		return "", e
	}
	info, e := os.Stat(p)
	if e != nil {
		return "", e
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("参照対象は通常ファイルである必要があります")
	}
	b, e := os.ReadFile(p)
	if e != nil {
		return "", e
	}
	text, e := decodeSource(b)
	if e != nil {
		return "", e
	}
	lines := strings.Split(text.text, "\n")
	if start > len(lines) {
		return "", fmt.Errorf("指定行がファイル範囲外です")
	}
	if end > len(lines) {
		end = len(lines)
	}
	out := strings.Join(lines[start-1:end], "\n")
	return out, nil
}

func failureContext(cfg model.Config, t model.Task) string {
	history := repairHistory(t)
	if len(history) < 2 {
		return ""
	}
	h := history[len(history)-2]
	msg := h.Note + "\n" + checkSummary(h.Checks)
	base := filepath.Join(cfg.QueuePath+".artifacts", h.ID)
	if b, err := os.ReadFile(base + ".diff"); err == nil {
		before, beforeErr := os.ReadFile(base + ".before")
		after, afterErr := os.ReadFile(base + ".after")
		if beforeErr == nil && afterErr == nil {
			if diff, err := sourceDiffDisplay(b, before, after); err == nil {
				msg += "\n前回の不合格差分（未採用）:\n" + diff
			}
		}
	}
	return msg
}
