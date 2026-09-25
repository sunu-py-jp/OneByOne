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
		{"old-record", [][]string{nil}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := model.CandidateRequest{PlanRevision: plan.Revision, AddressedItemIDs: []string{"P01", "P02"}}
			for _, links := range test.links {
				request.Edits = append(request.Edits, model.Edit{OldText: "before", NewText: "after", ItemIDs: links})
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
		if tool["name"] != "validate_candidate" {
			continue
		}
		props := tool["parameters"].(map[string]any)["properties"].(map[string]any)
		edit := props["edits"].(map[string]any)["items"].(map[string]any)
		if !strings.Contains(strings.Join(edit["required"].([]string), ","), "itemIds") {
			t.Fatal("new candidate tool does not request explicit edit attribution")
		}
		return
	}
	t.Fatal("validate_candidate tool missing")
}
