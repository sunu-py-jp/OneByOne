package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/solution"
	"onebyone/internal/store"
)

func (s *Service) DiscoverSolutions() ([]string, error) {
	s.mu.Lock()
	root := s.state.Config.Root
	s.mu.Unlock()
	return solution.Discover(root)
}

func solutionCatalog(ctx context.Context, cfg model.Config) (*catalog.Catalog, error) {
	// Compiler evidence is produced by this execution, never a stale import.
	cfg.DiagnosticsPath, cfg.DiagnosticsSourceFile, cfg.DiagnosticsOnly = "", "", false
	cat, err := catalog.Load(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if cat.SelectedRuleCount() == 0 {
		return nil, fmt.Errorf("実行するルールを1件以上選択してください")
	}
	return cat, nil
}

func solutionFiles(info solution.Info) []string {
	set := map[string]bool{info.Path: true}
	for _, path := range append(append([]string{}, info.Files...), info.Projects...) {
		set[path] = true
	}
	files := make([]string, 0, len(set))
	for path := range set {
		files = append(files, path)
	}
	sort.Strings(files)
	return files
}

func (s *Service) scanSolution(cfg model.Config) (model.State, error) {
	cfg, err := normalizeConfig(cfg)
	if err != nil {
		return s.Snapshot(), err
	}
	paths := cfg.Solution.SelectedPaths()
	if paths == nil {
		paths, err = solution.Discover(cfg.Root)
		if err != nil {
			return s.Snapshot(), err
		}
		cfg.Solution.Paths = append([]string{}, paths...)
	}
	if len(paths) == 0 {
		return s.Snapshot(), fmt.Errorf("処理するソリューションを1件以上選択してください")
	}
	files, notes := map[string]bool{}, map[string]string{}
	for _, path := range paths {
		files[path] = true
		info, inspectErr := solution.Inspect(cfg.Root, path)
		if inspectErr != nil {
			notes[path] = inspectErr.Error()
			continue
		}
		for _, file := range solutionFiles(info) {
			files[file] = true
		}
	}
	cat, err := solutionCatalog(context.Background(), cfg)
	if err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	previous := map[string]model.Task{}
	for _, task := range s.state.Tasks {
		previous[task.File] = task
	}
	tasks := []model.Task{}
	for file := range files {
		t, ok := previous[file]
		if !ok {
			t = model.Task{File: file, Status: "pending", History: []model.Attempt{}, Rules: []string{}, RulesApplied: []string{}}
		}
		t.Excluded = false
		if note := notes[file]; note != "" {
			t.Note = note
		}
		tasks = append(tasks, t)
	}
	// Keep accepted history even when an updated project no longer includes it.
	for _, task := range s.state.Tasks {
		if !files[task.File] && len(task.History) > 0 {
			task.Excluded = true
			tasks = append(tasks, task)
		}
	}
	sortTasks(tasks)
	s.state.Config, s.state.Tasks, s.state.Rules = cfg, tasks, cat.Rules
	s.cat, s.meta.RuleHash = cat, cat.Hash
	s.state.ScannedCount = len(tasks)
	s.recountLocked()
	s.mu.Unlock()
	if err = s.persist(); err != nil {
		return s.Snapshot(), err
	}
	return s.Snapshot(), nil
}

// startSolution is called under s.op, after the shared workspace/LLM checks.
func (s *Service) startSolution(cfg model.Config) error {
	if err := s.restorePendingSolution(); err != nil {
		return err
	}
	_, err := s.scanSolution(cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	cfg = s.state.Config
	s.mu.Unlock()
	if _, err = s.ValidateTargetFolder(cfg.Root); err != nil {
		return err
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return fmt.Errorf("アプリは終了しています")
	}
	s.state.Running, s.state.Phase, s.state.LastError = true, "preparing", ""
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.done = cancel, make(chan struct{})
	done := s.done
	s.mu.Unlock()
	if err = s.beginExecution(0); err != nil {
		cancel()
		s.mu.Lock()
		s.state.Running, s.state.Phase, s.cancel = false, "idle", nil
		s.mu.Unlock()
		close(done)
		return err
	}
	go func() {
		defer close(done)
		defer cancel()
		err := s.runSolutionBatch(ctx, cfg)
		if err != nil {
			s.log("error", err.Error())
		}
		s.mu.Lock()
		if err != nil {
			s.state.LastError = s.redactLocked(err.Error())
		}
		s.state.Phase = "finalizing"
		s.setWorkspaceIssueLocked(s.state.ActiveWorkspaceID, "run", err, "results", "")
		s.publishWorkspacesLocked()
		s.mu.Unlock()
		if saveErr := s.finishExecution(ctx, err); saveErr != nil {
			s.log("error", saveErr.Error())
			s.mu.Lock()
			s.state.LastError = saveErr.Error()
			s.mu.Unlock()
		}
		s.mu.Lock()
		s.state.Running, s.state.Phase, s.cancel = false, "idle", nil
		s.state.CurrentFile, s.state.CurrentFiles, s.state.FilePhases = "", nil, nil
		s.mu.Unlock()
	}()
	return nil
}

func (s *Service) solutionProgress(change func(*model.SolutionRun)) error {
	s.mu.Lock()
	change(s.state.SolutionRun)
	s.syncSolutionBatchLocked()
	s.recountLocked()
	s.mu.Unlock()
	return s.persist()
}

func (s *Service) restorePendingSolution() error {
	s.mu.Lock()
	cfg, readOnly, leased, worktree := s.state.Config, s.state.ReadOnly, s.workspaceLease != nil, s.state.Worktree
	s.mu.Unlock()
	if !cfg.SolutionMode() || readOnly || !leased || worktree == "" {
		return nil
	}
	if _, err := os.Stat(cfg.QueuePath + ".solution-adoption.json"); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	unlock, err := lockSharedQueue(cfg)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.recoverSolutionAdoption(ctx, cfg)
}

type solutionAdoption struct {
	Base   string               `json:"base"`
	Commit string               `json:"commit"`
	Tasks  []model.Task         `json:"tasks"`
	Run    model.SolutionRun    `json:"run"`
	Batch  *model.SolutionBatch `json:"batch,omitempty"`
}

// The journal is durable before the fast-forward. Recovery never resets a
// checkout, and refuses unexpected heads or user edits.
func (s *Service) recoverSolutionAdoption(ctx context.Context, cfg model.Config) error {
	b, err := store.ReadFile(cfg.QueuePath + ".solution-adoption.json")
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var j solutionAdoption
	if err = json.Unmarshal(b, &j); err != nil || !isImmutableCommitID(j.Base) || !isImmutableCommitID(j.Commit) {
		return fmt.Errorf("ソリューション採用記録が不正です")
	}
	s.mu.Lock()
	m := s.meta
	tasks := copyTasks(s.state.Tasks)
	s.mu.Unlock()
	if err = validateDiscardWorktree(ctx, cfg, m); err != nil {
		return err
	}
	if err = requireCleanDiscardWorktree(ctx, m.Worktree); err != nil {
		return err
	}
	// An older completed journal is harmless and must not replace newer history.
	for _, t := range tasks {
		for _, a := range t.History {
			if a.Commit == j.Commit {
				return nil
			}
		}
	}
	head, err := git(ctx, m.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	if trim(head) == j.Base {
		parent, e := git(ctx, m.Worktree, "rev-parse", j.Commit+"^")
		if e != nil || trim(parent) != j.Base {
			return fmt.Errorf("ソリューション採用コミットの親が一致しません")
		}
		if _, err = git(ctx, m.Worktree, "merge", "--ff-only", "--no-edit", j.Commit); err != nil {
			return err
		}
	} else if trim(head) != j.Commit {
		return fmt.Errorf("ソリューション採用の途中で作業コピーの履歴が変わりました")
	}
	s.mu.Lock()
	s.state.Tasks, s.state.SolutionRun = j.Tasks, &j.Run
	if j.Batch != nil {
		s.state.SolutionBatch = j.Batch
		s.syncSolutionBatchLocked()
		finishSolutionBatch(s.state.SolutionBatch, false)
	}
	s.recountLocked()
	s.mu.Unlock()
	return s.persist()
}

func (s *Service) runSolution(ctx context.Context, cfg model.Config) (runErr error) {
	if cfg.AuthMode == "oauth" {
		var err error
		cfg, err = s.oauthRunConfig(ctx, cfg)
		if err != nil {
			return err
		}
	}
	cat, err := solutionCatalog(ctx, cfg)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.cat, s.state.Rules = cat, cat.Rules
	s.mu.Unlock()
	if err = s.prepareWorktree(ctx, cfg); err != nil {
		return err
	}
	if err = s.recoverSolutionAdoption(ctx, cfg); err != nil {
		return err
	}
	s.mu.Lock()
	m := s.meta
	oldTasks := copyTasks(s.state.Tasks)
	runID := uid()
	executionID := runID
	if s.activeExecution != nil {
		executionID = s.activeExecution.Run.ID
		runID = executionID
	}
	if s.state.SolutionBatch != nil && s.state.SolutionRun != nil {
		runID = s.state.SolutionRun.ID
	}
	run := &model.SolutionRun{ID: runID, ExecutionID: executionID, Path: cfg.Solution.Path, StartedAt: now(), Status: "running", Builds: []model.SolutionBuild{}, Files: []string{}}
	s.state.SolutionRun = run
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		if runErr != nil {
			run.Status = "failed"
			var held *solutionHoldError
			if errors.As(runErr, &held) {
				run.Status = "needs_human"
			}
			if ctx.Err() != nil {
				run.Status = "stopped"
			}
			run.Note = s.redactLocked(runErr.Error())
			run.Usage.Uncertain = run.Usage.Uncertain || run.RequestPending
			s.recordSolutionFailureLocked(run)
		}
		run.FinishedAt = now()
		s.syncSolutionBatchLocked()
		s.recountLocked()
		s.mu.Unlock()
		if e := s.persist(); e != nil {
			runErr = errors.Join(runErr, e)
		}
	}()
	if err = requireCleanDiscardWorktree(ctx, m.Worktree); err != nil {
		return err
	}
	if err = validateDiscardHistory(ctx, m, oldTasks, ""); err != nil {
		return err
	}
	head, err := git(ctx, m.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	head = trim(head)
	scratch := cfg.QueuePath + ".solution-" + runID
	if _, err = os.Lstat(scratch); !os.IsNotExist(err) {
		return fmt.Errorf("検証用作業コピーが既に存在します: %s", scratch)
	}
	if _, err = git(ctx, m.Worktree, "worktree", "add", "--detach", scratch, head); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Only this execution's uniquely created scratch worktree is removed.
		if _, e := git(cleanupCtx, m.Worktree, "worktree", "remove", "--force", scratch); e != nil {
			s.log("warn", "検証用作業コピーの削除に失敗しました: "+e.Error())
		}
	}()
	source := filepath.Join(scratch, m.SourceRelative)
	info, err := solution.Inspect(source, cfg.Solution.Path)
	if err != nil {
		return &solutionHoldError{err.Error()}
	}
	files := solutionFiles(info)
	original, texts, err := prepareSolutionInputs(ctx, source, m.Worktree, head, m.SourceRelative, files)
	if err != nil {
		return err
	}
	expected := map[string][]byte{}
	for f, b := range original {
		expected[f] = b
	}
	if err = s.solutionProgress(func(r *model.SolutionRun) { r.Files = files; r.SourceFiles = append([]string{}, files...) }); err != nil {
		return err
	}
	var selected []model.Rule
	var ruleFiles map[string][]string
	build := s.buildSolution
	if build == nil {
		build = solution.Build
	}
	propose := s.proposeSolution
	if propose == nil {
		propose = agent.RunSolution
	}
	notes, applied := []string{}, map[string]map[string]bool{}
	seen := map[string]bool{}
	for iteration := 0; ; iteration++ {
		if err = ctx.Err(); err != nil {
			return err
		}
		currentInfo, inspectErr := solution.Inspect(source, cfg.Solution.Path)
		if inspectErr != nil {
			return &solutionHoldError{"修正後のソリューション構成を確認できません: " + inspectErr.Error()}
		}
		if strings.Join(solutionFiles(currentInfo), "\x00") != strings.Join(files, "\x00") {
			return &solutionHoldError{"修正案がソリューションの対象プロジェクト・ソース一覧を変更しました。追加・削除を確認してください"}
		}
		s.mu.Lock()
		s.state.Phase = "building"
		s.state.CurrentFile = cfg.Solution.Path
		run.Iteration = iteration
		s.mu.Unlock()
		s.log("info", fmt.Sprintf("%s · ビルド %d", cfg.Solution.Path, iteration))
		built, buildErr := build(ctx, source, *cfg.Solution)
		built.Iteration = iteration
		if buildErr == nil {
			buildErr = verifySolutionScratch(ctx, scratch, m.SourceRelative, head, expected)
		}
		if buildErr == nil && built.Success && iteration > 0 {
			built, buildErr = s.verifyEarlierSolutions(ctx, source, scratch, m, head, cfg, original, expected, built, build)
		}
		s.mu.Lock()
		built.Output = s.redactSecretsLocked(built.Output)
		for i := range built.Diagnostics {
			built.Diagnostics[i].Message = s.redactSecretsLocked(built.Diagnostics[i].Message)
		}
		s.mu.Unlock()
		if err = s.solutionProgress(func(r *model.SolutionRun) { r.Builds = append(r.Builds, built) }); err != nil {
			return err
		}
		if buildErr != nil {
			return buildErr
		}
		if err = verifySolutionScratch(ctx, scratch, m.SourceRelative, head, expected); err != nil {
			return err
		}
		if iteration == 0 {
			selected, ruleFiles, err = cat.MatchSolutionRules(ctx, source, files, built.Diagnostics)
			if err != nil {
				return err
			}
			selected, ruleFiles, err = s.freezeSolutionRuleScope(cfg, cat.Rules, selected, ruleFiles, files)
			if err != nil {
				return err
			}
			byFile := map[string][]string{}
			for _, file := range files {
				byFile[file] = []string{}
			}
			for _, rule := range selected {
				for _, file := range ruleFiles[rule.ID] {
					byFile[file] = append(byFile[file], rule.ID)
				}
			}
			s.mu.Lock()
			for i := range s.state.Tasks {
				if ids, ok := byFile[s.state.Tasks[i].File]; ok {
					s.state.Tasks[i].Rules = ids
				}
			}
			oldTasks = copyTasks(s.state.Tasks)
			s.recountLocked()
			s.mu.Unlock()
		}
		// Iteration zero also runs the rules even when the initial build succeeds.
		if built.Success && iteration > 0 {
			return s.adoptSolution(ctx, cfg, m, scratch, head, original, expected, oldTasks, run, notes, applied)
		}
		if cfg.Solution.MaxIterations > 0 && iteration >= cfg.Solution.MaxIterations {
			return &solutionHoldError{fmt.Sprintf("修正上限 %d 回に達しました。ビルドエラーが残っています", cfg.Solution.MaxIterations)}
		}
		s.mu.Lock()
		s.state.Phase = "repairing"
		s.mu.Unlock()
		diagnosticsJSON, _ := json.Marshal(built.Diagnostics)
		buildEvidence := fmt.Sprintf("MSBuild success: %t; exit code: %d\nCompiler diagnostics (including errors beyond the log preview):\n%s\nBuild log:\n%s", built.Success, built.ExitCode, diagnosticsJSON, built.Output)
		hookedUsage := false
		proposal, proposeErr := propose(ctx, agent.SolutionInput{
			Config: cfg, Rules: selected, RuleFiles: ruleFiles, Files: texts, BuildOutput: buildEvidence,
			Log: func(msg string) {
				if strings.HasPrefix(msg, "ソリューション全体の独立レビュー") {
					s.mu.Lock()
					s.state.Phase = "reviewing"
					s.mu.Unlock()
				}
				s.log("info", msg)
			},
			BeforeRequest: func(requestID string) error {
				return s.solutionProgress(func(r *model.SolutionRun) { r.RequestPending = true })
			},
			AfterRequest: func(usage model.Usage, requestErr error) error {
				hookedUsage = true
				return s.solutionProgress(func(r *model.SolutionRun) { r.RequestPending = false; addUsage(&r.Usage, usage) })
			},
		})
		if err = s.solutionProgress(func(r *model.SolutionRun) {
			if !hookedUsage {
				addUsage(&r.Usage, proposal.Usage)
			}
			r.Note = proposal.Note
			for _, review := range proposal.Reviews {
				review.Iteration = iteration
				r.Reviews = append(r.Reviews, review)
			}
		}); err != nil {
			return err
		}
		if e := writeOutputJSON(cfg, filepath.Join(cfg.QueuePath+".artifacts", runID+fmt.Sprintf(".solution-%d.json", iteration)), proposal); e != nil {
			return e
		}
		if proposeErr != nil {
			return proposeErr
		}
		if proposal.Status == "needs_human" {
			return &solutionHoldError{proposal.Note}
		}
		if proposal.Status == "skipped" {
			if !built.Success {
				return &solutionHoldError{"ビルドエラーが残っていますが、修正案がありません: " + proposal.Note}
			}
			notes = append(notes, proposal.Note)
			return s.adoptSolution(ctx, cfg, m, scratch, head, original, expected, oldTasks, run, notes, applied)
		}
		if proposal.Status != "modified" || len(proposal.Files) == 0 {
			return &solutionHoldError{"ソリューション全体の有効な修正案がありません: " + proposal.Note}
		}
		after, err := agent.ApplySolutionEdits(texts, proposal.Files)
		if err != nil {
			return err
		}
		next := map[string][]byte{}
		for _, change := range proposal.Files {
			data, ok := expected[change.File]
			if !ok {
				return fmt.Errorf("ソリューション外の編集です: %s", change.File)
			}
			decoded, e := decodeSource(data)
			if e != nil {
				return e
			}
			next[change.File], e = decoded.encode(after[change.File], change.Edits)
			if e != nil {
				return fmt.Errorf("%s: %w", change.File, e)
			}
		}
		for file, data := range next {
			path, e := catalog.PathWithin(source, file)
			if e != nil {
				return e
			}
			fi, e := os.Stat(path)
			if e != nil {
				return e
			}
			if e = store.WriteFile(path, data, fi.Mode().Perm()); e != nil {
				return e
			}
			expected[file] = data
		}
		texts = after
		fingerprint := solutionFingerprint(expected)
		if seen[fingerprint] {
			return &solutionHoldError{"同じ修正内容が繰り返されたため停止しました。ルールとビルドエラーを確認してください"}
		}
		seen[fingerprint] = true
		notes = append(notes, proposal.Note)
		for _, file := range proposal.Files {
			if applied[file.File] == nil {
				applied[file.File] = map[string]bool{}
			}
			for _, id := range file.RuleIDs {
				applied[file.File][id] = true
			}
		}
	}
}

// Failed/stopped solution work still has a billable, reportable attempt. The
// solution path owns accounting once; source-file attempts are created only
// after the complete solution passes its build and review gates.
func (s *Service) recordSolutionFailureLocked(run *model.SolutionRun) {
	for _, t := range s.state.Tasks {
		for _, h := range t.History {
			if attemptMatchesSolutionRun(h, run) {
				return
			}
		}
	}
	for i := range s.state.Tasks {
		t := &s.state.Tasks[i]
		path := run.Path
		if path == "" && s.state.Config.Solution != nil {
			path = s.state.Config.Solution.Path
		}
		if t.File != path {
			continue
		}
		t.Attempts++
		status := "failed"
		if run.Status == "needs_human" || run.Status == "stopped" {
			status = "needs_human"
		}
		t.History = append(t.History, model.Attempt{ID: uid(), ExecutionID: solutionExecutionID(run), SolutionRunID: run.ID, Number: t.Attempts, StartedAt: run.StartedAt, FinishedAt: now(), Outcome: status, Note: run.Note, Usage: run.Usage, RulesApplied: []string{}, Checks: []model.Check{}})
		t.Status, t.Note, t.UpdatedAt = status, run.Note, now()
		return
	}
}

type solutionHoldError struct{ message string }

func (e *solutionHoldError) Error() string { return e.message }

func solutionFingerprint(files map[string][]byte) string {
	paths := make([]string, 0, len(files))
	for f := range files {
		paths = append(paths, f)
	}
	sort.Strings(paths)
	var b strings.Builder
	for _, f := range paths {
		fmt.Fprintf(&b, "%d:%s:%s;", len(f), f, digest(files[f]))
	}
	return digest([]byte(b.String()))
}

func verifySolutionScratch(ctx context.Context, scratch, relative, head string, expected map[string][]byte) error {
	current, err := git(ctx, scratch, "rev-parse", "HEAD")
	if err != nil || trim(current) != head {
		return fmt.Errorf("ビルド中に検証用コピーのHEADが変更されました")
	}
	changed, err := git(ctx, scratch, "diff", "--no-renames", "--name-only", "-z", head, "--")
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for file, data := range expected {
		rel := filepath.ToSlash(filepath.Join(relative, file))
		allowed[rel] = true
		path, e := catalog.PathWithin(scratch, rel)
		if e != nil {
			return e
		}
		actual, e := os.ReadFile(path)
		if e != nil || digest(actual) != digest(data) {
			return fmt.Errorf("ビルドが入力ファイルを書き換えました。採用せず停止します: %s", file)
		}
	}
	for _, path := range zeroLines(changed) {
		if !allowed[path] {
			return fmt.Errorf("検証中に対象外の追跡ファイルが変更されました: %s", path)
		}
	}
	return nil
}

func (s *Service) adoptSolution(ctx context.Context, cfg model.Config, m manifest, scratch, head string, before, after map[string][]byte, previous []model.Task, run *model.SolutionRun, notes []string, applied map[string]map[string]bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := verifySolutionScratch(ctx, scratch, m.SourceRelative, head, after); err != nil {
		return err
	}
	changed := []string{}
	for file, data := range after {
		if digest(data) != digest(before[file]) {
			changed = append(changed, file)
		}
	}
	sort.Strings(changed)
	note := strings.Join(notes, "\n")
	commit := ""
	if len(changed) > 0 {
		tree, err := stageSolutionCandidate(ctx, scratch, head, m.SourceRelative, changed, after)
		if err != nil {
			return err
		}
		value, err := publicationGitInput(ctx, scratch, "OneByOne: repair VB.NET solution\n\nOneByOne-Solution: "+run.ID+"\n", "commit-tree", trim(tree), "-p", head, "-F", "-")
		if err != nil {
			return err
		}
		commit = trim(value)
	}
	updated := map[string]model.Task{}
	for _, t := range previous {
		updated[t.File] = t
	}
	files := make([]string, 0, len(after))
	for file := range after {
		files = append(files, file)
	}
	sort.Strings(files)
	for i, file := range files {
		ruleIDs := []string{}
		for id := range applied[file] {
			ruleIDs = append(ruleIDs, id)
		}
		sort.Strings(ruleIDs)
		t := updated[file]
		t.File = file
		t.Attempts++
		t.Excluded = false
		t.UpdatedAt = now()
		outcome := "skipped"
		fileCommit := ""
		if digest(before[file]) != digest(after[file]) {
			outcome = "done"
			fileCommit = commit
		}
		a := model.Attempt{ID: uid(), ExecutionID: solutionExecutionID(run), SolutionRunID: run.ID, Number: t.Attempts, StartedAt: run.StartedAt, FinishedAt: now(), Outcome: outcome, Note: note, RulesApplied: ruleIDs, BaseCommit: head, CommitBase: head, Commit: fileCommit, InputHash: digest(before[file]), OutputHash: digest(after[file]), Checks: []model.Check{{Name: "ソリューションビルド", Status: "passed", Detail: "VS2022 MSBuild が正常終了しました"}, {Name: "ソリューション独立レビュー", Status: "passed", Detail: "各修正案を別コンテキストでレビューしました"}}}
		if i == 0 {
			a.Usage = run.Usage
		}
		artifact := filepath.Join(cfg.QueuePath+".artifacts", a.ID)
		rel := filepath.ToSlash(filepath.Join(m.SourceRelative, file))
		diff, err := candidateSnapshotDiff(ctx, filepath.Dir(artifact), rel, before[file], after[file])
		if err != nil {
			return err
		}
		for suffix, data := range map[string][]byte{".before": before[file], ".after": after[file], ".diff": []byte(diff)} {
			if err = writeSharedArtifact(cfg, artifact+suffix, data); err != nil {
				return err
			}
		}
		a.DiffPath = artifact + ".diff"
		t.History = append(t.History, a)
		t.Status = outcome
		t.Note = note
		t.InputHash = digest(after[file])
		if outcome == "done" {
			t.RulesApplied = ruleIDs
		} else if canDiscardChanges(t) {
			t.Status = "done"
		}
		updated[file] = t
	}
	tasks := make([]model.Task, 0, len(updated))
	for _, t := range updated {
		tasks = append(tasks, t)
	}
	sortTasks(tasks)
	s.mu.Lock()
	run.Status, run.Note, run.Files, run.Commit = "completed", note, changed, commit
	run.FinishedAt = now()
	s.syncSolutionBatchLocked()
	snapshot := *run
	batchSnapshot := copySolutionBatch(s.state.SolutionBatch)
	s.mu.Unlock()
	if commit != "" {
		if err := requireCleanDiscardWorktree(ctx, m.Worktree); err != nil {
			return err
		}
		current, err := git(ctx, m.Worktree, "rev-parse", "HEAD")
		if err != nil || trim(current) != head {
			return fmt.Errorf("採用直前に作業コピーが変更されました")
		}
		j := solutionAdoption{Base: head, Commit: commit, Tasks: tasks, Run: snapshot, Batch: batchSnapshot}
		if err = writeOutputJSON(cfg, cfg.QueuePath+".solution-adoption.json", j); err != nil {
			return err
		}
		if _, err = git(ctx, m.Worktree, "merge", "--ff-only", "--no-edit", commit); err != nil {
			return err
		}
	}
	s.mu.Lock()
	s.state.Tasks = tasks
	s.recountLocked()
	s.mu.Unlock()
	return s.persist()
}

func (s *Service) GetSolutionFileDetail(file string) (model.FileDetail, error) {
	return s.GetFileDetail(file, -1)
}
