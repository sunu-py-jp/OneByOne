package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func TestIndividualFileDetailAddsDisplayProvenanceWithoutChangingHistory(t *testing.T) {
	h := cumulativeReportAttempt("first", 1, model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2})
	task := model.Task{File: "src/A.txt", History: []model.Attempt{h}}
	want := copyTask(task)
	s := &Service{state: model.State{Config: model.Config{QueuePath: filepath.Join(t.TempDir(), "queue.jsonl")}, Tasks: []model.Task{task}}}
	detail, err := s.GetFileDetail(task.File, 0)
	if err != nil || detail.Cumulative || len(detail.Changes) != 1 || detail.Changes[0].SourceAttemptID != h.ID || detail.Changes[0].ID != h.Changes[0].ID {
		t.Fatalf("individual report lost its attempt provenance or original item ID: %+v %v", detail, err)
	}
	if !reflect.DeepEqual(detail.Task, want) || !reflect.DeepEqual(s.state.Tasks[0], task) || s.state.Tasks[0].History[0].Changes[0].SourceAttemptID != "" {
		t.Fatal("reading display provenance changed the response or stored attempt history")
	}
	encoded, err := json.Marshal(detail)
	if err != nil || strings.Count(string(encoded), `"sourceAttemptId":"first"`) != 1 {
		t.Fatalf("only the detail row should expose its source attempt: %s %v", encoded, err)
	}
}

func TestCumulativeFileDetailPreservesAdoptedChangesAcrossRetriesAndDiscard(t *testing.T) {
	const original = "Legacy.Save()\n"
	s, cfg := fixture(t, map[string]string{"src/A.txt": original})
	assertCumulative := func(service *Service, after string) model.FileDetail {
		t.Helper()
		detail, err := service.GetFileDetail("src/A.txt", -1)
		if err != nil || !detail.Cumulative || detail.Before != original || detail.After != after || detail.Changes == nil {
			t.Fatalf("unexpected cumulative detail: %+v, %v", detail, err)
		}
		if after == original && detail.Diff != "" {
			t.Fatalf("unchanged cumulative result has a diff: %s", detail.Diff)
		}
		if after != original && (!strings.Contains(detail.Diff, "-Legacy.Save()") || !strings.Contains(detail.Diff, "+"+strings.TrimSpace(after))) {
			t.Fatalf("cumulative diff does not compare the original to the adopted result: %s", detail.Diff)
		}
		return detail
	}
	initial := assertCumulative(s, original)
	encoded, err := json.Marshal(initial)
	if err != nil || !strings.Contains(string(encoded), `"changes":[]`) {
		t.Fatal("an empty cumulative change report must be an explicit array")
	}
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "done" {
		t.Fatalf("initial edit was not adopted: %+v", st.Tasks)
	}
	assertCumulative(s, "Modern.Save()\n")
	if _, err = s.RetryTasks([]string{"src/A.txt"}); err != nil {
		t.Fatal(err)
	}
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		return model.Proposal{Outcome: "skipped", Note: "no additional changes", RulesApplied: []string{"R001", "R019"}}, nil
	}
	st = runTest(t, s, 0)
	assertCumulative(s, "Modern.Save()\n")
	individual, err := s.GetFileDetail("src/A.txt", 1)
	if err != nil || individual.Cumulative || individual.Before != "Modern.Save()\n" || individual.After != individual.Before || individual.Diff != "" || individual.Task.History[1].Outcome != "skipped" {
		t.Fatalf("the confirmation-only attempt was replaced by cumulative changes: %+v %v", individual, err)
	}
	if _, err = s.RetryTasks([]string{"src/A.txt"}); err != nil {
		t.Fatal(err)
	}
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		return model.Proposal{Outcome: "modified", Edits: []model.Edit{{OldText: "Modern.Save()", NewText: "Modern.Send()"}}, RulesApplied: []string{"R001"}, Note: "second update"}, nil
	}
	st = runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "done" {
		t.Fatalf("second edit was not adopted: %+v", st.Tasks)
	}
	assertCumulative(s, "Modern.Send()\n")
	individual, err = s.GetFileDetail("src/A.txt", 2)
	if err != nil || individual.Cumulative || individual.Before != "Modern.Save()\n" || individual.After != "Modern.Send()\n" || strings.Contains(individual.Diff, "Legacy") {
		t.Fatalf("the second attempt lost its own before/after comparison: %+v %v", individual, err)
	}
	// Artifact files are report evidence, not the authoritative adopted state.
	artifact := filepath.Join(cfg.QueuePath+".artifacts", st.Tasks[0].History[2].ID+".after")
	accepted := readTest(t, artifact)
	writeTest(t, artifact, []byte("unadopted artifact contents\n"))
	assertCumulative(s, "Modern.Send()\n")
	writeTest(t, artifact, []byte(accepted))
	if _, err = s.RetryTasks([]string{"src/A.txt"}); err != nil {
		t.Fatal(err)
	}
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		return model.Proposal{Outcome: "modified", Edits: []model.Edit{{OldText: "Modern.Send()", NewText: "Legacy.Send()"}}, RulesApplied: []string{"R019"}, Note: "this candidate must fail the legacy check"}, nil
	}
	st = runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" {
		t.Fatalf("invalid candidate was not rejected: %+v", st.Tasks)
	}
	assertCumulative(s, "Modern.Send()\n")
	individual, err = s.GetFileDetail("src/A.txt", len(st.Tasks[0].History)-1)
	if err != nil || individual.Cumulative || individual.Before != "Modern.Send()\n" || individual.After != "Legacy.Send()\n" {
		t.Fatalf("failed candidate is not available in its own history: %+v %v", individual, err)
	}
	// A validator may write a proposal into the worktree temporarily. It must
	// never leak into a cumulative detail request made during that attempt.
	worktreeFile := filepath.Join(st.Worktree, "src", "A.txt")
	writeTest(t, worktreeFile, []byte("candidate currently under validation\n"))
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	assertCumulative(s, "Modern.Send()\n")
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	writeTest(t, worktreeFile, []byte("Modern.Send()\n"))
	if _, err = s.DiscardFileChanges("src/A.txt"); err != nil {
		t.Fatal(err)
	}
	assertCumulative(s, original)
	individual, err = s.GetFileDetail("src/A.txt", 0)
	if err != nil || individual.Cumulative || individual.Before != original || individual.After != "Modern.Save()\n" {
		t.Fatalf("discard removed the earlier attempt's evidence: %+v %v", individual, err)
	}
	configPath := s.configPath
	s.Close()
	reloaded := New(configPath)
	t.Cleanup(reloaded.Close)
	assertCumulative(reloaded, original)
}

func TestCumulativeFileDetailReadsFrozenBaselineForUnprocessedFile(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "unchanged\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 1)
	if st.LastError != "" || st.Tasks[1].Attempts != 0 {
		t.Fatalf("second file should not have run yet: %+v", st.Tasks)
	}
	writeTest(t, filepath.Join(st.Worktree, "B.txt"), []byte("uncommitted external change\n"))
	detail, err := s.GetFileDetail("B.txt", -1)
	if err != nil || detail.Before != "unchanged\n" || detail.After != detail.Before || detail.Diff != "" || !detail.Cumulative {
		t.Fatalf("unprocessed detail read a moving worktree instead of the run baseline: %+v %v", detail, err)
	}
}

func TestCumulativeFileDetailRejectsAnUnverifiableAdoptedResult(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	if st.Tasks[0].Status != "done" {
		t.Fatal("initial edit was not adopted")
	}
	s.mu.Lock()
	s.state.Tasks[0].History[0].OutputHash = digest([]byte("something else"))
	s.mu.Unlock()
	if _, err := s.GetFileDetail("A.txt", -1); err == nil || !strings.Contains(err.Error(), "ハッシュ") {
		t.Fatalf("mismatched adopted content was presented as a valid result: %v", err)
	}
	// The persisted history and source were untouched by the read failure.
	if _, err := os.Stat(filepath.Join(st.Worktree, "A.txt")); err != nil {
		t.Fatal(err)
	}
}
