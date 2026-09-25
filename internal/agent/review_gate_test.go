package agent

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func reviewGateFixture() (Input, model.RepairState, model.Proposal) {
	baseHash, candidateHash := strings.Repeat("a", 64), strings.Repeat("b", 64)
	edits := []model.Edit{{OldText: "send();", NewText: "await send();"}}
	plan := model.RepairPlan{Revision: 1,
		RuleDecisions: []model.PlanDecision{{RuleID: "R101", Decision: "modify", Reason: "通知前に送信の完了を待つ"}},
		Items:         []model.PlanItem{{ID: "P1", RuleID: "R101", Location: "send()", Change: "送信を待つ", Expected: "送信後に通知する", Status: "proposed"}},
	}
	review := model.IndependentReview{ID: "review-1", CandidateID: "candidate-1", BaseHash: baseHash, CandidateHash: candidateHash, PlanRevision: 1,
		Verdict: "passed", Summary: "送信後に通知されます。", FinishedAt: "2026-09-15T01:00:00Z",
		Assessments: []model.ReviewAssessment{{RuleID: "R101", Status: "satisfied", Reason: "送信完了を待っています。"}},
	}
	state := model.RepairState{Version: 1, Plan: plan, ReadRuleIDs: []string{"R101"}, Usage: model.Usage{Turns: 6, InputTokens: 900, OutputTokens: 300, CostUSD: .01}, ReviewCount: 1, Reviews: []model.IndependentReview{review},
		LastCandidate: &model.CandidateRecord{
			Request: model.CandidateRequest{PlanRevision: 1, BaseHash: baseHash, Edits: edits, AddressedItemIDs: []string{"P1"}},
			Result:  model.CandidateValidation{CandidateID: "candidate-1", CandidateHash: candidateHash, PlanRevision: 1, Passed: true},
			Review:  &review, ReviewRequested: true, ReviewNote: "送信完了を待つよう修正しました。",
		},
	}
	in := Input{Config: model.Config{MaxAttempts: 3, MaxTurns: 12}, File: "src/dispatch.js", Content: "send();\nnotify();\n", BaseHash: baseHash, CandidateRules: []string{"R101"}, Rules: []model.Rule{{ID: "R101"}}, ReadRule: func(string) (string, error) { return "送信完了後に通知すること。", nil }}
	final := model.Proposal{Outcome: "modified", CandidateID: "candidate-1", Edits: edits, RulesApplied: []string{"R101"}, Note: "送信完了を待つよう修正しました。"}
	return in, state, final
}

func TestIndependentReviewCannotBeBypassedByWithdrawingPlanAndSkipping(t *testing.T) {
	for _, verdict := range []string{"needs_changes", "needs_human", "error", "running"} {
		t.Run(verdict, func(t *testing.T) {
			in, state, _ := reviewGateFixture()
			state.Reviews[0].Verdict = verdict
			state.LastCandidate.Review.Verdict = verdict
			// The editor can legitimately revise its plan, but doing so must not
			// erase the independent review's unresolved finding from the journal.
			plan, err := UpdateRepairPlan(state.Plan, model.PlanUpdate{ExpectedRevision: 1, RuleDecisions: []model.PlanDecision{{RuleID: "R101", Decision: "no_change", Reason: "調べ直した結果、変更は不要と考えました。"}}}, in.Rules, in.CandidateRules, state.ReadRuleIDs)
			if err != nil {
				t.Fatal(err)
			}
			state.Plan = plan
			for _, clearCandidate := range []bool{false, true} {
				if clearCandidate {
					state.LastCandidate = nil
				}
				if _, err := parseCandidateFinal(`{"outcome":"skipped","candidateId":"","note":"修正不要です。"}`, state, in, in.CandidateRules); err == nil {
					t.Fatalf("unresolved %s review bypassed with skipped (clearCandidate=%v)", verdict, clearCandidate)
				}
				if _, err := parseCandidateFinal(`{"outcome":"needs_human","candidateId":"","note":"レビューの指摘を判断できません。"}`, state, in, in.CandidateRules); err == nil {
					t.Fatal("editor ended a planned repair without recording an actual blocker")
				}
				heldState := cloneRepairState(state)
				heldState.Plan.RuleDecisions[0].Decision = "blocked"
				heldState.Plan.RuleDecisions[0].Reason = "独立レビューの指摘への対応には、呼び出し元の契約確認が必要です。"
				if out, err := parseCandidateFinal(`{"outcome":"needs_human","candidateId":"","note":"呼び出し元の契約を確認してください。"}`, heldState, in, in.CandidateRules); err != nil || out.Outcome != "needs_human" {
					t.Fatalf("safe hold was rejected: %+v %v", out, err)
				}
			}
		})
	}
}

func TestIndependentReviewDoesNotPreventOrdinaryNoChangeDecision(t *testing.T) {
	in, state, _ := reviewGateFixture()
	state.Plan = model.RepairPlan{Revision: 1, RuleDecisions: []model.PlanDecision{{RuleID: "R101", Decision: "no_change", Reason: "送信の完了を既に待っています。"}}}
	state.Reviews, state.LastCandidate = nil, nil
	out, err := parseCandidateFinal(`{"outcome":"skipped","candidateId":"","note":"修正不要です。"}`, state, in, in.CandidateRules)
	if err != nil || out.Outcome != "skipped" {
		t.Fatalf("ordinary no-change result rejected: %+v %v", out, err)
	}
}

func TestIndependentReviewDoesNotOverrideEditorHoldOrSkip(t *testing.T) {
	for _, outcome := range []string{"needs_human", "skipped"} {
		t.Run(outcome, func(t *testing.T) {
			in, state, _ := reviewGateFixture()
			before := cloneRepairState(state)
			in.ReviewCandidate = func(context.Context, ReviewInput) (model.IndependentReview, error) {
				t.Fatal("editor hold or skip triggered independent review")
				return model.IndependentReview{}, nil
			}
			noWrite := func() error { t.Fatal("editor hold or skip changed the checkpoint"); return nil }
			final := model.Proposal{Outcome: outcome, Note: "呼び出し元の契約を確認してください。"}
			out, accepted, err := reviewFinal(context.Background(), in, &state, final, noWrite, noWrite)
			if err != nil || !accepted || !reflect.DeepEqual(out, final) || !reflect.DeepEqual(state, before) {
				t.Fatalf("editor %s was overridden: %+v accepted=%v err=%v", outcome, out, accepted, err)
			}
		})
	}
}

func TestIndependentReviewRejectsStaleApprovalIdentity(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*model.IndependentReview)
	}{
		{"review ID", func(r *model.IndependentReview) { r.ID = "" }},
		{"candidate ID", func(r *model.IndependentReview) { r.CandidateID = "older-candidate" }},
		{"base hash", func(r *model.IndependentReview) { r.BaseHash = strings.Repeat("c", 64) }},
		{"candidate hash", func(r *model.IndependentReview) { r.CandidateHash = strings.Repeat("d", 64) }},
		{"plan revision", func(r *model.IndependentReview) { r.PlanRevision++ }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, state, final := reviewGateFixture()
			tc.change(state.LastCandidate.Review)
			in.ReviewCandidate = func(context.Context, ReviewInput) (model.IndependentReview, error) {
				t.Fatal("stale approval must be rejected before another request")
				return model.IndependentReview{}, nil
			}
			noWrite := func() error { t.Fatal("stale approval changed the checkpoint"); return nil }
			if _, accepted, err := reviewFinal(context.Background(), in, &state, final, noWrite, noWrite); err == nil || accepted {
				t.Fatalf("stale %s approval accepted: accepted=%v error=%v", tc.name, accepted, err)
			}
		})
	}
}

func TestIndependentReviewCandidateMustBelongToCurrentPlanAndSource(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Input, *model.RepairState)
	}{
		{"source changed", func(in *Input, _ *model.RepairState) { in.BaseHash = strings.Repeat("f", 64) }},
		{"plan changed", func(_ *Input, s *model.RepairState) { s.Plan.Revision++ }},
		{"validation revision changed", func(_ *Input, s *model.RepairState) { s.LastCandidate.Result.PlanRevision++ }},
		{"request revision changed", func(_ *Input, s *model.RepairState) { s.LastCandidate.Request.PlanRevision++ }},
		{"mechanical validation failed", func(_ *Input, s *model.RepairState) { s.LastCandidate.Result.Passed = false }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in, state, _ := reviewGateFixture()
			tc.change(&in, &state)
			if _, err := parseCandidateFinal(`{"outcome":"modified","candidateId":"candidate-1","note":"修正済みです。"}`, state, in, in.CandidateRules); err == nil {
				t.Fatalf("stale candidate with %s accepted", tc.name)
			}
		})
	}
}

func TestIndependentReviewCachedVerdictNeverRerequestsOrResetsUsage(t *testing.T) {
	for _, verdict := range []string{"passed", "needs_changes", "needs_human"} {
		t.Run(verdict, func(t *testing.T) {
			in, state, final := reviewGateFixture()
			state.LastCandidate.Review.Verdict = verdict
			state.Reviews[0].Verdict = verdict
			before := cloneRepairState(state)
			in.ReviewCandidate = func(context.Context, ReviewInput) (model.IndependentReview, error) {
				t.Fatal("cached candidate was reviewed a second time")
				return model.IndependentReview{}, nil
			}
			noWrite := func() error { t.Fatal("reading a cached review wrote a checkpoint"); return nil }
			out, accepted, err := reviewFinal(context.Background(), in, &state, final, noWrite, noWrite)
			if err != nil || accepted != (verdict != "needs_changes") {
				t.Fatalf("cached verdict %s: accepted=%v out=%+v err=%v", verdict, accepted, out, err)
			}
			if verdict == "needs_human" && (out.Outcome != "needs_human" || len(out.Edits) != 0 || out.CandidateID != "") {
				t.Fatalf("review hold retained an adoptable change: %+v", out)
			}
			if !reflect.DeepEqual(state, before) {
				t.Fatal("cached verdict mutated the repair state or usage counters")
			}
		})
	}
}
