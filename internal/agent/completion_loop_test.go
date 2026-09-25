package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"onebyone/internal/model"
)

func TestCompletionLoopRejectsPrematureHoldAndFinishesWithReview(t *testing.T) {
	prior := initializedRepairState()
	var requests atomic.Int32
	var saved model.RepairState
	validations, reviews := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		switch requests.Add(1) {
		case 1:
			respond(w, reviewResponseItem(map[string]any{"outcome": "needs_human", "candidateId": "", "note": "全修正の適用と再検証が残っているため、この実行枠では完了できません。"}))
		case 2:
			if !strings.Contains(fmt.Sprint(history), "recorded human-decision blocker") || !strings.Contains(fmt.Sprint(history), "Continue correcting the plan/candidate") {
				t.Error("premature hold was not returned to the editor as actionable feedback")
			}
			respond(w, testCall("complete", "validate_candidate", testCandidate()))
		case 3:
			respond(w, goodItem())
		default:
			t.Error("unexpected editor request after completion")
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &prior
	in.SaveRepairState = func(state model.RepairState) error { saved = state; return nil }
	validator := in.ValidateCandidate
	in.ValidateCandidate = func(ctx context.Context, candidate model.CandidateRequest) (model.CandidateValidation, error) {
		validations++
		if len(candidate.Edits) != 1 || len(candidate.AddressedItemIDs) != 1 || candidate.AddressedItemIDs[0] != "P1" {
			t.Error("continued editor did not submit the complete planned candidate")
		}
		return validator(ctx, candidate)
	}
	in.ReviewCandidate = func(ctx context.Context, review ReviewInput) (model.IndependentReview, error) {
		reviews++
		if review.Before != in.Content || review.After != "Modern.Save()\n" || len(review.Rules) != 1 || review.Rules[0].ID != "R019" {
			t.Error("independent reviewer did not receive the complete source, candidate and rules")
		}
		return passedTestReview(ctx, review)
	}
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || out.CandidateID != "C1" || requests.Load() != 3 || validations != 1 || reviews != 1 || out.Usage.Turns != 4 {
		t.Fatalf("premature hold did not continue through validation and review: out=%+v err=%v requests=%d validations=%d reviews=%d", out, err, requests.Load(), validations, reviews)
	}
	if saved.RequestPending || saved.Plan.Revision != 1 || saved.Plan.Items[0].Status == "blocked" || len(saved.Reviews) != 1 || saved.Reviews[0].Verdict != "passed" {
		t.Fatalf("completion did not preserve the active plan and passed review: %+v", saved)
	}
}

func TestCompletionLoopRecordedHumanBlockerBypassesReview(t *testing.T) {
	for _, scope := range []string{"item", "rule"} {
		t.Run(scope, func(t *testing.T) {
			prior := initializedRepairState()
			update := testPlan(1)
			update.Items[0].Status = "blocked"
			if scope == "item" {
				update.Items[0].HoldReason = "The external Save contract is missing; confirm whether the callback owns the transaction."
			} else {
				update.RuleDecisions[0].Decision = "blocked"
				update.RuleDecisions[0].Reason = "The replacement API requires a cross-file public contract change that cannot be decided from this target."
			}
			var requests atomic.Int32
			var saved model.RepairState
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch requests.Add(1) {
				case 1:
					respond(w, testCall("hold-plan", "update_state", update))
				case 2:
					respond(w, finalItem("needs_human", nil, nil))
				default:
					t.Error("a genuine hold should not trigger another editor request")
					http.Error(w, "unexpected request", http.StatusBadRequest)
				}
			}))
			defer srv.Close()
			in := testInput(srv.URL)
			in.RepairState = &prior
			in.SaveRepairState = func(state model.RepairState) error { saved = state; return nil }
			in.ValidateCandidate = func(context.Context, model.CandidateRequest) (model.CandidateValidation, error) {
				t.Error("blocked candidate reached mechanical validation")
				return model.CandidateValidation{}, errors.New("unexpected validation")
			}
			in.ReviewCandidate = func(context.Context, ReviewInput) (model.IndependentReview, error) {
				t.Error("editor hold reached independent review")
				return model.IndependentReview{}, errors.New("unexpected review")
			}
			out, err := Run(context.Background(), in)
			if err != nil || out.Outcome != "needs_human" || requests.Load() != 2 || saved.Plan.Revision != 2 || saved.Plan.Items[0].Status != "blocked" || saved.ValidationCount != 0 || saved.ReviewCount != 0 || len(saved.Reviews) != 0 {
				t.Fatalf("recorded human blocker was not retained as a terminal hold: out=%+v state=%+v err=%v requests=%d", out, saved, err, requests.Load())
			}
		})
	}
}

func TestCompletionLoopRepeatedPrematureHoldStillHitsExplicitTurnLimit(t *testing.T) {
	prior := initializedRepairState()
	var requests atomic.Int32
	var saved model.RepairState
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		respond(w, reviewResponseItem(map[string]any{"outcome": "needs_human", "candidateId": "", "note": "修正と検証が未完了で、残りの実行時間が足りないと判断しました。"}))
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.MaxTurns = 3
	in.RepairState = &prior
	in.SaveRepairState = func(state model.RepairState) error { saved = state; return nil }
	out, err := Run(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "MaxTurns") || !strings.Contains(err.Error(), "今回 3 / 上限 3") || !strings.Contains(err.Error(), "recorded human-decision blocker") || out.Outcome != "" || requests.Load() != 3 || out.Usage.Turns != 3 {
		t.Fatalf("refusal escaped the configured turn limit: out=%+v err=%v requests=%d", out, err, requests.Load())
	}
	if saved.RequestPending || saved.Plan.Revision != 1 || saved.Plan.Items[0].Status != "proposed" || saved.ValidationCount != 0 || saved.ReviewCount != 0 {
		t.Fatalf("runner-enforced stop lost or changed the repair plan: %+v", saved)
	}
}

func TestCompletionLoopUnlimitedContinuesPastFormerAggregateLimits(t *testing.T) {
	const contextReads = 34
	const candidateAttempts = 4
	prior := initializedRepairState()
	// An unlimited resumed execution must not silently inherit a time default.
	prior.ElapsedMS = 3_600_001
	var requests atomic.Int32
	var saved model.RepairState
	reads, validations, reviews := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := int(requests.Add(1))
		switch {
		case request <= contextReads:
			respond(w, testCall(fmt.Sprintf("context-%d", request), "read_context", map[string]any{"path": fmt.Sprintf("src/context-%d.txt", request), "startLine": 1, "endLine": 1}))
		case request <= contextReads+candidateAttempts:
			candidate := testCandidate()
			candidate.Edits[0].NewText = fmt.Sprintf("Modern.Save(%d)", request-contextReads)
			respond(w, testCall(fmt.Sprintf("candidate-%d", request), "validate_candidate", candidate))
		case request == contextReads+candidateAttempts+1:
			respond(w, candidateFinal("C4"))
		default:
			t.Error("unlimited run requested another response after the complete candidate")
			http.Error(w, "unexpected request", http.StatusBadRequest)
		}
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.MaxTurns, in.Config.MaxAttempts, in.Config.TimeoutSeconds = 0, 0, 0
	in.RepairState = &prior
	in.SaveRepairState = func(state model.RepairState) error { saved = state; return nil }
	in.ReadContext = func(string, int, int) (string, error) {
		reads++
		return strings.Repeat("x", 16<<10), nil // Individually bounded; cumulative reads exceed 512 KiB.
	}
	in.ValidateCandidate = func(_ context.Context, candidate model.CandidateRequest) (model.CandidateValidation, error) {
		validations++
		result := model.CandidateValidation{CandidateID: fmt.Sprintf("C%d", validations), CandidateHash: fmt.Sprintf("hash-%d", validations), PlanRevision: candidate.PlanRevision, Passed: validations == candidateAttempts}
		if !result.Passed {
			result.Diagnostics = []model.CandidateDiagnostic{{Check: "behavior", Message: "Update the complete original-based proposal to satisfy the next behavior check."}}
		}
		return result, nil
	}
	in.ReviewCandidate = func(ctx context.Context, review ReviewInput) (model.IndependentReview, error) {
		reviews++
		if review.Before != in.Content || review.After != "Modern.Save(4)\n" {
			t.Error("review did not inspect the last successful full candidate")
		}
		return passedTestReview(ctx, review)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel() // Test safety only; the application execution has no configured deadline.
	out, err := Run(ctx, in)
	if err != nil || out.Outcome != "modified" || out.CandidateID != "C4" || requests.Load() != 39 || out.Usage.Turns != 40 || reads != contextReads || validations != candidateAttempts || reviews != 1 {
		t.Fatalf("unlimited execution stopped at a former default: out=%+v err=%v requests=%d reads=%d validations=%d reviews=%d", out, err, requests.Load(), reads, validations, reviews)
	}
	if saved.ToolCalls != 38 || saved.ToolCalls <= maxToolCalls || saved.ReadBytes <= maxReadBytes || saved.ValidationCount != 4 || saved.ReviewCount != 1 || saved.RequestPending || saved.ElapsedMS < prior.ElapsedMS {
		t.Fatalf("unlimited execution did not retain accurate aggregate accounting: %+v", saved)
	}
}

func TestCompletionLoopUnlimitedStopsOnUserCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	prior := initializedRepairState()
	var requests atomic.Int32
	var saved model.RepairState
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		if request < 3 {
			respond(w, testCall(fmt.Sprintf("context-%d", request), "read_context", map[string]any{"path": "src/context.txt", "startLine": 1, "endLine": 1}))
			return
		}
		cancel()
		<-release
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.MaxTurns, in.Config.MaxAttempts, in.Config.TimeoutSeconds = 0, 0, 0
	in.RepairState = &prior
	in.SaveRepairState = func(state model.RepairState) error { saved = state; return nil }
	out, err := Run(ctx, in)
	close(release)
	if !errors.Is(err, context.Canceled) || !IsUsageUnknown(err) || out.Outcome != "" || !out.Usage.Uncertain || requests.Load() != 3 || out.Usage.Turns != 3 || saved.Usage.Turns != 3 || !saved.RequestPending || !saved.Usage.Uncertain {
		t.Fatalf("unlimited execution ignored cancellation or lost in-flight accounting: out=%+v state=%+v err=%v requests=%d", out, saved, err, requests.Load())
	}
	if saved.ToolCalls != 2 || saved.ValidationCount != 0 || saved.ReviewCount != 0 || saved.Plan.Revision != 1 {
		t.Fatalf("cancellation did not retain the existing plan and tool accounting: %+v", saved)
	}
}

func TestCompletionLoopUnlimitedTurnsStillHonorsExplicitValidationLimit(t *testing.T) {
	prior := initializedRepairState()
	var requests atomic.Int32
	var saved model.RepairState
	validations := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		request := requests.Add(1)
		respond(w, testCall(fmt.Sprintf("validate-%d", request), "validate_candidate", testCandidate()))
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.MaxTurns, in.Config.MaxAttempts, in.Config.TimeoutSeconds = 0, 2, 0
	in.RepairState = &prior
	in.SaveRepairState = func(state model.RepairState) error { saved = state; return nil }
	in.ValidateCandidate = func(_ context.Context, candidate model.CandidateRequest) (model.CandidateValidation, error) {
		validations++
		return model.CandidateValidation{CandidateID: fmt.Sprintf("C%d", validations), PlanRevision: candidate.PlanRevision, Passed: false, Diagnostics: []model.CandidateDiagnostic{{Message: "Behavior check still fails."}}}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := Run(ctx, in)
	if !errors.Is(err, errValidationLimit) || out.Outcome != "" || requests.Load() != 3 || validations != 2 || saved.ValidationCount != 2 || saved.LastCandidate == nil || saved.LastCandidate.Result.CandidateID != "C2" || saved.RequestPending || saved.ReviewCount != 0 {
		t.Fatalf("explicit validation limit was ignored by unlimited turn loop: out=%+v state=%+v err=%v requests=%d validations=%d", out, saved, err, requests.Load(), validations)
	}
}
