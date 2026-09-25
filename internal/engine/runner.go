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
		return fmt.Errorf("ルール・チェック定義が変更されています。対象抽出を再実行して候補を更新してください")
	}
	s.mu.Lock()
	s.cat = cat
	s.state.Rules = cat.Rules
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
	s.mu.Lock()
	worktree := s.state.Worktree
	source := s.sourceDirLocked()
	s.state.Phase = "checking"
	s.mu.Unlock()
	base, e := git(ctx, worktree, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	base = trim(base)
	if len(cfg.CheckCommands) > 0 {
		s.log("info", "修正前のビルド・テストを確認しています")
		baseline := runChecks(ctx, cfg, source)
		files, err := changedFiles(ctx, worktree)
		if err != nil {
			return err
		}
		if !checksPass(baseline) || len(files) > 0 {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err = rollback(cleanup, worktree, base); err != nil {
				return err
			}
			return fmt.Errorf("修正前の検証が成功しないか、検証コマンドがソースを変更しました。移行途中でもビルドできる環境を先に準備してください。\n%s", checkSummary(baseline))
		}
	}
	processed := 0
	visited := map[string]bool{}
	// Each Start grants a fresh per-file runtime allowance. Keep baselines for
	// this execution so an automatic retry cannot reset its own limits.
	budgets := map[string]agent.ExecutionBudgetBaseline{}
	attemptsThisRun := map[string]int{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		s.mu.Lock()
		index := -1
		for i := range s.state.Tasks {
			t := &s.state.Tasks[i]
			if !s.executionTargetsLocked(t.File) {
				continue
			}
			if !t.Excluded && (t.Status == "pending" || t.Status == "failed") {
				if !visited[t.File] && limit > 0 && len(visited) >= limit {
					continue
				}
				if cfg.MaxAttempts > 0 && attemptsThisRun[t.File] >= cfg.MaxAttempts {
					t.Status = "needs_human"
					t.Note = "今回の実行で最大試行回数に到達しました: " + t.Note
					t.UpdatedAt = now()
					s.recountLocked()
					continue
				}
				index = i
				visited[t.File] = true
				break
			}
		}
		s.mu.Unlock()
		if index < 0 {
			break
		}
		s.mu.Lock()
		attemptsThisRun[s.state.Tasks[index].File]++
		s.mu.Unlock()
		fatal := s.processOne(ctx, index, cfg, cat, budgets)
		processed++
		if fatal != nil {
			return fatal
		}
	}
	if e = s.persist(); e != nil {
		return e
	}
	s.log("info", fmt.Sprintf("今回の処理を終了しました（%dファイル / %d試行）。結果・差分を確認できます", len(visited), processed))
	return nil
}

func (s *Service) processOne(parent context.Context, index int, cfg model.Config, cat *catalog.Catalog, budgets map[string]agent.ExecutionBudgetBaseline) error {
	s.mu.Lock()
	t := copyTask(s.state.Tasks[index])
	source := s.sourceDirLocked()
	worktree := s.state.Worktree
	rel := filepath.ToSlash(filepath.Join(s.meta.SourceRelative, t.File))
	s.state.CurrentFile = t.File
	s.state.Phase = "running"
	s.mu.Unlock()
	ctx, cancel := executionContext(parent, cfg.TimeoutSeconds)
	defer cancel()
	head, e := git(ctx, worktree, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	head = trim(head)
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
		h.OutputHash = ""
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
	s.updateTask(index, t)
	if e = s.persist(); e != nil {
		return e
	}
	validationLimit := "無制限"
	if cfg.MaxAttempts > 0 {
		validationLimit = fmt.Sprintf("%d回", cfg.MaxAttempts)
	}
	s.log("info", fmt.Sprintf("%s · 試行 %d · 候補検証上限 %s", t.File, t.Attempts, validationLimit))
	finish := func(status, note string) error {
		h.Outcome = status
		h.Note = note
		h.FinishedAt = now()
		h.Changes = buildChangeReport(h, checkpoint)
		t.Status = status
		t.Note = note
		t.UpdatedAt = now()
		if status == "done" {
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
	path, e := catalog.PathWithin(source, t.File)
	if e != nil {
		return finish("needs_human", e.Error())
	}
	if forbiddenContext(t.File) {
		return finish("needs_human", "設定・認証ファイルは自動編集の対象外です")
	}
	if _, e = git(ctx, worktree, "ls-files", "--error-unmatch", "--", rel); e != nil {
		return finish("needs_human", "Gitに登録されていないファイルです")
	}
	before, e := os.ReadFile(path)
	if e != nil {
		return finish("needs_human", e.Error())
	}
	if len(before) > cfg.EffectiveMaxFileBytes() {
		return finish("needs_human", "ファイルが設定されたサイズ上限を超えています")
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
	s.updateTask(index, t)
	if e = s.persist(); e != nil {
		return e
	}
	prior := sumUsage(t.History[:len(t.History)-1])
	if checkpoint == nil {
		checkpoint = &repairCheckpoint{Version: 1, State: model.RepairState{Version: 1, Usage: prior}}
	} else if !resumeAttempt {
		// Retain historical accounting but never reuse a completed plan.
		checkpoint.State.Plan = model.RepairPlan{}
		checkpoint.State.LastCandidate = nil
		checkpoint.State.ReadRuleIDs = nil
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
		h.Changes = buildChangeReport(h, checkpoint)
		t.History[len(t.History)-1] = h
		s.updateTask(index, t)
		s.mu.Lock()
		if state.RequestPending && state.RequestKind == "review" {
			s.state.Phase = "reviewing"
		} else if s.state.Phase == "reviewing" {
			s.state.Phase = "running"
		}
		s.mu.Unlock()
		// The sidecar is the durable per-turn journal. Only attach its path once;
		// rewriting a 10,000-file JSONL queue for every tool/HTTP turn is unnecessary.
		// Candidate publication, final adoption and completion still save the queue.
		if previousPath != h.RepairPath {
			return s.persist()
		}
		return nil
	}
	if e = persistRepair(checkpoint.State); e != nil {
		return e
	}
	if checkpoint.State.Usage.Uncertain && (cfg.MaxCostUSD > 0 || !allowResume) {
		return finish("needs_human", "前回のAPI使用量が未確認です。自動再送はしません。費用上限を設定していない場合に限り、再実行を指定して復帰できます")
	}
	if cfg.MaxCostUSD > 0 && checkpoint.State.Usage.CostUSD >= cfg.MaxCostUSD {
		return finish("needs_human", "このファイルの費用上限に到達しました")
	}
	in := agent.Input{Config: cfg, SystemPrompt: cat.SystemPrompt, File: t.File, Content: text.text, BaseHash: h.InputHash, Rules: cat.Rules, CandidateRules: t.Rules, PreviousFailure: failureContext(cfg, t), RepairState: &checkpoint.State, BudgetBaseline: budgets[t.File], SaveRepairState: persistRepair, AllowUncertainResume: allowResume, ReadRule: cat.ReadRule, ReadContext: func(p string, start, end int) (string, error) {
		return readContext(source, p, start, end, cfg.EffectiveMaxFileBytes())
	}, Log: func(msg string) { s.log("info", t.File+" · "+msg) }}
	in.ValidateCandidate = func(validationCtx context.Context, request model.CandidateRequest) (model.CandidateValidation, error) {
		s.mu.Lock()
		s.state.Phase = "checking"
		s.mu.Unlock()
		result, err := validateCandidate(validationCtx, candidateValidationInput{Config: cfg, Catalog: cat, Source: source, Worktree: worktree, Relative: rel, Head: head, Artifact: artifact, File: t.File, Before: before, InputHash: h.InputHash, Journal: func(result model.CandidateValidation) error {
			h.OutputHash = result.CandidateHash
			t.History[len(t.History)-1] = h
			s.updateTask(index, t)
			return s.persist()
		}}, request)
		if err == nil {
			h.Checks = result.Checks
			if _, statErr := os.Stat(artifact + ".diff"); statErr == nil {
				h.DiffPath = artifact + ".diff"
			}
			t.History[len(t.History)-1] = h
			s.updateTask(index, t)
			err = s.persist()
		}
		s.mu.Lock()
		s.state.Phase = "running"
		s.mu.Unlock()
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
		if agent.IsFatal(e) || (cfg.MaxCostUSD > 0 && (agent.IsUsageUnknown(e) || h.Usage.Uncertain)) {
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
		check, err := cat.CheckLegacy(ctx, cfg, path)
		h.Checks = append(h.Checks, check)
		if err != nil || check.Status == "failed" {
			return finish("failed", "修正不要の申告と旧シンボル検査が矛盾しています: "+check.Detail)
		}
		if check.Status == "skipped" {
			return finish("needs_human", "修正不要との報告ですが、機械的に確認する旧シンボル検査が未設定です。"+proposal.Note)
		}
		return finish("skipped", proposal.Note)
	case "modified":
	default:
		return finish("failed", "応答のoutcomeが不正です")
	}
	afterText, e := applyEdits(text.text, proposal.Edits)
	if e != nil {
		return finish("failed", e.Error())
	}
	after := text.encode(afterText)
	if len(after) > cfg.EffectiveMaxFileBytes() {
		return finish("failed", "修正後のファイルがサイズ上限を超えています")
	}
	validatedCandidate := proposal.CandidateID != ""
	if validatedCandidate {
		last := checkpoint.State.LastCandidate
		if last == nil || !last.Result.Passed || last.Result.CandidateID != proposal.CandidateID || last.Result.PlanRevision != checkpoint.State.Plan.Revision || last.Request.BaseHash != h.InputHash || last.Result.CandidateHash != digest(after) || !checksPass(last.Result.Checks) {
			return finish("needs_human", "最終候補と保存済みの検証記録が一致しません")
		}
		if !passedIndependentReview(last, h.InputHash, digest(after), checkpoint.State.Plan) {
			return finish("needs_human", "最終候補に一致する独立レビューの合格記録がありません")
		}
		// The current snapshot must still match the rules/check settings used for validation.
		fresh, err := catalog.Load(ctx, cfg)
		if err != nil || fresh.Hash != checkpoint.RuleHash || repairSettingsHash(cfg) != checkpoint.SettingsHash {
			return finish("needs_human", "検証後にルール・設定が変更されました。対象抽出をやり直してください")
		}
		headNow, err := git(ctx, worktree, "rev-parse", "HEAD")
		files, dirtyErr := changedFiles(ctx, worktree)
		if err != nil || dirtyErr != nil || trim(headNow) != head || len(files) != 0 {
			return finish("needs_human", "検証後に作業コピーが変更されました。変更を確認してください")
		}
		h.Checks = append([]model.Check{}, last.Result.Checks...)
	}
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
		return rollback(c, worktree, head)
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
		check, err := cat.CheckLegacy(ctx, cfg, path)
		h.Checks = append(h.Checks, check)
		if err != nil {
			check.Status = "failed"
		}
		scope, scopeErr := scopeCheck(ctx, worktree, rel)
		h.Checks = append(h.Checks, scope)
		if err == nil && scopeErr == nil && checksPass(h.Checks) {
			s.mu.Lock()
			s.state.Phase = "checking"
			s.mu.Unlock()
			h.Checks = append(h.Checks, runChecks(ctx, cfg, source)...)
			finalScope, scopeErr := scopeCheck(ctx, worktree, rel)
			if scopeErr != nil || finalScope.Status != "passed" {
				finalScope.Name = "検証後の編集範囲"
				h.Checks = append(h.Checks, finalScope)
			}
			actual, readErr := os.ReadFile(path)
			if readErr != nil || digest(actual) != h.OutputHash {
				h.Checks = append(h.Checks, model.Check{Name: "検証後の内容", Status: "failed", Detail: "検証コマンドが対象ファイルを書き換えました"})
			}
		}
	} else {
		finalScope, scopeErr := scopeCheck(ctx, worktree, rel)
		if scopeErr != nil || finalScope.Status != "passed" {
			h.Checks = append(h.Checks, finalScope)
		}
		actual, readErr := os.ReadFile(path)
		if readErr != nil || digest(actual) != h.OutputHash {
			h.Checks = append(h.Checks, model.Check{Name: "採用前の内容", Status: "failed", Detail: "検証済み候補と一致しません"})
		}
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
	return finish("done", proposal.Note)
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

func runChecks(ctx context.Context, cfg model.Config, dir string) []model.Check {
	ctx, cancel := executionContext(ctx, cfg.TimeoutSeconds)
	defer cancel()
	if len(cfg.CheckCommands) == 0 {
		return []model.Check{{Name: "ビルド・テスト", Status: "skipped", Detail: "未設定（実行していません）"}}
	}
	checks := []model.Check{}
	for _, c := range cfg.CheckCommands {
		start := time.Now()
		args := append([]string(nil), c.Args...)
		out, e := command(ctx, dir, c.Executable, args...)
		name := c.Name
		if name == "" {
			name = c.Executable
		}
		check := model.Check{Name: name, Status: "passed", Detail: out, DurationMS: time.Since(start).Milliseconds()}
		if e != nil {
			check.Status = "failed"
			check.Detail = e.Error()
		}
		checks = append(checks, check)
		if e != nil {
			break
		}
	}
	return checks
}

func executionContext(parent context.Context, seconds int) (context.Context, context.CancelFunc) {
	if seconds > 0 {
		return context.WithTimeout(parent, time.Duration(seconds)*time.Second)
	}
	return context.WithCancel(parent)
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

func readContext(root, path string, start, end, maxBytes int) (string, error) {
	if forbiddenContext(path) {
		return "", fmt.Errorf("エージェント設定・認証ファイルは参照できません")
	}
	if start < 1 || end < start || end-start >= 200 {
		return "", fmt.Errorf("行番号は1以上、一度に200行までです")
	}
	p, e := catalog.PathWithin(root, path)
	if e != nil {
		return "", e
	}
	info, e := os.Stat(p)
	if e != nil {
		return "", e
	}
	if info.Size() > int64(maxBytes) {
		return "", fmt.Errorf("参照ファイルが大きすぎます")
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
	if len(out) > 64<<10 {
		return "", fmt.Errorf("参照範囲を狭めてください（上限64KB）")
	}
	return out, nil
}

func failureContext(cfg model.Config, t model.Task) string {
	history := repairHistory(t)
	if len(history) < 2 {
		return ""
	}
	h := history[len(history)-2]
	msg := h.Note + "\n" + checkSummary(h.Checks)
	if b, e := os.ReadFile(filepath.Join(cfg.QueuePath+".artifacts", h.ID) + ".diff"); e == nil {
		msg += "\n前回の不合格差分（現在は巻き戻し済み）:\n" + string(b)
	}
	if len(msg) > 32768 {
		msg = msg[:32768]
	}
	return msg
}
