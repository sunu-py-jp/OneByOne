package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func TestTaskSelectionPersistsAndRunsOnlyCheckedFiles(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	selected, err := s.SetTaskSelection([]string{"B.txt"})
	if err != nil || !selected.Tasks[0].Excluded || selected.Tasks[1].Excluded {
		t.Fatalf("checked subset was not applied: %v", err)
	}
	for _, rule := range selected.Rules {
		if rule.CandidateCount != 1 {
			t.Fatalf("rule %s counted excluded candidates", rule.ID)
		}
	}
	if restored, err := s.SelectWorkspace(selected.ActiveWorkspaceID); err != nil || !restored.Tasks[0].Excluded || restored.Tasks[1].Excluded {
		t.Fatalf("selection did not restore: %v", err)
	}
	visited := []string{}
	s.propose = func(ctx context.Context, input agent.Input) (model.Proposal, error) {
		visited = append(visited, input.File)
		return successfulProposal(ctx, input)
	}
	result := runTest(t, s, 0)
	if !reflect.DeepEqual(visited, []string{"B.txt"}) || result.Tasks[0].Attempts != 0 || len(result.Tasks[0].History) != 0 || result.Tasks[1].Status != "done" {
		t.Fatalf("run processed an excluded file or lost the chosen result: %#v", result.Tasks)
	}
	if readTest(t, filepath.Join(cfg.Root, "A.txt")) != "Legacy.Save()\n" || readTest(t, filepath.Join(cfg.Root, "B.txt")) != "Legacy.Save()\n" {
		t.Fatal("selected run changed the original source")
	}
	reviewed, err := s.Scan()
	if err != nil || reviewed.Running || reviewed.Phase != "idle" || !reviewed.Tasks[0].Excluded || reviewed.Tasks[1].Status != "done" || !reflect.DeepEqual(reviewed.Tasks[1].History, result.Tasks[1].History) {
		t.Fatalf("reopening review reset selection or completed history: %v", err)
	}
	retried, err := s.RetryTasks([]string{"A.txt"})
	if err != nil || retried.Tasks[0].Excluded || retried.Tasks[0].Status != "pending" {
		t.Fatalf("explicit retry did not reinclude the requested file: %v", err)
	}
}

func TestCompletedFileRetrySurvivesScanAndReloadWithoutUndoingAcceptedChanges(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	completed := runTest(t, s, 0)
	if completed.LastError != "" || completed.Tasks[0].Status != "done" {
		t.Fatalf("initial change did not complete: %+v", completed.Tasks)
	}
	history := completed.Tasks[0].History
	acceptedPath := filepath.Join(completed.Worktree, "src", "A.txt")
	acceptedContent := readTest(t, acceptedPath)
	acceptedHead := gitTest(t, completed.Worktree, "rev-parse", "HEAD")
	if acceptedContent != "Modern.Save()\n" {
		t.Fatal("initial change is not available for a completed-file retry")
	}
	if _, err := s.SetTaskSelection(nil); err != nil {
		t.Fatal(err)
	}
	assertQueuedRetry := func(state model.State) {
		t.Helper()
		if state.LastError != "" || len(state.Tasks) != 1 {
			t.Fatalf("retry state did not restore: %s", state.LastError)
		}
		task := state.Tasks[0]
		if task.Status != "pending" || !task.ResumeRequested || task.Excluded || task.Attempts != 1 || task.InputHash != digest([]byte(acceptedContent)) || !reflect.DeepEqual(task.History, history) || !reflect.DeepEqual(task.RulesApplied, completed.Tasks[0].RulesApplied) {
			t.Fatalf("completed-file retry lost its pending marker, selection, or prior evidence: %+v", task)
		}
		if readTest(t, acceptedPath) != acceptedContent || gitTest(t, state.Worktree, "rev-parse", "HEAD") != acceptedHead {
			t.Fatal("requesting a retry undid accepted code or changed Git history")
		}
	}
	queued, err := s.RetryTasks([]string{"src/A.txt"})
	if err != nil {
		t.Fatal(err)
	}
	assertQueuedRetry(queued)
	rescanned, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	assertQueuedRetry(rescanned)
	configPath := s.configPath
	s.Close()
	reloaded := New(configPath)
	t.Cleanup(reloaded.Close)
	assertQueuedRetry(reloaded.Snapshot())
	called := false
	reloaded.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		called = true
		if in.Content != acceptedContent || in.BaseHash != digest([]byte(acceptedContent)) || reloaded.Snapshot().Tasks[0].ResumeRequested {
			t.Fatal("retry did not start from accepted code or did not clear its pending marker")
		}
		return model.Proposal{Outcome: "skipped", Note: "already updated", RulesApplied: []string{"R001", "R019"}}, nil
	}
	finished := runTest(t, reloaded, 0)
	if !called || finished.LastError != "" || finished.Tasks[0].Status != "done" || finished.Tasks[0].ResumeRequested || finished.Tasks[0].Attempts != 2 || len(finished.Tasks[0].History) != 2 || !reflect.DeepEqual(finished.Tasks[0].History[0], history[0]) {
		t.Fatalf("retry did not finish independently while retaining the accepted attempt: %+v", finished.Tasks)
	}
	latest := finished.Tasks[0].History[1]
	if latest.Outcome != "skipped" || latest.Commit != "" || latest.OutputHash != "" || !reflect.DeepEqual(finished.Tasks[0].RulesApplied, completed.Tasks[0].RulesApplied) {
		t.Fatalf("confirmation-only retry fabricated a change or changed adopted rules: %+v", finished.Tasks[0])
	}
	if readTest(t, acceptedPath) != acceptedContent || readTest(t, filepath.Join(cfg.Root, "src", "A.txt")) != "Legacy.Save()\n" || gitTest(t, finished.Worktree, "rev-parse", "HEAD") != acceptedHead {
		t.Fatal("retry changed accepted code or the original target")
	}
}

func TestTaskSelectionSurvivesChangedSourcesAndSelectsNewFiles(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	if _, err := s.SetTaskSelection([]string{"B.txt"}); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(cfg.Root, "A.txt"), []byte("Legacy.Save() // new content\n"))
	writeTest(t, filepath.Join(cfg.Root, "C.txt"), []byte("Legacy.Save()\n"))
	gitTest(t, cfg.Root, "add", ".")
	gitTest(t, cfg.Root, "commit", "-qm", "new source version")
	state, err := s.Scan()
	if err != nil || len(state.Tasks) != 3 || !state.Tasks[0].Excluded || state.Tasks[1].Excluded || state.Tasks[2].Excluded {
		t.Fatalf("rescan lost prior selection or excluded a new file: %v", err)
	}
	if _, err := s.SetTaskSelection(nil); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(0); err == nil || !strings.Contains(err.Error(), "選択されていません") {
		t.Fatalf("empty checked set started work: %v", err)
	}
}

func TestTaskSelectionFailuresPreserveStateAndSavedQueue(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	initial, queue := s.Snapshot(), readTest(t, cfg.QueuePath)
	if _, err := s.SetTaskSelection([]string{"unknown.txt"}); err == nil || !reflect.DeepEqual(initial, s.Snapshot()) || readTest(t, cfg.QueuePath) != queue {
		t.Fatal("unknown selection changed state or queue")
	}
	lease, owner, err := store.AcquireWorkspace(cfg.QueuePath+".lock", store.WorkspaceOwner{})
	if err != nil || owner != nil {
		t.Fatal(err)
	}
	_, err = s.SetTaskSelection([]string{"A.txt"})
	_ = lease.Release()
	if err == nil || !reflect.DeepEqual(initial, s.Snapshot()) || readTest(t, cfg.QueuePath) != queue {
		t.Fatal("busy queue accepted selection changes")
	}
	backup := cfg.QueuePath + ".saved"
	if err := os.Rename(cfg.QueuePath, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cfg.QueuePath, 0700); err != nil {
		t.Fatal(err)
	}
	_, err = s.SetTaskSelection([]string{"A.txt"})
	if err == nil || !reflect.DeepEqual(initial, s.Snapshot()) || readTest(t, backup) != queue {
		t.Fatal("failed atomic queue replacement published selection changes")
	}
	if err := os.Remove(cfg.QueuePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, cfg.QueuePath); err != nil {
		t.Fatal(err)
	}
	reader := workspaceNewService(t, s.configPath)
	if _, err := reader.SetTaskSelection(nil); err == nil {
		t.Fatal("read-only workspace changed file selection")
	}
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	_, err = s.SetTaskSelection(nil)
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	if err == nil {
		t.Fatal("running workspace changed file selection")
	}
}

func TestReviewScanRequiresCleanGitAndAtLeastOneRule(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	initialTasks, queue := s.Snapshot().Tasks, readTest(t, cfg.QueuePath)
	writeTest(t, filepath.Join(cfg.Root, "A.txt"), []byte("uncommitted change\n"))
	if _, err := s.Scan(); err == nil || !strings.Contains(err.Error(), "未コミット") || !reflect.DeepEqual(s.Snapshot().Tasks, initialTasks) || readTest(t, cfg.QueuePath) != queue {
		t.Fatal("dirty target was scanned or replaced the prior queue")
	}
	gitTest(t, cfg.Root, "add", ".")
	gitTest(t, cfg.Root, "commit", "-qm", "updated source")
	for _, rule := range s.Snapshot().Rules {
		if _, err := s.DeleteRule(rule.ID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.Scan(); err == nil || readTest(t, cfg.QueuePath) != queue {
		t.Fatal("review accepted zero rules or replaced the prior queue")
	}
}
