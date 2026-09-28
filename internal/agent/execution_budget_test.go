package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestExplicitExecutionKeepsHistoricalAccountingWithoutRuntimeLimits(t *testing.T) {
	prior := model.RepairState{Version: 1, Usage: model.Usage{Turns: 22, InputTokens: 800, OutputTokens: 300}, ElapsedMS: 180000, ToolCalls: 45, ReadBytes: (512 << 10) + 5000, ValidationCount: 7, ReviewCount: 6}
	var saved model.RepairState
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
			return
		}
		prompt, _ := req["instructions"].(string)
		if strings.Contains(prompt, "remaining, including this request") {
			t.Errorf("current execution prompt uses historical turns: %q", prompt)
		}
		if !saved.RequestPending || saved.Usage.Turns != 23+calls || saved.ElapsedMS < 180000 {
			t.Errorf("in-flight reservation did not retain lifetime totals and new deadline: %+v", saved)
		}
		respond(w, standardTurn(calls))
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &prior
	in.BudgetBaseline = BudgetBaselineFor(prior)
	in.SaveRepairState = func(state model.RepairState) error { saved = state; return nil }
	validate := in.ValidateCandidate
	in.ValidateCandidate = func(ctx context.Context, candidate model.CandidateRequest) (model.CandidateValidation, error) {
		if saved.ElapsedMS < 180000 || saved.ValidationCount != 8 || saved.RequestPending || saved.Usage.Uncertain {
			t.Errorf("validation did not reserve this execution's remaining time: %+v", saved)
		}
		return validate(ctx, candidate)
	}
	reviews := 0
	in.ReviewCandidate = func(ctx context.Context, review ReviewInput) (model.IndependentReview, error) {
		reviews++
		if review.TurnBaseline != 22 || review.Usage.Turns != 26 {
			t.Errorf("review lost historical usage or execution baseline: %+v", review)
		}
		reserve := review.BeforeRequest
		review.BeforeRequest = func(id string) error {
			if err := reserve(id); err != nil {
				return err
			}
			if saved.ElapsedMS < 180000 || saved.Usage.Turns != 27 || saved.RequestKind != "review" || !saved.RequestPending {
				t.Errorf("review did not reserve current execution time and turn: %+v", saved)
			}
			return nil
		}
		return passedTestReview(ctx, review)
	}
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || calls != 4 || reviews != 1 || out.Usage.Turns != 5 || saved.Usage.Turns != 27 || saved.Usage.InputTokens != 1200 || saved.ToolCalls != 48 || saved.ValidationCount != 8 || saved.ReviewCount != 7 || saved.ElapsedMS < 180000 || saved.ElapsedMS >= 360000 || saved.ReadBytes <= prior.ReadBytes {
		t.Fatalf("explicit execution did not receive new limits while retaining history: out=%+v saved=%+v calls=%d reviews=%d err=%v", out, saved, calls, reviews, err)
	}
	if prior.Usage.Turns != 22 || prior.ToolCalls != 45 {
		t.Fatal("source journal mutated")
	}
}

func TestExecutionBudgetBaselineRejectsInvalidOffsetsBeforeRequest(t *testing.T) {
	total := model.RepairState{Version: 1, Usage: model.Usage{Turns: 22}, ElapsedMS: 2000, ToolCalls: 40, ReadBytes: 600000, ValidationCount: 5, ReviewCount: 4}
	for _, field := range []string{"turns", "elapsed", "toolCalls", "readBytes", "validations", "reviews"} {
		for _, negative := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s-negative=%v", field, negative), func(t *testing.T) {
				baseline := BudgetBaselineFor(total)
				value := func(n int) int {
					if negative {
						return -1
					}
					return n + 1
				}
				switch field {
				case "turns":
					baseline.Turns = value(baseline.Turns)
				case "elapsed":
					baseline.ElapsedMS = int64(value(int(baseline.ElapsedMS)))
				case "toolCalls":
					baseline.ToolCalls = value(baseline.ToolCalls)
				case "readBytes":
					baseline.ReadBytes = value(baseline.ReadBytes)
				case "validations":
					baseline.ValidationCount = value(baseline.ValidationCount)
				case "reviews":
					baseline.ReviewCount = value(baseline.ReviewCount)
				}
				in := testInput("http://127.0.0.1:1")
				in.RepairState, in.BudgetBaseline = &total, baseline
				if out, err := Run(context.Background(), in); err == nil || !IsFatal(err) || out.Usage.Turns != 0 || !strings.Contains(err.Error(), "baseline") {
					t.Fatalf("invalid baseline accepted or sent request: %+v %v", out, err)
				}
			})
		}
	}
	total.ElapsedMS = math.MaxInt64
	if err := BudgetBaselineFor(total).validate(total); err == nil {
		t.Fatal("overflowing elapsed reservation accepted")
	}
}

func TestRepairCountersAllowLifetimeToolsAndBatchedRuleCatalog(t *testing.T) {
	state := model.RepairState{ToolCalls: 200, ReadBytes: 2000000}
	for i := 0; i < 256; i++ {
		state.ReadRuleIDs = append(state.ReadRuleIDs, fmt.Sprintf("R%03d", i))
	}
	if err := validateRepairCounters(state); err != nil {
		t.Fatal(err)
	}
	state.ReadRuleIDs = append(state.ReadRuleIDs, "R999")
	if err := validateRepairCounters(state); err != nil {
		t.Fatal("large restored rule catalog rejected")
	}
}
