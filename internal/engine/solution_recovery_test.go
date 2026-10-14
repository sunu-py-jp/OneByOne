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
)

func TestSolutionJournalRecoversPostCommitBeforeQueueSave(t *testing.T) {
	s, cfg := solutionFixture(t)
	var beforeQueue, beforeSession, beforeExecution, beforeSummary []byte
	var executionDir string
	builds := 0
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		builds++
		if builds == 1 {
			var err error
			beforeQueue, err = os.ReadFile(cfg.QueuePath)
			if err != nil {
				t.Fatal(err)
			}
			beforeSession, err = os.ReadFile(cfg.QueuePath + ".session.json")
			if err != nil {
				t.Fatal(err)
			}
			executionDir, err = executionDirectory(cfg.QueuePath, s.Snapshot().SolutionRun.ID)
			if err != nil {
				t.Fatal(err)
			}
			beforeExecution, err = os.ReadFile(filepath.Join(executionDir, "snapshot.json"))
			if err != nil {
				t.Fatal(err)
			}
			beforeSummary, err = os.ReadFile(filepath.Join(executionDir, "summary.json"))
			if err != nil {
				t.Fatal(err)
			}
		}
		return model.SolutionBuild{Success: builds > 1, ExitCode: map[bool]int{true: 0, false: 1}[builds > 1]}, nil
	}
	s.proposeSolution = func(context.Context, agent.SolutionInput) (agent.SolutionProposal, error) {
		return solutionProposal("Demo/A.vb", "Demo/B.vb"), nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	completed := s.Snapshot()
	if completed.LastError != "" || completed.SolutionRun == nil || completed.SolutionRun.Commit == "" {
		t.Fatalf("run failed: %+v", completed)
	}
	commit := completed.SolutionRun.Commit
	configPath := s.configPath
	s.Close()
	// A crash can leave the fast-forward and its durable adoption journal on
	// disk while queue/session still hold their pre-adoption version.
	writeTest(t, cfg.QueuePath, beforeQueue)
	writeTest(t, cfg.QueuePath+".session.json", beforeSession)
	writeTest(t, filepath.Join(executionDir, "snapshot.json"), beforeExecution)
	writeTest(t, filepath.Join(executionDir, "summary.json"), beforeSummary)
	reopened := workspaceNewService(t, configPath)
	if st := reopened.Snapshot(); st.LastError != "" || st.ReadOnly || st.SolutionRun == nil || st.SolutionRun.Status != "completed" || st.SolutionRun.Commit != commit {
		t.Fatalf("reopen: %+v", st)
	}
	if gitTest(t, completed.Worktree, "rev-parse", "HEAD") != commit {
		t.Fatal("test did not retain adopted commit")
	}
	if err := reopened.recoverSolutionAdoption(context.Background(), reopened.Snapshot().Config); err != nil {
		t.Fatal(err)
	}
	recovered := reopened.Snapshot()
	if recovered.SolutionRun.Status != "completed" || recovered.SolutionRun.Commit != commit || recovered.Usage.InputTokens != 10 {
		t.Fatalf("recovery lost adoption/accounting: %+v", recovered)
	}
	if !reflect.DeepEqual(recovered.Tasks, completed.Tasks) {
		t.Fatal("journal did not restore the complete per-file history")
	}
	archive, err := reopened.GetExecutionRun(completed.SolutionRun.ID)
	if err != nil || archive.Run.Status != "completed" || archive.State.SolutionRun == nil || archive.State.SolutionRun.Status != "completed" || archive.State.Usage.InputTokens != 10 {
		t.Fatalf("recovered adoption archive: %+v %v", archive, err)
	}
	for _, file := range []string{"Demo/A.vb", "Demo/B.vb"} {
		detail, err := reopened.GetSolutionFileDetail(file)
		if err != nil || !strings.Contains(detail.Diff, "= 2") {
			t.Fatalf("recovered evidence %s: %+v %v", file, detail, err)
		}
	}
	if err := reopened.recoverSolutionAdoption(context.Background(), recovered.Config); err != nil {
		t.Fatal(err)
	}
	if got := reopened.Snapshot(); got.Usage.InputTokens != 10 || !reflect.DeepEqual(got.Tasks, recovered.Tasks) {
		t.Fatal("repeated recovery duplicated history or usage")
	}
	reopened.Close()
	again := workspaceNewService(t, configPath)
	if st := again.Snapshot(); st.SolutionRun.Status != "completed" || st.SolutionRun.Commit != commit || st.Usage.InputTokens != 10 {
		t.Fatalf("recovery did not persist: %+v", st)
	}
	if gitTest(t, completed.Worktree, "rev-parse", "HEAD") != commit {
		t.Fatal("recovery created a second commit")
	}
}

func TestSolutionInterruptedRequestAccountingSurvivesReopen(t *testing.T) {
	s, cfg := solutionFixture(t)
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	if err := s.prepareWorktree(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.beginExecution(0); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	runID := s.activeExecution.Run.ID
	s.state.SolutionRun = &model.SolutionRun{ID: runID, StartedAt: now(), Status: "running", RequestPending: true, Files: []string{"Demo.sln"}, Usage: model.Usage{InputTokens: 17, OutputTokens: 5, Turns: 2}}
	s.state.Running, s.state.Phase = true, "repairing"
	s.recountLocked()
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	configPath := s.configPath
	s.Close()
	reopened := workspaceNewService(t, configPath)
	st := reopened.Snapshot()
	if st.LastError != "" || st.Running || st.SolutionRun == nil || st.SolutionRun.Status != "stopped" {
		t.Fatalf("interrupted state was not recovered: %+v", st)
	}
	if st.Usage.InputTokens != 17 || st.Usage.OutputTokens != 5 || st.Usage.Turns != 2 || !st.Usage.Uncertain {
		t.Fatalf("interrupted usage lost or duplicated: %+v", st.Usage)
	}
	attempts := 0
	for _, task := range st.Tasks {
		for _, attempt := range task.History {
			if attempt.ExecutionID == runID {
				attempts++
				if !attempt.Usage.Uncertain || attempt.Usage.InputTokens != 17 {
					t.Fatalf("attempt accounting: %+v", attempt)
				}
			}
		}
	}
	if attempts != 1 {
		t.Fatalf("expected exactly one solution failure ledger entry, got %d", attempts)
	}
	history, err := reopened.GetExecutionRun(runID)
	if err != nil || history.Run.Status != "stopped" || history.State.Usage.InputTokens != 17 || !history.State.Usage.Uncertain {
		t.Fatalf("interrupted execution archive: %+v %v", history, err)
	}
	if history.State.SolutionRun == nil || history.State.SolutionRun.Status != "stopped" || !history.State.SolutionRun.Usage.Uncertain {
		t.Fatalf("execution archive retained stale running solution: %+v", history.State.SolutionRun)
	}
	if err := reopened.persist(); err != nil {
		t.Fatal(err)
	}
	reopened.Close()
	again := workspaceNewService(t, configPath)
	if got := again.Snapshot(); got.Usage.InputTokens != 17 || !got.Usage.Uncertain || !reflect.DeepEqual(got.Tasks, st.Tasks) {
		t.Fatalf("second reopen duplicated or discarded interrupted accounting: %+v", got)
	}
}

func TestSolutionModeAndBuildSettingsPersistAcrossReopen(t *testing.T) {
	s, cfg := solutionFixture(t)
	cfg.Solution = &model.SolutionSettings{Path: "Demo.sln", Configuration: "Release", Platform: "x64", MSBuildPath: `C:\Program Files\Microsoft Visual Studio\2022\BuildTools\MSBuild\Current\Bin\MSBuild.exe`, MaxIterations: 0, BuildTimeoutSeconds: 91}
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	configPath, workspaceID := s.configPath, s.Snapshot().ActiveWorkspaceID
	s.Close()
	reopened := workspaceNewService(t, configPath)
	st := reopened.Snapshot()
	if st.LastError != "" || st.ReadOnly || st.ActiveWorkspaceID != workspaceID || !st.Config.SolutionMode() || !reflect.DeepEqual(st.Config.Solution, cfg.Solution) {
		t.Fatalf("settings changed after reopening: %+v", st)
	}
	if len(st.Rules) != 1 || st.Rules[0].ID != "raise-value" || st.Rules[0].Body == "" {
		t.Fatalf("solution rules failed to reopen: %+v", st.Rules)
	}
}

func TestSolutionNoChangeGreenRunPersistsWithoutCommit(t *testing.T) {
	s, cfg := solutionFixture(t)
	builds, proposals := 0, 0
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		builds++
		return model.SolutionBuild{Success: true}, nil
	}
	s.proposeSolution = func(context.Context, agent.SolutionInput) (agent.SolutionProposal, error) {
		proposals++
		return agent.SolutionProposal{Status: "skipped", Note: "すべてのルールを確認し、変更不要", Usage: model.Usage{InputTokens: 13, Turns: 2}}, nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	if st.LastError != "" || st.SolutionRun == nil || st.SolutionRun.Status != "completed" || st.SolutionRun.Commit != "" || len(st.SolutionRun.Files) != 0 || builds != 1 || proposals != 1 {
		t.Fatalf("no-change run: %+v", st)
	}
	if st.Usage.InputTokens != 13 || len(st.Tasks) != 4 {
		t.Fatalf("usage/tasks: %+v", st)
	}
	for _, task := range st.Tasks {
		if task.Status != "skipped" || len(task.History) != 1 || task.History[0].Commit != "" || task.History[0].InputHash != task.History[0].OutputHash {
			t.Fatalf("no-change task: %+v", task)
		}
	}
	if gitTest(t, st.Worktree, "rev-parse", "HEAD") != gitTest(t, cfg.Root, "rev-parse", "HEAD") {
		t.Fatal("no-change run created a commit")
	}
	if _, err := os.Stat(cfg.QueuePath + ".solution-adoption.json"); !os.IsNotExist(err) {
		t.Fatalf("no-change run wrote adoption journal: %v", err)
	}
	report, err := s.GetExecutionRun(st.SolutionRun.ID)
	if err != nil || report.State.Usage.InputTokens != 13 || report.Run.Status != "completed" {
		t.Fatalf("report: %+v %v", report, err)
	}
	configPath := s.configPath
	s.Close()
	reopened := workspaceNewService(t, configPath)
	if got := reopened.Snapshot(); got.LastError != "" || got.SolutionRun.Status != "completed" || got.Usage.InputTokens != 13 || !reflect.DeepEqual(got.Tasks, st.Tasks) {
		t.Fatalf("no-change run failed to reopen: %+v", got)
	}
}

func TestSolutionExecutionRetainsSelectedPrivateCredential(t *testing.T) {
	s, _ := solutionFixture(t)
	s.mu.Lock()
	credential := s.state.Config.Credential
	s.mu.Unlock()
	if credential == "" {
		t.Fatal("fixture must have a saved private API key")
	}
	if s.Snapshot().Config.Credential != "" {
		t.Fatal("public snapshot exposed the API key")
	}
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		return model.SolutionBuild{Success: true}, nil
	}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		if in.Config.Credential != credential {
			t.Error("solution execution lost selected private credential while scanning")
		}
		return agent.SolutionProposal{Status: "skipped", Note: "変更不要"}, nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	if st := s.Snapshot(); st.LastError != "" || st.Config.Credential != "" {
		t.Fatalf("execution failed or exposed credential: %s", st.LastError)
	}
}
