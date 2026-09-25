package engine

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func TestNoAdditionalChangeReflectsRemainingAcceptedChanges(t *testing.T) {
	for _, scenario := range []struct {
		name     string
		adopted  bool
		cleanup  string
		wantDone bool
	}{
		{name: "unchanged from first execution"},
		{name: "accepted changes remain", adopted: true, wantDone: true},
		{name: "accepted changes were discarded", adopted: true, cleanup: "discard"},
		{name: "later accepted changes cancel earlier changes", adopted: true, cleanup: "restore"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			const original = "Modern.Save()\n"
			s, cfg := fixture(t, map[string]string{"A.txt": original})
			edit := func(before, after string) func(context.Context, agent.Input) (model.Proposal, error) {
				return func(context.Context, agent.Input) (model.Proposal, error) {
					return model.Proposal{Outcome: "modified", Edits: []model.Edit{{OldText: before, NewText: after}}, RulesApplied: []string{"R001"}, Note: "updated"}, nil
				}
			}
			if scenario.adopted {
				s.propose = edit("Modern.Save()", "Modern.Write()")
				st := runTest(t, s, 0)
				if st.LastError != "" || st.Tasks[0].Status != "done" || !st.Tasks[0].CanDiscardChanges {
					t.Fatalf("initial edit was not adopted: %+v", st.Tasks)
				}
				switch scenario.cleanup {
				case "discard":
					if _, err := s.DiscardFileChanges("A.txt"); err != nil {
						t.Fatal(err)
					}
				case "restore":
					if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
						t.Fatal(err)
					}
					s.propose = edit("Modern.Write()", "Modern.Save()")
					st = runTest(t, s, 0)
					if st.LastError != "" || st.Tasks[0].Status != "done" || st.Tasks[0].CanDiscardChanges || readTest(t, filepath.Join(st.Worktree, "A.txt")) != original {
						t.Fatalf("accepted restoration did not cancel the original difference: %+v", st.Tasks)
					}
				}
				if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
					t.Fatal(err)
				}
			}
			before := s.Snapshot()
			head := ""
			if before.Worktree != "" {
				head = gitTest(t, before.Worktree, "rev-parse", "HEAD")
			}
			s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
				return model.Proposal{Outcome: "skipped", RulesApplied: []string{"R001"}, Note: "no additional changes"}, nil
			}
			st := runTest(t, s, 0)
			task := st.Tasks[0]
			want := "skipped"
			if scenario.wantDone {
				want = "done"
			}
			if st.LastError != "" || task.Status != want || task.CanDiscardChanges != scenario.wantDone {
				t.Fatalf("unexpected overall state after no additional changes: %+v", task)
			}
			history := task.History
			latest := history[len(history)-1]
			if latest.Outcome != "skipped" || latest.Commit != "" || !reflect.DeepEqual(task.RulesApplied, before.Tasks[0].RulesApplied) {
				t.Fatalf("confirmation-only attempt changed adopted evidence: %+v", task)
			}
			if len(before.Tasks[0].History) > 0 && !reflect.DeepEqual(history[:len(history)-1], before.Tasks[0].History) {
				t.Fatal("prior attempt evidence was rewritten")
			}
			if head != "" && gitTest(t, st.Worktree, "rev-parse", "HEAD") != head {
				t.Fatal("confirmation-only attempt created a commit")
			}
			queue, err := store.LoadQueue(cfg.QueuePath)
			if err != nil || queue[0].Status != want || queue[0].History[len(history)-1].Outcome != "skipped" {
				t.Fatalf("overall state and per-attempt outcome were not persisted independently: %+v %v", queue, err)
			}
		})
	}
}

func TestAcceptedChangesDoNotHideRetryFailureOrHumanReview(t *testing.T) {
	for _, outcome := range []string{"needs_human", "invalid"} {
		t.Run(outcome, func(t *testing.T) {
			s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
			s.propose = successfulProposal
			before := runTest(t, s, 0)
			if before.Tasks[0].Status != "done" {
				t.Fatal("initial edit was not adopted")
			}
			head := gitTest(t, before.Worktree, "rev-parse", "HEAD")
			if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
				t.Fatal(err)
			}
			s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
				return model.Proposal{Outcome: outcome, Note: "cannot complete this retry"}, nil
			}
			st := runTest(t, s, 0)
			task := st.Tasks[0]
			wantAttempt := "needs_human"
			if outcome == "invalid" {
				wantAttempt = "failed"
			}
			if st.LastError != "" || task.Status != "needs_human" || !task.CanDiscardChanges || task.History[len(task.History)-1].Outcome != wantAttempt || !reflect.DeepEqual(task.History[0], before.Tasks[0].History[0]) {
				t.Fatalf("previously accepted changes concealed the retry problem: %+v", task)
			}
			if gitTest(t, st.Worktree, "rev-parse", "HEAD") != head {
				t.Fatal("unsuccessful retry changed accepted code")
			}
		})
	}
}
