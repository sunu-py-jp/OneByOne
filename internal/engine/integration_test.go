package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

func fixture(t *testing.T, sources map[string]string) (*Service, model.Config) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required for isolated worktree integration tests")
	}
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("rg required for scan integration tests")
	}
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	// Use explicit budgets in bounded failure/retry tests. Tests for unset
	// limits clear these three fields before running.
	cfg := DefaultConfig()
	cfg.MaxAttempts, cfg.MaxTurns, cfg.TimeoutSeconds = 3, 12, 600
	cfg.Root = filepath.Join(base, "source")
	cfg.RulesPath = filepath.Join(base, "rules")
	cfg.LegacyPath = filepath.Join(base, "patterns", "legacy-symbols.txt")
	cfg.QueuePath = filepath.Join(base, "session", "queue.jsonl")
	cfg.RGPath = rg
	cfg.Endpoint, cfg.Deployment, cfg.Credential = "https://example.openai.azure.com", "test-deployment", "do-not-persist-secret"
	for file, content := range sources {
		writeTest(t, filepath.Join(cfg.Root, filepath.FromSlash(file)), []byte(content))
	}
	writeTest(t, filepath.Join(cfg.RulesPath, "R001", "rule.json"), fixtureRuleJSON(t, "Preserve behavior", "", "Preserve unrelated code and formatting.", "", "", "", ""))
	writeTest(t, filepath.Join(cfg.RulesPath, "R019", "rule.json"), fixtureRuleJSON(t, "Legacy Save migration", `Legacy\.Save`, "Replace Legacy.Save with Modern.Save.", "Legacy.Save()", "Modern.Save()", "", ""))
	writeTest(t, cfg.LegacyPath, []byte(`\bLegacy\b`))
	gitTest(t, cfg.Root, "init", "-q")
	gitTest(t, cfg.Root, "add", ".")
	gitTest(t, cfg.Root, "commit", "-qm", "initial")
	s := New(filepath.Join(base, "settings.json"))
	t.Cleanup(s.Close)
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	connections, err := s.SaveLLMConnection(model.LLMConnection{
		Name: "Integration test", Provider: cfg.Provider, Endpoint: cfg.Endpoint,
		Deployment: cfg.Deployment, AuthMode: cfg.AuthMode, Credential: cfg.Credential,
	})
	if err != nil || len(connections.LLMConnections) != 1 {
		t.Fatalf("register test connection: %v", err)
	}
	if _, err = s.SelectLLMConnection(connections.LLMConnections[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(); s.Wait() })
	return s, cfg
}

func fixtureRuleJSON(t *testing.T, name, pattern, overview, before, after, notes, hold string) []byte {
	t.Helper()
	data, err := json.Marshal(map[string]any{"version": 1, "name": name, "pattern": pattern, "overview": overview, "before": before, "after": after, "notes": notes, "holdConditions": hold})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func writeTest(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
}

func gitTest(t *testing.T, root string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := git(ctx, root, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return trim(out)
}

func runTest(t *testing.T, s *Service, limit int) model.State {
	t.Helper()
	if err := s.Start(limit); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { s.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		s.Stop()
		t.Fatal("engine did not finish within 30 seconds")
	}
	return s.Snapshot()
}

func successfulProposal(_ context.Context, in agent.Input) (model.Proposal, error) {
	return model.Proposal{Outcome: "modified", Edits: []model.Edit{{OldText: "Legacy.Save()", NewText: "Modern.Save()"}}, RulesApplied: []string{"R019"}, Note: "migrated", Usage: model.Usage{InputTokens: 100, OutputTokens: 20, Turns: 1, CostUSD: .002}}, nil
}

func readTest(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestEndToEndIsolatedCommitAndPersistentResults(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n", "src/context.txt": "untouched\n"})
	originalHead := gitTest(t, cfg.Root, "rev-parse", "HEAD")
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		if in.File == "src/context.txt" {
			if in.Content != "untouched\n" || len(in.CandidateRules) != 0 || !strings.Contains(in.SystemPrompt, "Preserve unrelated code") {
				t.Error("common-only source did not receive an independent rule context")
			}
			return model.Proposal{Outcome: "skipped", RulesApplied: []string{"R001"}, Note: "no edit required"}, nil
		}
		if in.File != "src/A.txt" || in.Content != "Legacy.Save()\n" || len(in.CandidateRules) != 1 || in.CandidateRules[0] != "R019" {
			t.Errorf("wrong per-file input: %+v", in)
		}
		if !strings.Contains(in.SystemPrompt, "Preserve unrelated code") || !strings.Contains(in.SystemPrompt, "R019") {
			t.Error("rules missing from context")
		}
		body, err := in.ReadRule("R019")
		if err != nil || !strings.Contains(body, "Modern.Save") {
			t.Error("rule cannot be read")
		}
		last := -1
		for _, heading := range []string{"# 変更概要", "# 変更前", "# 変更後", "# 備考", "# 修正を保留すべきケース"} {
			position := strings.Index(body, heading)
			if position <= last {
				t.Fatalf("the AI did not receive the structured fields as ordered Markdown: %q", body)
			}
			last = position
			if !strings.Contains(in.SystemPrompt, heading) {
				t.Errorf("common-rule prompt omitted structured section %s", heading)
			}
		}
		if strings.Contains(body, `"holdConditions"`) || strings.Contains(body, `Legacy\.Save`) {
			t.Error("saved JSON or the selection pattern leaked into the AI rule body")
		}
		return successfulProposal(ctx, in)
	}
	st := runTest(t, s, 0)
	if st.LastError != "" || st.Running || len(st.Tasks) != 2 || st.Tasks[0].Status != "done" || st.Tasks[1].Status != "skipped" {
		t.Fatalf("unexpected result: %+v", st)
	}
	if readTest(t, filepath.Join(cfg.Root, "src/A.txt")) != "Legacy.Save()\n" || gitTest(t, cfg.Root, "rev-parse", "HEAD") != originalHead {
		t.Error("original checkout was changed")
	}
	if readTest(t, filepath.Join(st.Worktree, "src/A.txt")) != "Modern.Save()\n" {
		t.Error("worktree did not receive migration")
	}
	if gitTest(t, st.Worktree, "status", "--porcelain") != "" {
		t.Error("successful worktree is dirty")
	}
	if got := gitTest(t, st.Worktree, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"); got != "src/A.txt" {
		t.Errorf("commit includes unexpected files: %s", got)
	}
	if gitTest(t, st.Worktree, "rev-list", "--count", originalHead+"..HEAD") != "1" {
		t.Error("expected one commit")
	}
	queue, err := store.LoadQueue(cfg.QueuePath)
	if err != nil || queue[0].Status != "done" || queue[0].History[0].Commit == "" || queue[0].History[0].OutputHash == "" {
		t.Fatalf("results not persisted: %+v, %v", queue, err)
	}
	detail, err := s.GetFileDetail("src/A.txt", -1)
	if err != nil || detail.Before != "Legacy.Save()\n" || detail.After != "Modern.Save()\n" || !strings.Contains(detail.Diff, "+Modern.Save()") {
		t.Fatalf("missing review artifacts: %+v, %v", detail, err)
	}
	if st.Usage.InputTokens != 100 || st.Usage.CostUSD != .002 {
		t.Errorf("usage not aggregated: %+v", st.Usage)
	}
	if st.Config.Credential != "" || !st.Config.CredentialSet {
		t.Error("snapshot exposed credential or lost presence state")
	}
	for _, path := range []string{s.configPath, cfg.QueuePath + ".session.json"} {
		if strings.Contains(readTest(t, path), cfg.Credential) {
			t.Errorf("credential persisted in %s", path)
		}
	}
	report, err := s.ExportReport(filepath.Join(t.TempDir(), "selected-report.json"))
	if err != nil || strings.Contains(readTest(t, report), cfg.Credential) {
		t.Fatalf("unsafe report: %v", err)
	}
}

func TestPilotLimitAndResume(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 1)
	if st.LastError != "" || st.Tasks[0].Status != "done" || st.Tasks[1].Status != "pending" {
		t.Fatalf("pilot limit failed: %+v", st)
	}
	s.Close()
	reloaded := New(s.configPath)
	t.Cleanup(reloaded.Close)
	reloaded.propose = successfulProposal
	st = runTest(t, reloaded, 0)
	if st.LastError != "" || st.Tasks[1].Status != "done" || st.Tasks[0].Attempts != 1 || st.Tasks[1].Attempts != 1 {
		t.Fatalf("resume failed: %+v", st)
	}
	if got := gitTest(t, st.Worktree, "rev-list", "--count", gitTest(t, cfg.Root, "rev-parse", "HEAD")+"..HEAD"); got != "2" {
		t.Errorf("expected 2 separate commits, got %s", got)
	}
}

func TestPackagePilotResumesWithoutRescan(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	pkg, err := rulepack.Snapshot(cfg.RulesPath, cfg.LegacyPath, rulepack.FromConfig(cfg))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "resume.oborules")
	if err = rulepack.Write(archive, pkg); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ImportRulePackage(archive, "replace"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Scan(); err != nil {
		t.Fatal(err)
	}
	s.propose = successfulProposal
	first := runTest(t, s, 1)
	if first.LastError != "" || first.Tasks[0].Status != "done" || first.Tasks[1].Status != "pending" {
		t.Fatalf("pilot failed: %s", first.LastError)
	}
	s.Close()
	restored := New(s.configPath)
	t.Cleanup(restored.Close)
	restored.propose = successfulProposal
	final := runTest(t, restored, 0)
	if final.LastError != "" || final.Tasks[1].Status != "done" || final.Tasks[0].Attempts != 1 {
		t.Fatalf("package restart did not resume: %s", final.LastError)
	}
}

func TestLegacyResidueRollsBackAndRetriesThreeTimes(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	calls := 0
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		calls++
		if in.Content != "Legacy.Save()\n" {
			t.Error("failed edits leaked into next retry")
		}
		if calls > 1 && (!strings.Contains(in.PreviousFailure, "Legacy") || !strings.Contains(in.PreviousFailure, "diff")) {
			t.Errorf("retry did not receive saved failure: %q", in.PreviousFailure)
		}
		return model.Proposal{Outcome: "modified", Edits: []model.Edit{{OldText: "Save", NewText: "Write"}}, RulesApplied: []string{"R019"}}, nil
	}
	st := runTest(t, s, 0)
	if st.LastError != "" || calls != 3 || st.Tasks[0].Status != "needs_human" || st.Tasks[0].Attempts != 3 || len(st.Tasks[0].History) != 3 {
		t.Fatalf("bad retry result: %+v; calls=%d", st, calls)
	}
	if readTest(t, filepath.Join(st.Worktree, "A.txt")) != "Legacy.Save()\n" || gitTest(t, st.Worktree, "status", "--porcelain") != "" {
		t.Error("failed edits not rolled back")
	}
	if gitTest(t, st.Worktree, "rev-parse", "HEAD") != gitTest(t, cfg.Root, "rev-parse", "HEAD") {
		t.Error("failed attempt committed")
	}
	for _, h := range st.Tasks[0].History {
		if h.DiffPath == "" || !strings.Contains(readTest(t, h.DiffPath), "+Legacy.Write()") {
			t.Error("failure diff missing")
		}
	}
}

func TestSkippedCannotBypassLegacyGate(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		return model.Proposal{Outcome: "skipped", Note: "no change"}, nil
	}
	st := runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" || st.Tasks[0].Attempts != 3 {
		t.Fatalf("skipped bypassed legacy check: %+v", st)
	}
	if st.Tasks[0].History[0].Checks[0].Status != "failed" {
		t.Error("legacy gate not executed")
	}
}

func TestSourceChangesDuringModelCallArePreserved(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		worktree := s.Snapshot().Worktree
		writeTest(t, filepath.Join(worktree, "A.txt"), []byte("human edited this file\n"))
		return successfulProposal(ctx, in)
	}
	st := runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" || st.Tasks[0].History[0].Commit != "" {
		t.Fatalf("concurrent change adopted: %+v", st)
	}
	if readTest(t, filepath.Join(st.Worktree, "A.txt")) != "human edited this file\n" {
		t.Error("concurrent manual change overwritten")
	}
	if readTest(t, filepath.Join(cfg.Root, "A.txt")) != "Legacy.Save()\n" {
		t.Error("original modified")
	}
}

func TestSourceChangesAfterScanDoNotReachModel(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	writeTest(t, filepath.Join(cfg.Root, "A.txt"), []byte("Legacy.Save() // changed\n"))
	gitTest(t, cfg.Root, "add", ".")
	gitTest(t, cfg.Root, "commit", "-qm", "concurrent change")
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		t.Error("stale scanned source reached provider")
		return model.Proposal{}, nil
	}
	st := runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" || !strings.Contains(st.Tasks[0].Note, "抽出時") {
		t.Fatalf("stale scan not rejected: %+v", st)
	}
}

func TestProviderErrorPreservesPartialUsage(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxAttempts = 1
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		return model.Proposal{Usage: model.Usage{InputTokens: 222, OutputTokens: 10, CostUSD: .004, Turns: 2}}, errors.New("malformed final output")
	}
	st := runTest(t, s, 1)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" || st.Usage.InputTokens != 222 || st.Tasks[0].History[0].Usage.CostUSD != .004 {
		t.Fatalf("error lost partial usage: %+v", st)
	}
}

func TestRuleChangesRequireRescan(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	writeTest(t, filepath.Join(cfg.RulesPath, "R019", "rule.json"), fixtureRuleJSON(t, "New rule", `Legacy\.Save`, "Changed rule content", "", "", "", ""))
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		t.Error("stale catalog reached provider")
		return model.Proposal{}, nil
	}
	st := runTest(t, s, 0)
	if st.LastError == "" || st.Tasks[0].Attempts != 0 {
		t.Fatalf("rule change not detected: %+v", st)
	}
}

func TestRecoveryReconcilesCommittedJournal(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "done" {
		t.Fatalf("setup migration failed: %+v", st)
	}
	s.mu.Lock()
	task := &s.state.Tasks[0]
	task.Status = "running"
	task.History[0].Outcome = "validated"
	task.History[0].Commit = ""
	task.History[0].FinishedAt = ""
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reloaded := New(s.configPath)
	t.Cleanup(reloaded.Close)
	reloaded.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		t.Error("committed attempt unnecessarily rerun")
		return model.Proposal{}, nil
	}
	recovered := runTest(t, reloaded, 0)
	if recovered.LastError != "" || recovered.Tasks[0].Status != "done" || recovered.Tasks[0].Attempts != 1 || recovered.Tasks[0].History[0].Commit == "" {
		t.Fatalf("commit recovery failed: %+v", recovered)
	}
}

func TestRecoveryRollsBackInterruptedUncommittedEdit(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	if err := s.prepareWorktree(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	worktree := s.Snapshot().Worktree
	base := gitTest(t, worktree, "rev-parse", "HEAD")
	after := []byte("Modern.Save()\n")
	s.mu.Lock()
	task := &s.state.Tasks[0]
	task.Status, task.Attempts = "running", 1
	task.History = []model.Attempt{{ID: "interrupted-attempt", Number: 1, Outcome: "validated", BaseCommit: base, InputHash: digest([]byte("Legacy.Save()\n")), OutputHash: digest(after), Checks: []model.Check{{Name: "legacy", Status: "passed"}}}}
	s.mu.Unlock()
	writeTest(t, filepath.Join(worktree, "A.txt"), after)
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reloaded := New(s.configPath)
	t.Cleanup(reloaded.Close)
	reloaded.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		if in.Content != "Legacy.Save()\n" {
			t.Error("interrupted edit was not rolled back")
		}
		return successfulProposal(ctx, in)
	}
	st := runTest(t, reloaded, 0)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" || st.Tasks[0].Attempts != 1 || !st.Tasks[0].History[0].Usage.Uncertain {
		t.Fatalf("interrupted request was automatically retried: %+v", st.Tasks[0])
	}
	if readTest(t, filepath.Join(st.Worktree, "A.txt")) != "Legacy.Save()\n" {
		t.Fatal("interrupted candidate was not rolled back")
	}
	if _, err := reloaded.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	st = runTest(t, reloaded, 0)
	if st.LastError != "" || st.Tasks[0].Status != "done" || st.Tasks[0].Attempts != 2 || st.Tasks[0].History[0].Outcome != "interrupted" {
		t.Fatalf("interruption recovery failed: %+v", st)
	}
}

func TestChecksCannotModifyOtherFiles(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "untouched\n"})
	cfg.MaxAttempts = 1
	cfg.CheckCommands = []model.Command{{Name: "mutating checker", Executable: os.Args[0], Args: []string{"-test.run=^TestCommandHelperProcess$", "--", "onebyone-helper", "mutate-after-migration"}}}
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.propose = successfulProposal
	st := runTest(t, s, 1)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" {
		t.Fatalf("checker mutation adopted: %+v", st)
	}
	if readTest(t, filepath.Join(st.Worktree, "A.txt")) != "Legacy.Save()\n" || readTest(t, filepath.Join(st.Worktree, "B.txt")) != "untouched\n" {
		t.Error("checker mutation was not rolled back")
	}
}

func TestCommandHelperProcess(t *testing.T) {
	args := os.Args
	for i, arg := range args {
		if arg != "onebyone-helper" || i+1 >= len(args) {
			continue
		}
		switch args[i+1] {
		case "mutate-after-migration":
			data, err := os.ReadFile("A.txt")
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(2)
			}
			if bytes.Contains(data, []byte("Modern.Save")) {
				if err := os.WriteFile("B.txt", []byte("unexpected checker edit"), 0600); err != nil {
					os.Exit(2)
				}
			}
		case "check-secret-environment":
			if os.Getenv("AZURE_OPENAI_API_KEY") != "" || os.Getenv("AZURE_OPENAI_AUTH_TOKEN") != "" || os.Getenv("OPENAI_API_KEY") != "" || os.Getenv("ANTHROPIC_API_KEY") != "" {
				os.Exit(3)
			}
		}
		os.Exit(0)
	}
}

func TestVerificationCommandDoesNotInheritAzureCredentials(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "api-secret")
	t.Setenv("AZURE_OPENAI_AUTH_TOKEN", "auth-secret")
	t.Setenv("OPENAI_API_KEY", "openai-secret")
	t.Setenv("ANTHROPIC_API_KEY", "anthropic-secret")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := command(ctx, t.TempDir(), os.Args[0], "-test.run=^TestCommandHelperProcess$", "--", "onebyone-helper", "check-secret-environment")
	if err != nil {
		t.Fatalf("verification inherited LLM credentials: %v", err)
	}
}

func TestSnapshotsWhileMigrationRuns(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n", "C.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	done := make(chan struct{})
	var readers sync.WaitGroup
	readers.Add(1)
	go func() {
		defer readers.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			_ = s.Snapshot()
			runtime.Gosched()
		}
	}()
	st := runTest(t, s, 0)
	close(done)
	readers.Wait()
	if st.LastError != "" {
		t.Fatal(st.LastError)
	}
	for _, task := range st.Tasks {
		if task.Status != "done" {
			t.Fatalf("unexpected task: %+v", task)
		}
	}
}

func TestLoadingQueueDoesNotTransferCredentialToDifferentEndpoint(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "")
	t.Setenv("AZURE_OPENAI_AUTH_TOKEN", "")
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	var session manifest
	if err := json.Unmarshal([]byte(readTest(t, cfg.QueuePath+".session.json")), &session); err != nil {
		t.Fatal(err)
	}
	session.Config.Endpoint = "https://different-endpoint.example.com"
	if err := store.WriteJSON(cfg.QueuePath+".session.json", session); err != nil {
		t.Fatal(err)
	}
	st, err := s.LoadQueue(cfg.QueuePath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Config.Endpoint != cfg.Endpoint && st.Config.CredentialSet {
		t.Error("credential carried to endpoint loaded from another queue")
	}
	s.mu.Lock()
	secret := s.state.Config.Credential
	s.mu.Unlock()
	if st.Config.Endpoint != cfg.Endpoint && secret != "" {
		t.Error("prior endpoint credential retained for different endpoint internally")
	}
}

func TestUnknownUsageStopsBudgetedRetriesAcrossRestart(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxCostUSD, cfg.InputPricePerMillion, cfg.OutputPricePerMillion = 1, 1, 1
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		calls++
		return model.Proposal{Usage: model.Usage{Uncertain: true, Turns: 1}}, errors.New("Azure response omitted usage; cost cannot be verified")
	}
	st := runTest(t, s, 0)
	if calls != 1 || st.Tasks[0].Status != "needs_human" || !st.Usage.Uncertain || !st.Tasks[0].History[0].Usage.Uncertain {
		t.Fatalf("unknown usage retried or lost: calls=%d %+v", calls, st)
	}
	s.Close()
	reloaded := New(s.configPath)
	t.Cleanup(reloaded.Close)
	reloaded.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		t.Error("unaccounted cost was forgotten after reload/retry")
		return model.Proposal{}, nil
	}
	if _, err := reloaded.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	st = runTest(t, reloaded, 0)
	if st.Tasks[0].Status != "needs_human" || !strings.Contains(st.Tasks[0].Note, "未確認") {
		t.Fatalf("unknown historical usage bypassed budget: %+v", st)
	}
}

func TestRescanKeepsCommitHistoryAndExcludesPendingTasks(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n", "C.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 1)
	if st.LastError != "" || st.Tasks[0].Status != "done" {
		t.Fatalf("setup failed: %+v", st)
	}
	commit := st.Tasks[0].History[0].Commit
	cfg.IncludeGlobs = []string{"C.txt"}
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	// Rule changes must remain rescan-able while the completed commit survives.
	rule := filepath.Join(cfg.RulesPath, "R019", "rule.json")
	writeTest(t, rule, fixtureRuleJSON(t, "Legacy Save migration", `Legacy\.Save`, "Replace Legacy.Save with Modern.Save.", "Legacy.Save()", "Modern.Save()", "Preserve semantics.", ""))
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	st = s.Snapshot()
	if len(st.Tasks) != 3 || st.Tasks[0].Status != "done" || st.Tasks[0].History[0].Commit != commit || st.Tasks[1].Status != "needs_human" || st.Tasks[2].Status != "pending" {
		t.Fatalf("rescan discarded audit history or retained excluded work: %+v", st)
	}
	st = runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[2].Status != "done" || st.Tasks[1].Attempts != 0 {
		t.Fatalf("rescan made session unresumable: %+v", st)
	}
	if readTest(t, filepath.Join(st.Worktree, "B.txt")) != "Legacy.Save()\n" {
		t.Error("excluded pending file was migrated")
	}
}

func TestRescanDoesNotOverwriteMalformedExistingLedger(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	writeTest(t, cfg.QueuePath, []byte("broken ledger"))
	if _, err := s.Scan(); err == nil {
		t.Error("malformed ledger silently overwritten")
	}
	if readTest(t, cfg.QueuePath) != "broken ledger" {
		t.Error("malformed ledger content lost")
	}
}

func TestImportedEndpointCannotAcquireEnvironmentCredential(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "environment-secret")
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.Close()
	// A different local user has no trusted selection for this shared queue.
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(t.TempDir(), "other-private"))
	fresh := New("")
	t.Cleanup(fresh.Close)
	st, err := fresh.LoadQueue(cfg.QueuePath)
	if err != nil {
		t.Fatal(err)
	}
	if st.Config.Endpoint != "" || st.Config.Credential != "" || st.SelectedLLMConnectionID != "" || st.Config.CredentialSet {
		t.Error("imported endpoint became eligible to receive environment credential")
	}
	_ = s
}

func TestRescanRequiresLoadingExistingWorktreeSession(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	if st.LastError != "" {
		t.Fatal(st.LastError)
	}
	before := readTest(t, cfg.QueuePath+".session.json")
	s.Close()
	fresh := New(filepath.Join(t.TempDir(), "app-settings.json"))
	t.Cleanup(fresh.Close)
	if _, err := fresh.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Scan(); err == nil {
		t.Error("scan overwrote an unloaded existing worktree session")
	}
	if readTest(t, cfg.QueuePath+".session.json") != before {
		t.Error("existing worktree identity was lost")
	}
}
