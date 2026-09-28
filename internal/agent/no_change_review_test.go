package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func noChangeFixture() (Input, model.RepairState, model.Proposal) {
	in := testInput("http://127.0.0.1:1")
	in.BaseHash = testCandidate().BaseHash
	state := model.RepairState{Version: 1, ReadRuleIDs: []string{"R019"}, Plan: model.RepairPlan{Revision: 1, RuleDecisions: []model.PlanDecision{{RuleID: "R019", Decision: "no_change", Reason: "既に条件を満たしています。"}}, Items: []model.PlanItem{}}}
	return in, state, model.Proposal{Outcome: "skipped", Note: "変更不要です。"}
}

func TestNoChangeReviewAssessesOriginalAndAccountsUsage(t *testing.T) {
	in, state, final := noChangeFixture()
	calls := 0
	in.ReviewCandidate = func(ctx context.Context, review ReviewInput) (model.IndependentReview, error) {
		calls++
		if review.Before != in.Content || review.After != in.Content || review.BaseHash != in.BaseHash || review.CandidateHash != in.BaseHash || len(review.Rules) != 1 || review.Rules[0].ID != "R019" {
			t.Fatalf("no-change review did not receive the unchanged source and complete rule scope: %+v", review)
		}
		return passedTestReview(ctx, review)
	}
	writes := 0
	checkpoint := func() error { writes++; return nil }
	out, accepted, err := reviewFinal(context.Background(), in, &state, final, checkpoint, checkpoint)
	if err != nil || !accepted || out.Outcome != "skipped" || out.CandidateID == "" || calls != 1 || writes == 0 || state.ReviewCount != 1 || state.Usage.Turns != 1 || state.ValidationCount != 0 || state.RequestPending || !state.LastCandidate.NoChange || len(state.LastCandidate.Request.Edits) != 0 {
		t.Fatalf("unchanged review was not durably accounted: out=%+v state=%+v accepted=%v err=%v", out, state, accepted, err)
	}
	// A saved review has the same identity and never silently spends again.
	second, accepted, err := reviewFinal(context.Background(), in, &state, final, checkpoint, checkpoint)
	if err != nil || !accepted || second.CandidateID != out.CandidateID || calls != 1 || state.Usage.Turns != 1 {
		t.Fatalf("cached unchanged review was not reused: %+v %v", second, err)
	}
}

func TestNoChangeReviewResumesWithoutEditorRequest(t *testing.T) {
	in, state, final := noChangeFixture()
	checkpoint := func() error { return nil }
	if _, _, err := reviewFinal(context.Background(), in, &state, final, checkpoint, checkpoint); err != nil {
		t.Fatal(err)
	}
	state.LastCandidate.Review = nil
	state.Reviews = nil
	state.LastCandidate.ReviewRequested = true
	priorTurns := state.Usage.Turns
	reviews := 0
	in.ReviewCandidate = func(ctx context.Context, review ReviewInput) (model.IndependentReview, error) {
		reviews++
		return passedTestReview(ctx, review)
	}
	in.RepairState = &state
	var saved model.RepairState
	in.SaveRepairState = func(s model.RepairState) error { saved = cloneRepairState(s); return nil }
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "skipped" || reviews != 1 || saved.Usage.Turns != priorTurns+1 || out.Usage.Turns != 1 || saved.RequestPending {
		t.Fatalf("no-change review failed to resume independently: %+v %+v %v", out, saved, err)
	}
	// A second restart reuses the finished review and consumes no LLM turns.
	in.RepairState = &saved
	out, err = Run(context.Background(), in)
	if err != nil || out.Outcome != "skipped" || out.Usage.Turns != 0 || reviews != 1 {
		t.Fatalf("completed unchanged review was replayed: %+v %v", out, err)
	}
}

func TestNoChangeReviewCannotBePresentedAsModified(t *testing.T) {
	in, state, final := noChangeFixture()
	checkpoint := func() error { return nil }
	out, _, err := reviewFinal(context.Background(), in, &state, final, checkpoint, checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	answer := string(raw(map[string]string{"outcome": "modified", "candidateId": out.CandidateID, "note": "変更しました。"}))
	if _, err := parseCandidateFinal(answer, state, in, in.CandidateRules); err == nil {
		t.Fatal("unchanged review identity authorized an edit")
	}
}

func TestNoChangeReviewHoldRemainsNeedsHumanWithoutAdoptableCandidate(t *testing.T) {
	in, state, final := noChangeFixture()
	in.ReviewCandidate = func(ctx context.Context, review ReviewInput) (model.IndependentReview, error) {
		result, err := passedTestReview(ctx, review)
		result.Verdict, result.Summary = "needs_human", "呼び出し元の仕様が必要です。"
		result.Assessments[0].Status = "needs_human"
		return result, err
	}
	checkpoint := func() error { return nil }
	out, accepted, err := reviewFinal(context.Background(), in, &state, final, checkpoint, checkpoint)
	if err != nil || !accepted || out.Outcome != "needs_human" || out.CandidateID != "" || len(out.Edits) != 0 || state.LastCandidate.Review.Verdict != "needs_human" {
		t.Fatalf("review hold was converted to an adoptable outcome: %+v accepted=%v err=%v", out, accepted, err)
	}
}

func TestNoChangeReviewRejectionContinuesTheSameEditorLoop(t *testing.T) {
	turn, reviews := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn++
		var envelope map[string]any
		_ = json.NewDecoder(r.Body).Decode(&envelope)
		switch turn {
		case 1:
			respond(w, testCall("read", "read_rule", map[string]string{"id": "R019"}))
		case 2:
			respond(w, testCall("unchanged-plan", "update_state", model.PlanUpdate{RuleDecisions: []model.PlanDecision{{RuleID: "R019", Decision: "no_change", Reason: "変更不要と判断。"}}, Items: []model.PlanItem{}}))
		case 3:
			respond(w, finalItem("skipped", nil, nil))
		case 4:
			if reviews != 1 || !strings.Contains(string(raw(envelope)), "NO_CHANGE_MISSED_CALL") {
				t.Error("unchanged-source review rejection did not return to the editor conversation")
			}
			respond(w, testCall("repair-plan", "update_state", testPlan(1)))
		case 5:
			candidate := testCandidate()
			candidate.PlanRevision = 2
			respond(w, testCall("candidate", "validate_candidate", candidate))
		case 6:
			respond(w, goodItem())
		default:
			t.Errorf("unexpected editor request %d", turn)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.ReviewCandidate = func(ctx context.Context, review ReviewInput) (model.IndependentReview, error) {
		reviews++
		result, err := passedTestReview(ctx, review)
		if reviews == 1 {
			if review.Before != review.After {
				t.Error("first review was not an unchanged-source review")
			}
			result.Verdict, result.Summary = "needs_changes", "NO_CHANGE_MISSED_CALL"
			result.Assessments[0].Status = "violated"
			result.Issues = []model.ReviewIssue{{RuleID: "R019", Location: "Save", LineBasis: "before", Excerpt: "Legacy.Save()", Reason: "NO_CHANGE_MISSED_CALL", RequestedChange: "Use Modern.Save()"}}
		}
		return result, err
	}
	var saved model.RepairState
	in.SaveRepairState = func(s model.RepairState) error { saved = cloneRepairState(s); return nil }
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || turn != 6 || reviews != 2 || saved.Usage.Turns != 8 || saved.ValidationCount != 1 || saved.LastCandidate.NoChange || len(saved.Reviews) != 2 {
		t.Fatalf("unchanged review did not repair within the same attempt: %+v %+v err=%v", out, saved, err)
	}
}

func TestReadRuleCannotEscapeTheFileRuleScope(t *testing.T) {
	in := testInput("http://127.0.0.1:1")
	in.ReadRule = func(string) (string, error) { t.Fatal("out-of-scope reader was invoked"); return "", nil }
	for _, call := range []functionCall{{Name: "read_rule", Arguments: `{"id":"R999"}`}, {Name: "read_rules", Arguments: `{"ids":["R999"]}`}} {
		if _, err := executeTool(in, call, map[string]string{}); err == nil {
			t.Fatalf("out-of-scope rule was readable: %+v", call)
		}
	}
}
