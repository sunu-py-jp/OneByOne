package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestTurnLimitExplainsCurrentExecutionExhaustionWithoutRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		respond(w, goodItem())
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.MaxTurns = 20
	in.RepairState = &model.RepairState{Version: 1, Usage: model.Usage{Turns: 22, InputTokens: 800}}
	var saved model.RepairState
	in.SaveRepairState = func(state model.RepairState) error { saved = state; return nil }
	out, err := Run(context.Background(), in)
	if err == nil || calls != 0 || out.Usage.Turns != 0 || saved.Usage.Turns != 22 || saved.Plan.Revision != 0 {
		t.Fatalf("exhausted resume changed work or sent a request: calls=%d usage=%+v saved=%+v err=%v", calls, out.Usage, saved, err)
	}
	for _, text := range []string{"MaxTurns", "今回 22 / 上限 20", "編集と独立レビュー", "再実行するとターン数は0", "計画と処理履歴は保持"} {
		if !strings.Contains(err.Error(), text) {
			t.Errorf("turn-limit explanation omits %q: %v", text, err)
		}
	}
}

func TestTurnBudgetIsCurrentWithoutGrowingConversation(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		prompt, _ := req["instructions"].(string)
		want := fmt.Sprintf("%d used of 5; %d remaining", 3+calls, 2-calls)
		if !strings.Contains(prompt, want) || strings.Count(prompt, "Current per-file LLM turn budget:") != 1 || !strings.Contains(prompt, "one additional independent-review request") {
			t.Errorf("incorrect current budget: %q", prompt)
		}
		if strings.Contains(fmt.Sprint(req["input"]), "Current per-file LLM turn budget:") {
			t.Error("budget notices accumulated in provider history")
		}
		respond(w, testCall(fmt.Sprintf("context-%d", calls), "read_context", map[string]any{"path": "src/helpers.js", "startLine": 1, "endLine": 1}))
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &model.RepairState{Version: 1, Usage: model.Usage{Turns: 3}}
	out, err := Run(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "今回 5 / 上限 5") || calls != 2 || out.Usage.Turns != 2 {
		t.Fatalf("budget unexpectedly extended: calls=%d out=%+v err=%v", calls, out, err)
	}
}

func TestTurnLimitIncludesLatestFinalRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respond(w, goodItem()) }))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.MaxTurns = 1
	var logs []string
	in.Log = func(message string) { logs = append(logs, message) }
	_, err := Run(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "直前に完了できなかった理由") || !strings.Contains(err.Error(), "Required rules must be reviewed") || !strings.Contains(strings.Join(logs, "\n"), "最終回答の拒否: Required rules must be reviewed") {
		t.Fatalf("missing final rejection: err=%v logs=%v", err, logs)
	}
}

func TestTurnLimitReportsOnlyUncorrectedToolRejection(t *testing.T) {
	for _, corrected := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrected=%t", corrected), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				plan := testPlan(99)
				if calls == 1 {
					plan = testPlan(1)
				}
				respond(w, testCall(fmt.Sprintf("plan-%d", calls), "update_state", plan))
				calls++
			}))
			defer srv.Close()
			in := testInput(srv.URL)
			in.Config.MaxTurns = 1
			if corrected {
				in.Config.MaxTurns = 2
			}
			prior := initializedRepairState()
			in.RepairState = &prior
			var logs []string
			in.Log = func(message string) { logs = append(logs, message) }
			_, err := Run(context.Background(), in)
			if err == nil || !strings.Contains(err.Error(), "MaxTurns") || strings.Contains(err.Error(), "stale plan revision") == corrected {
				t.Errorf("incorrect latest error after correction=%t: %v", corrected, err)
			}
			if !strings.Contains(strings.Join(logs, "\n"), "ツール update_state の拒否: Tool error: stale plan revision") {
				t.Errorf("tool rejection not logged: %v", logs)
			}
		})
	}
}

func TestTurnLimitReportsOnlyUncorrectedValidationRejection(t *testing.T) {
	for _, corrected := range []bool{false, true} {
		t.Run(fmt.Sprintf("corrected=%t", corrected), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				respond(w, testCall(fmt.Sprintf("candidate-%d", calls), "validate_candidate", testCandidate()))
				calls++
			}))
			defer srv.Close()
			in := testInput(srv.URL)
			in.Config.MaxTurns = 1
			if corrected {
				in.Config.MaxTurns = 2
			}
			prior := initializedRepairState()
			in.RepairState = &prior
			validations := 0
			in.ValidateCandidate = func(_ context.Context, req model.CandidateRequest) (model.CandidateValidation, error) {
				validations++
				result := model.CandidateValidation{CandidateID: fmt.Sprintf("C%d", validations), PlanRevision: req.PlanRevision, Passed: validations == 2}
				if !result.Passed {
					result.Diagnostics = []model.CandidateDiagnostic{{Message: "Modern.Flush must run after saving"}}
				}
				return result, nil
			}
			var logs []string
			in.Log = func(message string) { logs = append(logs, message) }
			_, err := Run(context.Background(), in)
			if err == nil || strings.Contains(err.Error(), "Modern.Flush must run after saving") == corrected {
				t.Errorf("incorrect validation error after correction=%t: %v", corrected, err)
			}
			if !strings.Contains(strings.Join(logs, "\n"), "候補検証の不合格: Modern.Flush must run after saving") {
				t.Errorf("validation rejection not logged: %v", logs)
			}
		})
	}
}

func TestTurnLimitAllowsAlreadyReviewedCandidateWithoutRequest(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		respond(w, goodItem())
	}))
	defer srv.Close()
	in, state, _ := reviewGateFixture()
	in.Config.Endpoint, in.Config.Deployment, in.Config.Credential = srv.URL, "mock-model", "test-secret"
	in.Config.MaxTurns = state.Usage.Turns
	in.RepairState = &state
	in.SaveRepairState = func(model.RepairState) error { return nil }
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || calls != 0 || out.Usage.Turns != 0 {
		t.Fatalf("already reviewed adoption blocked or billed: out=%+v calls=%d err=%v", out, calls, err)
	}
}
