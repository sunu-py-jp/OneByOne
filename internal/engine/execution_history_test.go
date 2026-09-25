package engine

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func TestExecutionHistoryKeepsTargetsAndResultsAtTheirExecutionBoundary(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Modern.Save()\n"})
	s.propose = successfulProposal
	first := runTest(t, s, 1)
	if len(first.ExecutionRuns) != 1 || first.ExecutionRuns[0].Status != "completed" {
		t.Fatalf("missing first run: %+v", first.ExecutionRuns)
	}
	id1 := first.ExecutionRuns[0].ID
	run1, err := s.GetExecutionRun(id1)
	if err != nil || !reflect.DeepEqual(run1.TargetFiles, []string{"A.txt"}) || run1.State.Tasks[0].Status != "done" || run1.State.Tasks[1].Status != "pending" {
		t.Fatalf("first run must retain its limited target set and pending siblings: %+v %v", run1, err)
	}
	if run1.State.Tasks[0].History[0].ExecutionID != id1 {
		t.Fatal("attempt was not associated with its execution")
	}
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		return model.Proposal{Outcome: "skipped", Note: "Already compliant"}, nil
	}
	second := runTest(t, s, 0)
	id2 := second.ExecutionRuns[1].ID
	run2, err := s.GetExecutionRun(id2)
	if err != nil || !reflect.DeepEqual(run2.TargetFiles, []string{"B.txt"}) || run2.State.Tasks[0].Status != "done" || run2.State.Tasks[1].Status != "skipped" {
		t.Fatalf("current completion must remain targeted while prior completion is not targeted: %+v %v", run2, err)
	}
	if _, err = s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	third := runTest(t, s, 0)
	id3 := third.ExecutionRuns[2].ID
	run3, err := s.GetExecutionRun(id3)
	if err != nil || !reflect.DeepEqual(run3.TargetFiles, []string{"A.txt"}) || run3.State.Tasks[0].Status != "done" || run3.State.Tasks[0].History[1].ExecutionID != id3 {
		t.Fatalf("explicit retry must become a target again: %+v %v", run3, err)
	}
	beforeDiscard, err := s.GetExecutionFileDetail(id1, "A.txt", -1)
	if err != nil || beforeDiscard.Before != "Legacy.Save()\n" || beforeDiscard.After != "Modern.Save()\n" || beforeDiscard.Diff == "" {
		t.Fatalf("missing cumulative evidence: %+v %v", beforeDiscard, err)
	}
	history, err := s.GetExecutionFileDetail(id1, "A.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DiscardFileChanges("A.txt"); err != nil {
		t.Fatal(err)
	}
	// Neither mutable artifacts nor rule edits may rewrite an old execution.
	writeTest(t, filepath.Join(cfg.QueuePath+".artifacts", history.Task.History[0].ID+".after"), []byte("later replacement"))
	s.mu.Lock()
	s.state.Rules[0].Title = "Renamed afterwards"
	s.mu.Unlock()
	for _, index := range []int{-1, 0} {
		detail, err := s.GetExecutionFileDetail(id1, "A.txt", index)
		if err != nil || detail.After != "Modern.Save()\n" || detail.Before != "Legacy.Save()\n" {
			t.Fatalf("historical file evidence changed: %+v %v", detail, err)
		}
	}
	unchangedRun1, err := s.GetExecutionRun(id1)
	if err != nil || !reflect.DeepEqual(unchangedRun1, run1) {
		t.Fatal("later executions, discard or rule edits rewrote the first snapshot")
	}
	if err := s.load(cfg.QueuePath); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().ExecutionRuns) != 3 {
		t.Fatal("execution summaries did not survive queue reload")
	}
	export := filepath.Join(t.TempDir(), "execution.json")
	if _, err := s.ExportExecutionReport(id2, export); err != nil {
		t.Fatal(err)
	}
	var report model.ExecutionRunResult
	b, err := os.ReadFile(export)
	if err != nil || json.Unmarshal(b, &report) != nil || report.Run.ID != id2 || report.State.Tasks[0].History[0].ExecutionID != id1 {
		t.Fatalf("report did not export selected execution: %s %v", b, err)
	}
	for _, run := range third.ExecutionRuns {
		dir, _ := executionDirectory(cfg.QueuePath, run.ID)
		b, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
		if err != nil || strings.Contains(string(b), cfg.Credential) || strings.Contains(string(b), `"credential"`) {
			t.Fatalf("credential leaked to execution snapshot: %v", err)
		}
	}
	if _, err := s.ExportExecutionReport(id2, filepath.Join(cfg.QueuePath+".executions", id1, "snapshot.json")); err == nil {
		t.Fatal("report export allowed overwriting immutable history")
	}
}

func TestExecutionHistorySeparatesExplicitResumeWithSameAttemptID(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxAttempts = 1
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		state := *in.RepairState
		state.Plan = engineRepairPlan()
		state.ReadRuleIDs = []string{"R019"}
		state.Usage = model.Usage{InputTokens: 100, Turns: 2}
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		return model.Proposal{}, errors.New("interrupted planned repair")
	}
	first := runTest(t, s, 0)
	id1 := first.ExecutionRuns[0].ID
	oldAttempt := first.Tasks[0].History[0]
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	s.propose = successfulProposal
	second := runTest(t, s, 0)
	if second.Tasks[0].History[0].ID != oldAttempt.ID || second.Tasks[0].History[0].ExecutionID == id1 {
		t.Fatal("resumed attempt was not associated with the new execution")
	}
	frozen, err := s.GetExecutionRun(id1)
	if err != nil || !reflect.DeepEqual(frozen.State.Tasks[0].History[0], oldAttempt) {
		t.Fatalf("same-ID resume changed prior execution attempt: %+v %v", frozen, err)
	}
	detail, err := s.GetExecutionFileDetail(id1, "A.txt", 0)
	if err != nil || detail.After != "Legacy.Save()\n" {
		t.Fatalf("same-ID resume overwrote frozen artifacts: %+v %v", detail, err)
	}
}

func TestExecutionHistoryRecordsPreparationFailureStopAndInterruptedRestart(t *testing.T) {
	t.Run("preparation failure", func(t *testing.T) {
		s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
		writeTest(t, filepath.Join(cfg.RulesPath, "R001", "rule.json"), []byte("invalid json"))
		st := runTest(t, s, 0)
		if len(st.ExecutionRuns) != 1 || st.ExecutionRuns[0].Status != "failed" || st.ExecutionRuns[0].Error == "" {
			t.Fatalf("preparation failure disappeared: %+v", st.ExecutionRuns)
		}
	})
	t.Run("stop and interruption recovery", func(t *testing.T) {
		s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
		entered := make(chan struct{})
		s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
			close(entered)
			<-ctx.Done()
			return model.Proposal{}, ctx.Err()
		}
		if err := s.Start(0); err != nil {
			t.Fatal(err)
		}
		<-entered
		live := s.Snapshot()
		id := live.ExecutionRuns[0].ID
		running, err := s.GetExecutionRun(id)
		if err != nil || running.Run.Status != "running" || !running.State.Running || running.State.Tasks[0].Status != "running" {
			t.Fatalf("active execution not live: %+v %v", running, err)
		}
		s.Stop()
		s.Wait()
		if got := s.Snapshot().ExecutionRuns[0].Status; got != "stopped" {
			t.Fatalf("cancel became %s", got)
		}
		r, err := readExecutionRecord(cfg.QueuePath, id)
		if err != nil {
			t.Fatal(err)
		}
		r.Run.Status, r.Run.FinishedAt, r.EvidenceReady = "running", "", false
		r.State.Running = true
		if err := writeExecutionRecord(r); err != nil {
			t.Fatal(err)
		}
		// A passive diagnostics reader cannot steal the live execution.
		probe := &Service{state: emptyState(cfg)}
		if err := probe.loadExecutionRuns(cfg.QueuePath); err != nil || probe.Snapshot().ExecutionRuns[0].Status != "running" {
			t.Fatalf("passive reader modified execution: %v", err)
		}
		if err := s.load(cfg.QueuePath); err != nil {
			t.Fatal(err)
		}
		recovered, err := s.GetExecutionRun(id)
		if err != nil || recovered.Run.Status != "stopped" || recovered.State.Running || !strings.Contains(recovered.Run.Error, "中断") {
			t.Fatalf("interrupted execution not recovered: %+v %v", recovered, err)
		}
	})
}

func TestExecutionHistoryArchiveFailureBlocksMutationsUntilSaved(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Modern.Save()\n"})
	entered, release := make(chan struct{}), make(chan struct{})
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		close(entered)
		<-release
		return model.Proposal{Outcome: "skipped", Note: "Compliant"}, nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	<-entered
	st := s.Snapshot()
	id, attemptID := st.ExecutionRuns[0].ID, st.Tasks[0].History[0].ID
	dir, _ := executionDirectory(cfg.QueuePath, id)
	obstruction := filepath.Join(dir, "attempts", attemptID+".json")
	if err := os.MkdirAll(obstruction, 0700); err != nil {
		t.Fatal(err)
	}
	close(release)
	s.Wait()
	if s.Snapshot().LastError == "" {
		t.Fatal("archive failure was hidden")
	}
	if _, err := s.RetryTasks([]string{"A.txt"}); err == nil {
		t.Fatal("retry was allowed to rewrite evidence before failed archive saved")
	}
	if err := os.Remove(obstruction); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	frozen, err := s.GetExecutionRun(id)
	if err != nil || frozen.State.Tasks[0].Status != "skipped" || frozen.Run.Status != "completed" || !s.Snapshot().Tasks[0].ResumeRequested {
		t.Fatalf("recovered archive must precede retry mutation: %+v %v", frozen, err)
	}
}

func TestExecutionHistoryRecoverAccountingUsesDurableSidecar(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxAttempts = 1
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		state := *in.RepairState
		state.Plan = engineRepairPlan()
		state.Usage = model.Usage{InputTokens: 321, Turns: 3, Uncertain: true}
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		return model.Proposal{}, errors.New("planned interruption")
	}
	st := runTest(t, s, 0)
	r, err := readExecutionRecord(cfg.QueuePath, st.ExecutionRuns[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	r.State.Tasks[0].History[0].Usage = model.Usage{}
	r.State.Usage = model.Usage{}
	recoverExecutionAccounting(r)
	if r.State.Usage.InputTokens != 321 || r.State.Usage.Turns != 3 || !r.State.Usage.Uncertain {
		t.Fatalf("sidecar usage lost at restart: %+v", r.State.Usage)
	}
	if !executionUsage(model.Usage{Uncertain: true}, model.Usage{Uncertain: true}).Uncertain {
		t.Fatal("an earlier uncertain run must not conceal current uncertain billing")
	}
}
