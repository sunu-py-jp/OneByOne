package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"onebyone/internal/model"
	"strings"
	"testing"
)

func heldLocationPlan() model.PlanUpdate {
	update := testPlan(0)
	update.Items[0].Status = "blocked"
	update.Items[0].HoldReason = "呼び出し元の契約を確認する必要があります。"
	update.Items[0].SourceLocations = []model.SourceLocation{{StartLine: 1, EndLine: 1, Excerpt: "Legacy.Save()"}}
	return update
}

func TestPlanLocationErrorsDoNotPersistAndRequireExactCode(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing-array", func(item map[string]any) { delete(item, "sourceLocations") }},
		{"null-array", func(item map[string]any) { item["sourceLocations"] = nil }},
		{"blocked-without-location", func(item map[string]any) { item["sourceLocations"] = []any{} }},
		{"ambiguous-line", func(item map[string]any) {
			item["sourceLocations"].([]any)[0].(map[string]any)["startLine"] = 2
			item["sourceLocations"].([]any)[0].(map[string]any)["endLine"] = 2
		}},
		{"fabricated-code", func(item map[string]any) {
			item["sourceLocations"].([]any)[0].(map[string]any)["excerpt"] = "invented()"
		}},
		{"missing-range", func(item map[string]any) { delete(item["sourceLocations"].([]any)[0].(map[string]any), "endLine") }},
		{"null-range", func(item map[string]any) { item["sourceLocations"].([]any)[0].(map[string]any)["endLine"] = nil }},
		{"nonblocked-fabrication", func(item map[string]any) {
			item["status"] = "proposed"
			item["holdReason"] = ""
			item["sourceLocations"].([]any)[0].(map[string]any)["excerpt"] = "invented()"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := model.RepairState{Version: 1}
			in := testInput("https://example.invalid")
			if test.name == "ambiguous-line" {
				in.Content = "Legacy.Save()\nother()\nLegacy.Save()\n"
			}
			wire := testPlanWire(heldLocationPlan())
			test.mutate(wire["items"].([]any)[0].(map[string]any))
			saves := 0
			save := func() error { saves++; return nil }
			_, err := executeRepairTool(context.Background(), in, functionCall{Name: "update_state", Arguments: string(raw(wire))}, &state, []string{"R019"}, map[string]string{"R019": "rule"}, in.Config, save, save)
			if err == nil || state.Plan.Revision != 0 || saves != 0 {
				t.Fatalf("bad location persisted: %+v %v saves=%d", state, err, saves)
			}
		})
	}
}

func TestHeldLocationCanBeCorrectedWithinEditorLoop(t *testing.T) {
	state := model.RepairState{Version: 1, ReadRuleIDs: []string{"R019"}}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		switch calls {
		case 0:
			update := heldLocationPlan()
			update.Items[0].SourceLocations[0].Excerpt = "invented()"
			respond(w, testCall("bad-location", "update_state", update))
		case 1:
			if state.Plan.Revision != 0 || !strings.Contains(fmt.Sprint(history), "exact full-line excerpt matches") {
				t.Errorf("invalid location did not return a repairable error: %+v %v", state, history)
			}
			respond(w, testCall("fixed-location", "update_state", heldLocationPlan()))
		case 2:
			respond(w, finalItem("needs_human", nil, nil))
		default:
			t.Error("unexpected extra request")
			w.WriteHeader(http.StatusBadRequest)
		}
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &state
	in.SaveRepairState = func(saved model.RepairState) error { state = saved; return nil }
	in.ValidateCandidate = func(context.Context, model.CandidateRequest) (model.CandidateValidation, error) {
		t.Fatal("held location entered candidate validation")
		return model.CandidateValidation{}, nil
	}
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "needs_human" || calls != 3 || state.Plan.Revision != 1 || len(state.Plan.Items[0].SourceLocations) != 1 || state.ValidationCount != 0 || state.ReviewCount != 0 {
		t.Fatalf("hold did not correct and finish: %+v %v state=%+v", out, err, state)
	}
}

func TestNeedsHumanCannotBypassLegacyOrForgedBlockedLocations(t *testing.T) {
	in := testInput("https://example.invalid")
	for _, locations := range [][]model.SourceLocation{nil, {{StartLine: 1, EndLine: 1, Excerpt: "invented()"}}} {
		state := initializedRepairState()
		state.Plan.Items[0].Status = "blocked"
		state.Plan.Items[0].HoldReason = "契約を確認"
		state.Plan.Items[0].SourceLocations = locations
		if _, err := parseCandidateFinal(`{"outcome":"needs_human","candidateId":"","note":"契約を確認してください"}`, state, in, []string{"R019"}); err == nil {
			t.Fatal("unverified blocked location accepted by final answer")
		}
	}
	// Whole-file uncertainty has no invented code location, and an inability to
	// read enough context still permits the established pre-plan escape.
	for _, state := range []model.RepairState{{Version: 1}, {Version: 1, Plan: model.RepairPlan{Revision: 1, RuleDecisions: []model.PlanDecision{{RuleID: "R019", Decision: "blocked", Reason: "ルール本文を判断できる資料が必要"}}}}} {
		if out, err := parseCandidateFinal(`{"outcome":"needs_human","candidateId":"","note":"判断資料が必要です"}`, state, in, []string{"R019"}); err != nil || out.Outcome != "needs_human" {
			t.Fatalf("unlocalizable hold requires a fabricated line: %+v %v", out, err)
		}
	}
}

func TestReviewerRequiresMatchingExplicitLineRange(t *testing.T) {
	in := reviewTestInput("https://example.invalid")
	in.After = "// candidate\n" + in.After
	for _, mutation := range []string{"missing", "null", "wrong-line", "outside", "valid"} {
		t.Run(mutation, func(t *testing.T) {
			wire := reviewWire(in, "needs_changes")
			issue := wire["issues"].([]any)[0].(map[string]any)
			issue["startLine"], issue["endLine"], issue["excerpt"] = 2, 2, strings.Split(in.After, "\n")[1]
			switch mutation {
			case "missing":
				delete(issue, "startLine")
			case "null":
				issue["endLine"] = nil
			case "wrong-line":
				issue["startLine"], issue["endLine"] = 1, 1
			case "outside":
				issue["endLine"] = 3
			}
			_, err := parseIndependentReview(string(raw(wire)), in)
			if (err == nil) != (mutation == "valid" || mutation == "wrong-line" || mutation == "outside") {
				t.Fatalf("range validation mismatch: %s %v", mutation, err)
			}
		})
	}
	wire := reviewWire(in, "needs_changes")
	issue := wire["issues"].([]any)[0].(map[string]any)
	issue["startLine"], issue["endLine"], issue["excerpt"] = 2, 2, strings.Split(in.After, "\n")[1]
	wire["verdict"] = "needs_human"
	wire["assessments"].([]any)[1].(map[string]any)["status"] = "needs_human"
	if _, err := parseIndependentReview(string(raw(wire)), in); err != nil {
		t.Fatal("grounded reviewer hold rejected", err)
	}
}

func TestFreshPlanCorrectsUniqueExcerptLineBeforePersistence(t *testing.T) {
	state := model.RepairState{Version: 1}
	in := testInput("https://example.invalid")
	in.Content = "before();\nLegacy.Save()\nafter();\n"
	update := heldLocationPlan() // The correct quotation has an incorrect line 1.
	call := functionCall{Name: "update_state", Arguments: string(raw(testPlanWire(update)))}
	noop := func() error { return nil }
	_, err := executeRepairTool(context.Background(), in, call, &state, []string{"R019"}, map[string]string{"R019": "rule"}, in.Config, noop, noop)
	if err != nil || state.Plan.Revision != 1 || state.Plan.Items[0].SourceLocations[0].StartLine != 2 || state.Plan.Items[0].SourceLocations[0].EndLine != 2 {
		t.Fatalf("fresh plan was not normalized before persistence: %+v %v", state.Plan, err)
	}
	if out, err := parseCandidateFinal(`{"outcome":"needs_human","candidateId":"","note":"確認してください"}`, state, in, []string{"R019"}); err != nil || out.Outcome != "needs_human" {
		t.Fatalf("normalized plan failed strict final validation: %+v %v", out, err)
	}
}
