package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

// Exercise the real edit/review transports and adoption gate against a local
// mock. Historical usage exceeds MaxTurns, but each explicit execution gets its
// own allowance and review must still fit in that allowance.
func TestExecutionBudgetEditorAndReviewShareFreshAllowance(t *testing.T) {
	for _, limit := range []int{5, 4} {
		name := "review_fits"
		if limit == 4 {
			name = "review_exceeds_current_allowance"
		}
		t.Run(name, func(t *testing.T) {
			original := "Legacy.Save()\nLegacy.Load()\nflush()\n"
			s, cfg := fixture(t, map[string]string{"A.txt": original})
			cfg.MaxTurns = limit
			if _, err := s.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			seedHistoricalTurnUsage(t, s)
			var calls, reviews atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				turn := int(calls.Add(1))
				var body struct {
					Tools []json.RawMessage `json:"tools"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(500)
					return
				}
				queue, err := store.LoadQueue(cfg.QueuePath)
				if err != nil || len(queue) != 1 || len(queue[0].History) != 3 {
					t.Errorf("missing durable history: %v", err)
					w.WriteHeader(500)
					return
				}
				checkpoint, err := loadRepairCheckpoint(cfg, queue[0].History[2])
				if err != nil || checkpoint == nil || !checkpoint.State.RequestPending || checkpoint.State.Usage.Turns != 22+turn {
					t.Errorf("request lost cumulative reservation: checkpoint=%+v err=%v turn=%d", checkpoint, err, turn)
					w.WriteHeader(500)
					return
				}
				switch turn {
				case 1:
					engineRepairResponse(w, engineRepairCall("read", "read_rule", map[string]string{"id": "R019"}))
				case 2:
					plan := engineRepairPlan()
					plan.Items[0].Risk = "廃止された保存APIでは保存に失敗する"
					plan.Items[1].Risk = "廃止された読込APIでは取得に失敗する"
					engineRepairResponse(w, engineRepairCall("plan", "update_state", model.PlanUpdate{ExpectedRevision: 0, RuleDecisions: plan.RuleDecisions, Items: plan.Items}))
				case 3:
					rows := s.Snapshot().Tasks[0].History[2].Changes
					if len(rows) != 2 || rows[0].Status != "pending" || rows[0].Risk == "" {
						t.Errorf("live plan was not published as unadopted report items: %+v", rows)
					}
					engineRepairResponse(w, engineRepairCall("candidate", "validate_candidate", independentReviewCandidate(original, true, true)))
				case 4:
					last := checkpoint.State.LastCandidate
					if last == nil || !last.Result.Passed {
						t.Error("editor final preceded mechanical validation")
						w.WriteHeader(500)
						return
					}
					independentReviewFinal(w, map[string]string{"outcome": "modified", "candidateId": last.Result.CandidateID, "note": "Updated both calls and retained flush behavior."})
				case 5:
					reviews.Add(1)
					if len(body.Tools) != 0 || checkpoint.State.RequestKind != "review" {
						t.Error("last shared turn was not an independent review")
					}
					independentReviewReply(t, w, original, true)
				default:
					t.Errorf("request exceeded expected shared allowance: %d", turn)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
				if in.RepairState.Usage.Turns != 22 || in.BudgetBaseline.Turns != 22 {
					t.Errorf("editor did not receive a fresh allowance: state=%+v baseline=%+v", in.RepairState, in.BudgetBaseline)
				}
				in.Config.Endpoint = srv.URL
				return agent.Run(ctx, in)
			}
			result := runTest(t, s, 1)
			task := result.Tasks[0]
			if result.LastError != "" || int(calls.Load()) != limit || len(task.History) != 3 || sumUsage(task.History).Turns != 22+limit {
				t.Fatalf("historical usage interfered with current requests/accounting: calls=%d task=%+v error=%s", calls.Load(), task, result.LastError)
			}
			wantReportStatus := "fixed"
			if limit == 4 {
				wantReportStatus = "not_applied"
			}
			checkReport := func(h model.Attempt) {
				t.Helper()
				wantCount := 2
				if limit == 5 {
					wantCount = 3 // The unchanged common rule is verified by review.
				}
				if len(h.Changes) != wantCount {
					t.Fatalf("final report missing plan items: %+v", h.Changes)
				}
				for _, row := range h.Changes {
					if row.RuleID == "R001" && limit == 5 {
						if row.Status != "unchanged" || row.RuleTitle == "" {
							t.Fatalf("reviewed unchanged common rule lost its result: %+v", row)
						}
						continue
					}
					if row.Status != wantReportStatus || row.Risk == "" || row.RuleTitle == "" {
						t.Fatalf("report lost risk/title or promoted an unadopted claim: %+v", row)
					}
				}
			}
			checkReport(task.History[2])
			queue, err := store.LoadQueue(cfg.QueuePath)
			if err != nil || len(queue) != 1 {
				t.Fatalf("cannot reload report history: %v", err)
			}
			checkReport(queue[0].History[2])
			exported, err := s.ExportReport(filepath.Join(t.TempDir(), "execution-report.json"))
			if err != nil {
				t.Fatal(err)
			}
			var exportedState model.State
			if err := json.Unmarshal([]byte(readTest(t, exported)), &exportedState); err != nil {
				t.Fatal(err)
			}
			checkReport(exportedState.Tasks[0].History[2])
			if limit == 5 {
				if task.Status != "done" || reviews.Load() != 1 || len(task.History[2].Reviews) != 1 || task.History[2].Usage.Turns != 5 {
					t.Fatalf("new execution could not complete review: reviews=%d task=%+v", reviews.Load(), task)
				}
				if got := readTest(t, filepath.Join(result.Worktree, "A.txt")); got != "Modern.Save()\nModern.Load()\nflush()\n" {
					t.Fatalf("wrong reviewed output: %q", got)
				}
				// Simulate exit after Git committed but before finish(done). The
				// recovery path must rebuild fixed statuses from matched evidence.
				markInterrupted := func() {
					s.mu.Lock()
					defer s.mu.Unlock()
					s.state.Tasks[0].Status = "running"
					recovering := &s.state.Tasks[0].History[2]
					recovering.Outcome, recovering.Commit, recovering.FinishedAt = "validated", "", ""
					for i := range recovering.Changes {
						recovering.Changes[i].Status = "pending"
					}
				}
				markInterrupted()
				if err := s.recover(context.Background()); err != nil {
					t.Fatal(err)
				}
				checkReport(s.Snapshot().Tasks[0].History[2])
				markInterrupted()
				if err := s.persist(); err != nil {
					t.Fatal(err)
				}
				s.Close()
				reloaded := New(s.configPath)
				t.Cleanup(reloaded.Close)
				reloaded.propose = func(context.Context, agent.Input) (model.Proposal, error) {
					t.Error("committed report recovery must not call the model")
					return model.Proposal{}, nil
				}
				recovered := runTest(t, reloaded, 0)
				if recovered.LastError != "" {
					t.Fatal(recovered.LastError)
				}
				checkReport(recovered.Tasks[0].History[2])
			} else {
				if task.Status != "needs_human" || reviews.Load() != 0 || task.History[2].Commit != "" {
					t.Fatalf("review bypassed the shared execution cap: reviews=%d task=%+v", reviews.Load(), task)
				}
				if got := readTest(t, filepath.Join(result.Worktree, "A.txt")); got != original {
					t.Fatalf("unreviewed candidate was adopted: %q", got)
				}
			}
		})
	}
}
