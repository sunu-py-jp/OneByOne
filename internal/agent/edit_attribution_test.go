package agent

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestCandidateEditAttributionRequiresExactPlanLinks(t *testing.T) {
	plan := checkedRepairPlan(t)
	for _, test := range []struct {
		name  string
		links [][]string
		valid bool
	}{
		{"separate", [][]string{{"P01"}, {"P02"}}, true},
		{"shared-edit", [][]string{{"P01", "P02"}}, true},
		{"unknown", [][]string{{"P01", "unknown"}, {"P02"}}, false},
		{"duplicate", [][]string{{"P01", "P01", "P02"}}, false},
		{"unmapped-item", [][]string{{"P01"}}, false},
		{"empty-mapping", [][]string{{}, {"P01", "P02"}}, false},
		{"mixed-old-new", [][]string{nil, {"P01", "P02"}}, false},
		{"unlinked-edit", [][]string{nil}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := model.CandidateRequest{PlanRevision: plan.Revision, AddressedItemIDs: []string{"P01", "P02"}}
			for _, links := range test.links {
				edit := model.Edit{OldText: "before", NewText: "after", ItemIDs: links}
				if len(links) > 1 {
					for _, id := range links {
						edit.Attributions = append(edit.Attributions, model.EditAttribution{ItemID: id, BeforeText: "before", AfterText: "after"})
					}
				}
				request.Edits = append(request.Edits, edit)
			}
			if err := CheckCandidatePlan(plan, request); (err == nil) != test.valid {
				t.Fatalf("mapping acceptance mismatch: %v", err)
			}
		})
	}
}

func TestLiveCandidateRequiresAttributionAndCanRepairWithoutValidationCharge(t *testing.T) {
	for _, missing := range []string{"omitted", "null", "empty"} {
		t.Run(missing, func(t *testing.T) {
			state := initializedRepairState()
			calls, validations := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				history := requestHistory(t, r)
				switch calls {
				case 0:
					req := testCandidate()
					edit := map[string]any{"oldText": req.Edits[0].OldText, "newText": req.Edits[0].NewText}
					if missing == "null" {
						edit["itemIds"] = nil
					} else if missing == "empty" {
						edit["itemIds"] = []string{}
					}
					respond(w, testCall("missing-links", "validate_candidate", map[string]any{"planRevision": req.PlanRevision, "baseHash": req.BaseHash, "edits": []any{edit}, "addressedItemIds": req.AddressedItemIDs}))
				case 1:
					if !strings.Contains(fmt.Sprint(history), "requires nonempty itemIds") || validations != 0 || state.ValidationCount != 0 {
						t.Error("missing attribution reached validation or did not return a repairable diagnostic")
					}
					respond(w, testCall("linked", "validate_candidate", testCandidate()))
				case 2:
					respond(w, goodItem())
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
			validate := in.ValidateCandidate
			in.ValidateCandidate = func(ctx context.Context, request model.CandidateRequest) (model.CandidateValidation, error) {
				validations++
				if len(request.Edits[0].ItemIDs) != 1 || request.Edits[0].ItemIDs[0] != "P1" {
					t.Fatal("validator received missing or fabricated mapping")
				}
				return validate(ctx, request)
			}
			out, err := Run(context.Background(), in)
			if err != nil || out.Outcome != "modified" || validations != 1 || calls != 3 || state.ValidationCount != 1 || len(state.LastCandidate.Request.Edits[0].ItemIDs) != 1 {
				t.Fatalf("new attribution protocol did not recover: %+v %v calls=%d validations=%d", out, err, calls, validations)
			}
		})
	}
}

func TestCandidateToolRequiresPerEditItemIDs(t *testing.T) {
	for _, tool := range toolDefinitions() {
		if tool["name"] != "stage_edits" {
			continue
		}
		props := tool["parameters"].(map[string]any)["properties"].(map[string]any)
		edit := props["edits"].(map[string]any)["items"].(map[string]any)
		if !strings.Contains(strings.Join(edit["required"].([]string), ","), "itemIds") || !strings.Contains(strings.Join(edit["required"].([]string), ","), "attributions") {
			t.Fatal("new candidate tool does not request explicit edit attribution")
		}
		return
	}
	t.Fatal("stage_edits tool missing")
}

func TestCandidateFragmentAttributionRequiresCoverageAndNoDuplicateClaims(t *testing.T) {
	plan := checkedRepairPlan(t)
	shared := []model.EditAttribution{{ItemID: "P01", BeforeText: "before", AfterText: "after"}, {ItemID: "P02", BeforeText: "before", AfterText: "after"}}
	for _, test := range []struct {
		name      string
		fragments []model.EditAttribution
		valid     bool
	}{
		{"shared-minimal-change", shared, true},
		{"multi-item-empty", nil, false},
		{"uncovered-item", shared[:1], false},
		{"unknown-item", append(append([]model.EditAttribution{}, shared...), model.EditAttribution{ItemID: "P03", BeforeText: "a", AfterText: "b"}), false},
		{"duplicate-item-fragment", append(append([]model.EditAttribution{}, shared...), shared[0]), false},
		{"empty-anchor", []model.EditAttribution{{ItemID: "P01", BeforeText: "", AfterText: "after"}, shared[1]}, false},
		{"unchanged-fragment", []model.EditAttribution{{ItemID: "P01", BeforeText: "before", AfterText: "before"}, shared[1]}, false},
		{"deletion", []model.EditAttribution{{ItemID: "P01", BeforeText: "before", AfterText: ""}, {ItemID: "P02", BeforeText: "before", AfterText: ""}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := model.CandidateRequest{PlanRevision: plan.Revision, AddressedItemIDs: []string{"P01", "P02"}, Edits: []model.Edit{{OldText: "before", NewText: "after", ItemIDs: []string{"P01", "P02"}, Attributions: test.fragments}}}
			if err := CheckCandidatePlan(plan, req); (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
		})
	}
}

func TestLiveCandidateRequiresExplicitAttributionArrayAndFragmentFields(t *testing.T) {
	for _, value := range []string{"", "null", `[{"itemId":"P1","beforeText":"Legacy.Save()"}]`, `[{"itemId":"P1","beforeText":"Legacy.Save()","afterText":null}]`} {
		t.Run(value, func(t *testing.T) {
			state := initializedRepairState()
			in := testInput("https://example.invalid")
			in.BaseHash = testCandidate().BaseHash
			validations := 0
			in.ValidateCandidate = func(context.Context, model.CandidateRequest) (model.CandidateValidation, error) {
				validations++
				return model.CandidateValidation{}, nil
			}
			attr := ""
			if value != "" {
				attr = `,"attributions":` + value
			}
			args := fmt.Sprintf(`{"planRevision":1,"baseHash":%q,"addressedItemIds":["P1"],"edits":[{"oldText":"Legacy.Save()","newText":"Modern.Save()","itemIds":["P1"]%s}]}`, in.BaseHash, attr)
			noop := func() error { return nil }
			_, err := executeRepairTool(context.Background(), in, functionCall{Name: "validate_candidate", Arguments: args}, &state, []string{"R019"}, map[string]string{}, in.Config, noop, noop)
			if err == nil || validations != 0 || state.ValidationCount != 0 {
				t.Fatalf("invalid fields reached validation: %v count=%d", err, validations)
			}
		})
	}
}

func TestLegacyAttributionCandidateIsRebuiltWithinSameRun(t *testing.T) {
	state := initializedRepairState()
	state.Usage = model.Usage{Turns: 3, InputTokens: 200, CostUSD: .01}
	state.ValidationCount = 1
	state.LastCandidate = &model.CandidateRecord{Request: testCandidate(), Result: model.CandidateValidation{CandidateID: "obsolete", PlanRevision: 1, Passed: true}, ReviewRequested: true, ReviewNote: "old candidate"}
	calls, validations := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		switch calls {
		case 0:
			if !strings.Contains(fmt.Sprint(history), "Previous attribution was obsolete") || state.LastCandidate != nil || state.Plan.Revision != 1 || state.Usage.InputTokens != 200 || state.ValidationCount != 1 {
				t.Errorf("old candidate did not resume with preserved state: %+v", state)
			}
			respond(w, testCall("revalidate", "validate_candidate", testCandidate()))
		case 1:
			respond(w, goodItem())
		default:
			t.Error("unexpected call")
			w.WriteHeader(http.StatusBadRequest)
		}
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &state
	in.SaveRepairState = func(saved model.RepairState) error { state = saved; return nil }
	validate := in.ValidateCandidate
	in.ValidateCandidate = func(ctx context.Context, request model.CandidateRequest) (model.CandidateValidation, error) {
		validations++
		return validate(ctx, request)
	}
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || calls != 2 || validations != 1 || state.ValidationCount != 2 || state.LastCandidate.Result.AttributionVersion != model.LineAttributionVersion || state.Usage.Turns != 6 {
		t.Fatalf("legacy candidate upgrade failed: %+v %v calls=%d state=%+v", out, err, calls, state)
	}
}

func TestLegacyAttributionCannotPassFinalOrCachedReview(t *testing.T) {
	in, state, final := reviewGateFixture()
	state.LastCandidate.Result.AttributionVersion = 0
	if _, err := parseCandidateFinal(string(raw(map[string]any{"outcome": "modified", "candidateId": final.CandidateID, "note": final.Note})), state, in, in.CandidateRules); err == nil {
		t.Fatal("old candidate accepted by final parser")
	}
	noop := func() error { return nil }
	if _, accepted, err := reviewFinal(context.Background(), in, &state, final, noop, noop); err == nil || accepted {
		t.Fatal("cached independent review bypassed attribution upgrade")
	}
}

func TestFailedUnversionedCandidateKeepsDiagnosticsOnResume(t *testing.T) {
	state := initializedRepairState()
	state.ValidationCount = 1
	state.LastCandidate = &model.CandidateRecord{Request: testCandidate(), Result: model.CandidateValidation{CandidateID: "failed", PlanRevision: 1, Passed: false, Diagnostics: []model.CandidateDiagnostic{{Message: "exact fragment did not match"}}}}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		switch calls {
		case 0:
			if state.LastCandidate == nil || state.LastCandidate.Result.CandidateID != "failed" || !strings.Contains(fmt.Sprint(history), "exact fragment did not match") || strings.Contains(fmt.Sprint(history), "Previous attribution was obsolete") {
				t.Errorf("failed candidate lost its diagnostics: history=%v candidate=%+v", history, state.LastCandidate)
			}
			respond(w, testCall("corrected", "validate_candidate", testCandidate()))
		case 1:
			respond(w, goodItem())
		default:
			t.Error("unexpected additional request")
			w.WriteHeader(http.StatusBadRequest)
		}
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &state
	in.SaveRepairState = func(saved model.RepairState) error { state = saved; return nil }
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || calls != 2 || state.ValidationCount != 2 || state.LastCandidate.Result.AttributionVersion != model.LineAttributionVersion {
		t.Fatalf("failed candidate did not resume normally: %+v %v calls=%d", out, err, calls)
	}
}
