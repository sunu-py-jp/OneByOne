package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func partialFixture() (Input, model.RepairState, model.Proposal) {
	in, state, final := reviewGateFixture()
	in.Content = "send();\nnotify();\nambiguous();\n"
	state.Plan.Items = append(state.Plan.Items, model.PlanItem{ID: "H1", RuleID: "R101", Location: "ambiguous", Change: "判断を待つ", Expected: "契約を確認する", Status: "blocked", HoldReason: "呼び出し元の契約が不明です。", SourceLocations: []model.SourceLocation{{StartLine: 3, EndLine: 3, Excerpt: "ambiguous();"}}})
	state.LastCandidate.Review = nil
	state.Reviews = nil
	return in, state, final
}
func partialApproval(in ReviewInput) model.IndependentReview {
	return model.IndependentReview{BaseHash: in.BaseHash, CandidateHash: in.CandidateHash, Verdict: "passed_with_holds", Summary: "独立した送信修正は安全で、契約不明箇所は保持しています。", Assessments: []model.ReviewAssessment{{RuleID: "R101", Status: "needs_human", Reason: "送信修正は完了。ambiguousの契約確認は保留です。"}}, HoldAssessments: []model.ReviewHoldAssessment{{ItemID: "H1", Status: "preserved", Reason: "保留行は不変で、送信の完了待ちはこの契約に依存しません。"}}}
}

func TestPartialCandidateRequiresAllSafeItemsAndKeepsHeldItemsSeparate(t *testing.T) {
	in, state, _ := partialFixture()
	req := state.LastCandidate.Request
	if err := CheckCandidatePlan(state.Plan, req); err != nil {
		t.Fatal(err)
	}
	if err := CheckCandidateHolds(state.Plan, req, in.Content); err != nil {
		t.Fatal(err)
	}
	req.AddressedItemIDs = append(req.AddressedItemIDs, "H1")
	if err := CheckCandidatePlan(state.Plan, req); err == nil {
		t.Fatal("blocked item accepted as addressed")
	}
	req = state.LastCandidate.Request
	req.AddressedItemIDs = nil
	if err := CheckCandidatePlan(state.Plan, req); err == nil {
		t.Fatal("unaddressed safe work omitted")
	}
	state.Plan.RuleDecisions = append(state.Plan.RuleDecisions, model.PlanDecision{RuleID: "R2", Decision: "blocked", Reason: "全体の契約不明"})
	if _, err := CandidateHolds(state.Plan); err == nil {
		t.Fatal("whole-file blocker allowed partial approval")
	}
}

func TestPartialHeldLinesRemainUnchangedAcrossBroadContext(t *testing.T) {
	in, state, _ := partialFixture()
	cases := []struct {
		name, old, new string
		allow          bool
	}{
		{"separate safe edit", "send();", "await send();", true},
		{"broad unchanged held context", in.Content, "await send();\nnotify();\nambiguous();\n", true},
		{"changes on both sides", "send();\nnotify();\nambiguous();\n", "await send();\nnotify();\nambiguous();\nreport();\n", true},
		{"changes held call", "ambiguous();", "guess();", false},
		{"partial held line", "ambiguous", "guess", false},
		{"prefix held line", "ambiguous();", "await ambiguous();", false},
		{"removes preceding newline", "notify();\n", "notify();", false},
		{"adds full line before hold", "notify();\n", "notify();\nreport();\n", true},
		{"duplicates held scope", "ambiguous();\n", "ambiguous();\nambiguous();\n", false},
		{"deletes held scope", "ambiguous();\n", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := state.LastCandidate.Request
			req.Edits = []model.Edit{{OldText: tc.old, NewText: tc.new, ItemIDs: []string{"P1"}}}
			err := CheckCandidateHolds(state.Plan, req, in.Content)
			if (err == nil) != tc.allow {
				t.Fatalf("allowed=%v error=%v", tc.allow, err)
			}
		})
	}
	// A final line without a newline must not acquire appended statement text.
	in.Content = strings.TrimSuffix(in.Content, "\n")
	req := state.LastCandidate.Request
	req.Edits = []model.Edit{{OldText: "ambiguous();", NewText: "ambiguous(); report();", ItemIDs: []string{"P1"}}}
	if err := CheckCandidateHolds(state.Plan, req, in.Content); err == nil {
		t.Fatal("held EOF acquired code")
	}
}

func TestPartialFinalCannotAbandonSafeWorkOrClaimHeldOnlyRuleApplied(t *testing.T) {
	in, state, _ := partialFixture()
	if _, err := parseCandidateFinal(`{"outcome":"needs_human","candidateId":"","note":"契約不明です。"}`, state, in, in.CandidateRules); err == nil {
		t.Fatal("mixed plan abandoned safe changes")
	}
	if _, err := parseCandidateFinal(`{"outcome":"modified","candidateId":"candidate-1","note":"安全な修正を行いました。"}`, state, in, in.CandidateRules); err != nil {
		t.Fatal(err)
	}
	state.Plan.Items[1].RuleID = "R2"
	state.Plan.RuleDecisions = append(state.Plan.RuleDecisions, model.PlanDecision{RuleID: "R2", Decision: "modify", Reason: "保留箇所の確認"})
	out, err := parseCandidateFinal(`{"outcome":"modified","candidateId":"candidate-1","note":"安全な修正を行いました。"}`, state, in, in.CandidateRules)
	if err != nil || len(out.RulesApplied) != 1 || out.RulesApplied[0] != "R101" {
		t.Fatalf("held rule marked applied: %+v %v", out, err)
	}
	state.Plan.Items[0].Status = "blocked"
	state.Plan.Items[0].HoldReason = "送信も契約不明"
	state.Plan.Items[0].SourceLocations = []model.SourceLocation{{StartLine: 1, EndLine: 1, Excerpt: "send();"}}
	if _, err := parseCandidateFinal(`{"outcome":"needs_human","candidateId":"","note":"どの修正も契約に依存します。"}`, state, in, in.CandidateRules); err != nil {
		t.Fatalf("fully held file cannot stop: %v", err)
	}
}

func TestPartialReviewFreshContextAndExplicitApproval(t *testing.T) {
	in, state, final := partialFixture()
	calls := 0
	in.ReviewCandidate = func(ctx context.Context, r ReviewInput) (model.IndependentReview, error) {
		calls++
		if len(r.Holds) != 1 || r.Holds[0].ItemID != "H1" || r.Holds[0].RuleID != "R101" || r.Holds[0].SourceLocations[0].StartLine != 3 {
			t.Fatalf("missing held scope: %+v", r.Holds)
		}
		data, _ := json.Marshal(r.Holds)
		if strings.Contains(string(data), "送信を待つ") || strings.Contains(string(data), "expected") {
			t.Fatal("editor rationale leaked")
		}
		if err := r.BeforeRequest("partial-review"); err != nil {
			return model.IndependentReview{}, err
		}
		if err := r.AfterRequest(model.Usage{Turns: 1}, nil); err != nil {
			return model.IndependentReview{}, err
		}
		return partialApproval(r), nil
	}
	out, accepted, err := reviewFinal(context.Background(), in, &state, final, func() error { return nil }, func() error { return nil })
	if err != nil || !accepted || out.Outcome != "modified" || calls != 1 || state.LastCandidate.Review.Verdict != "passed_with_holds" {
		t.Fatalf("safe subset not reviewed: %+v %v %v", out, accepted, err)
	}
	if err := CheckPartialReview(*state.LastCandidate.Review, state.Plan); err != nil {
		t.Fatal(err)
	}
	// Saved approval can resume without another paid review.
	if _, accepted, err := reviewFinal(context.Background(), in, &state, final, func() error { t.Fatal("unexpected write"); return nil }, func() error { return nil }); err != nil || !accepted || calls != 1 {
		t.Fatalf("cached partial approval rejected %v", err)
	}
}

func TestPartialApprovalRejectsClearedUnsafeOrUnscopedHolds(t *testing.T) {
	in, state, _ := partialFixture()
	holds, _ := CandidateHolds(state.Plan)
	reviewIn := ReviewInput{BaseHash: in.BaseHash, CandidateHash: state.LastCandidate.Result.CandidateHash, Holds: holds, Rules: []ReviewRule{{ID: "R101"}}, Before: in.Content, After: strings.Replace(in.Content, "send();", "await send();", 1)}
	base := partialApproval(reviewIn)
	cases := []struct {
		name   string
		change func(*model.IndependentReview)
	}{
		{"ordinary pass", func(r *model.IndependentReview) { r.Verdict = "passed" }},
		{"no held assessment", func(r *model.IndependentReview) { r.HoldAssessments = nil }},
		{"unknown hold", func(r *model.IndependentReview) { r.HoldAssessments[0].ItemID = "OTHER" }},
		{"unsafe dependency", func(r *model.IndependentReview) { r.HoldAssessments[0].Status = "unsafe" }},
		{"cleared held rule", func(r *model.IndependentReview) { r.Assessments[0].Status = "satisfied" }},
		{"violation", func(r *model.IndependentReview) { r.Assessments[0].Status = "violated" }},
		{"unregistered issue", func(r *model.IndependentReview) {
			r.Issues = []model.ReviewIssue{{Kind: "needs_human", RuleID: "R101", LineBasis: "before", StartLine: 1, EndLine: 1, Excerpt: "send();", Location: "send", Reason: "不明", RequestedChange: "確認"}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := base
			r.Assessments = append([]model.ReviewAssessment(nil), base.Assessments...)
			r.HoldAssessments = append([]model.ReviewHoldAssessment(nil), base.HoldAssessments...)
			tc.change(&r)
			if err := CheckPartialReview(r, state.Plan); err == nil {
				t.Fatal("unsafe partial approval accepted")
			}
		})
	}
	wire := map[string]any{"baseHash": base.BaseHash, "candidateHash": base.CandidateHash, "verdict": base.Verdict, "summary": base.Summary, "assessments": base.Assessments, "issues": []model.ReviewIssue{}, "holdAssessments": base.HoldAssessments}
	if _, err := parseIndependentReview(string(raw(wire)), reviewIn); err != nil {
		t.Fatalf("valid partial review rejected: %v", err)
	}
	wire["verdict"] = "passed"
	if _, err := parseIndependentReview(string(raw(wire)), reviewIn); err == nil {
		t.Fatal("normal pass cleared held scope")
	}
}

func TestReviewRepairableProblemsCoexistWithHumanHolds(t *testing.T) {
	in := reviewTestInput("http://127.0.0.1:1")
	wire := reviewWire(in, "needs_changes")
	wire["assessments"].([]any)[0].(map[string]any)["status"] = "needs_human"
	if r, err := parseIndependentReview(string(raw(wire)), in); err != nil || r.Verdict != "needs_changes" {
		t.Fatalf("repair loop was blocked by human assessment: %+v %v", r, err)
	}
	// Reviewer-discovered local uncertainty returns to the editor for a new plan.
	editor, state, final := partialFixture()
	state.Plan.Items = state.Plan.Items[:1]
	r := partialApproval(ReviewInput{BaseHash: editor.BaseHash, CandidateHash: state.LastCandidate.Result.CandidateHash})
	r.Verdict = "needs_human"
	r.ID = "review"
	r.CandidateID = final.CandidateID
	r.PlanRevision = 1
	r.HoldAssessments = nil
	r.Issues = []model.ReviewIssue{{Kind: "needs_human", RuleID: "R101", LineBasis: "before", StartLine: 3, EndLine: 3, Excerpt: "ambiguous();", Location: "ambiguous", Reason: "契約不明", RequestedChange: "保留する"}}
	state.LastCandidate.Review = &r
	if _, accepted, err := reviewedOutcome(final, state.LastCandidate, state.Plan, editor.Content); err != nil || accepted {
		t.Fatalf("localized hold discarded safe work: accepted=%v err=%v", accepted, err)
	}
	r.Issues = nil
	if out, accepted, err := reviewedOutcome(final, state.LastCandidate, state.Plan, editor.Content); err != nil || !accepted || out.Outcome != "needs_human" {
		t.Fatal("unlocalizable uncertainty did not stop")
	}
	state.LastCandidate.NoChange = true
	r.Issues = []model.ReviewIssue{{Kind: "needs_changes", RuleID: "R101"}}
	if _, accepted, err := reviewedOutcome(final, state.LastCandidate, state.Plan, editor.Content); err != nil || accepted {
		t.Fatal("repairable no-change review discarded known fixes")
	}
}

func TestPartialAdoptionProviderLoopAndReviewerDiscoveredHold(t *testing.T) {
	for _, discovered := range []bool{false, true} {
		t.Run(fmt.Sprintf("reviewer_discovered_%v", discovered), func(t *testing.T) {
			content := "Legacy.Save()\nambiguous();\n"
			hash := func(s string) string { x := sha256.Sum256([]byte(s)); return hex.EncodeToString(x[:]) }
			plan := testPlan(0)
			hold := model.PlanItem{ID: "H1", RuleID: "R019", Location: "ambiguous", Change: "契約を確認する", Expected: "実装を確定できる", HoldReason: "呼び出し元の契約が必要", Status: "blocked", SourceLocations: []model.SourceLocation{{StartLine: 2, EndLine: 2, Excerpt: "ambiguous();"}}}
			if !discovered {
				plan.Items = append(plan.Items, hold)
			}
			candidate := testCandidate()
			candidate.BaseHash = hash(content)
			editors, reviews, validations := 0, 0, 0
			var saved model.RepairState
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var request map[string]any
				if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
					t.Error(err)
					return
				}
				history := request["input"].([]any)
				if request["instructions"] == reviewInstructions {
					reviews++
					if len(history) != 1 {
						t.Error("review inherited editor history")
					}
					var payload map[string]json.RawMessage
					if err := json.Unmarshal([]byte(history[0].(map[string]any)["content"].(string)), &payload); err != nil {
						t.Error(err)
					}
					for _, key := range []string{"plan", "history", "previousReview", "ruleDecisions"} {
						if payload[key] != nil {
							t.Errorf("editor data leaked: %s", key)
						}
					}
					verdict, issues, assessments := "passed_with_holds", []model.ReviewIssue{}, []model.ReviewHoldAssessment{{ItemID: "H1", Status: "preserved", Reason: "保留行は不変で、新保存APIは保留箇所の契約に依存しません。"}}
					if discovered && reviews == 1 {
						if payload["holds"] != nil {
							t.Error("first reviewer should discover hold independently")
						}
						verdict = "needs_human"
						assessments = nil
						issues = []model.ReviewIssue{{Kind: "needs_human", RuleID: "R019", Location: "ambiguous", LineBasis: "before", StartLine: 2, EndLine: 2, Excerpt: "ambiguous();", Reason: "呼び出し元の契約が必要", RequestedChange: "この行を保留し、独立した保存修正を残す"}}
					} else {
						var holds []model.ReviewHold
						if json.Unmarshal(payload["holds"], &holds) != nil || len(holds) != 1 || holds[0].ItemID != "H1" {
							t.Error("partial reviewer did not receive precise hold")
						}
					}
					wire := map[string]any{"baseHash": hash(content), "candidateHash": hash(strings.Replace(content, "Legacy.Save()", "Modern.Save()", 1)), "verdict": verdict, "summary": "保存修正は安全で、契約不明の行は要確認です。", "assessments": []model.ReviewAssessment{{RuleID: "R019", Status: "needs_human", Reason: "保存変更は完了、ambiguousの契約を確認してください。"}}, "issues": issues, "holdAssessments": assessments}
					if len(assessments) == 0 {
						wire["holdAssessments"] = []model.ReviewHoldAssessment{}
					}
					respond(w, reviewResponseItem(wire))
					return
				}
				switch editors {
				case 0:
					respond(w, standardTurn(0))
				case 1:
					respond(w, testCall("plan-1", "update_state", plan))
				case 2:
					respond(w, testCall("candidate-1", "validate_candidate", candidate))
				case 3:
					respond(w, candidateFinal("C1"))
				case 4:
					if !discovered {
						t.Error("unexpected replan")
					}
					if !strings.Contains(fmt.Sprint(history), "Preserve localized human holds") {
						t.Error("review feedback omitted partial replanning")
					}
					plan.ExpectedRevision = 1
					plan.Items = append(plan.Items, hold)
					respond(w, testCall("plan-2", "update_state", plan))
				case 5:
					candidate.PlanRevision = 2
					respond(w, testCall("candidate-2", "validate_candidate", candidate))
				case 6:
					respond(w, candidateFinal("C2"))
				default:
					t.Errorf("unexpected editor turn %d", editors)
					respond(w, finalItem("needs_human", nil, nil))
				}
				editors++
			}))
			defer srv.Close()
			in := testInput(srv.URL)
			in.Content = content
			in.ReviewCandidate = nil
			in.SaveRepairState = func(s model.RepairState) error { saved = cloneRepairState(s); return nil }
			in.ValidateCandidate = func(_ context.Context, req model.CandidateRequest) (model.CandidateValidation, error) {
				validations++
				return model.CandidateValidation{AttributionVersion: model.LineAttributionVersion, CandidateID: fmt.Sprintf("C%d", validations), CandidateHash: hash(strings.Replace(content, "Legacy.Save()", "Modern.Save()", 1)), PlanRevision: req.PlanRevision, Passed: true}, nil
			}
			out, err := Run(context.Background(), in)
			wantReviews, wantEditors := 1, 4
			if discovered {
				wantReviews, wantEditors = 2, 7
			}
			if err != nil || out.Outcome != "modified" || reviews != wantReviews || editors != wantEditors || len(out.Edits) != 1 || saved.LastCandidate.Review.Verdict != "passed_with_holds" || saved.Plan.Items[1].Status != "blocked" {
				t.Fatalf("partial loop failed: out=%+v err=%v editors=%d reviews=%d saved=%+v", out, err, editors, reviews, saved)
			}
			if saved.RequestPending || saved.Usage.Turns != reviews+editors || len(saved.Reviews) != reviews {
				t.Fatalf("partial loop accounting mismatch: %+v", saved)
			}
		})
	}
}
