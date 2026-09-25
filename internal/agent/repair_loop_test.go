package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func requestHistory(t *testing.T, r *http.Request) []any {
	t.Helper()
	var req map[string]any
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		t.Fatal(err)
	}
	if req["conversation"] != nil || req["previous_response_id"] != nil {
		t.Fatal("remote conversation resumed")
	}
	return req["input"].([]any)
}
func candidateFinal(id string) map[string]any {
	return map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": fmt.Sprintf(`{"outcome":"modified","candidateId":%q,"note":"修正しました"}`, id)}}}
}
func initializedRepairState() model.RepairState {
	plan := testPlan(0)
	return model.RepairState{Version: 1, Plan: model.RepairPlan{Revision: 1, RuleDecisions: plan.RuleDecisions, Items: plan.Items}, ReadRuleIDs: []string{"R019"}}
}

func TestValidationFailureRepairsInSameConversation(t *testing.T) {
	content := "Legacy.Save()\nLegacy.Log()\n"
	sum := sha256.Sum256([]byte(content))
	candidate := testCandidate()
	candidate.BaseHash = hex.EncodeToString(sum[:])
	candidate.AddressedItemIDs = append(candidate.AddressedItemIDs, "P2")
	candidate.Edits[0].ItemIDs = []string{"P1", "P2"} // Deliberately incomplete proposal; validation must catch it.
	plan := testPlan(0)
	plan.Items = append(plan.Items, model.PlanItem{ID: "P2", RuleID: "R019", Location: "Legacy.Log", Change: "Replace logging", Expected: "No old logging", Status: "pending"})
	turn, validations := 0, 0
	var saved model.RepairState
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		if turn > 2 && !strings.Contains(fmt.Sprint(history), "Legacy.Log remains") {
			t.Error("validator failure missing from continuation")
		}
		if !saved.RequestPending || saved.RequestID == "" || saved.Usage.Turns != turn+1 || saved.ElapsedMS != 180000 {
			t.Error("request sent before durable pending reservation")
		}
		switch turn {
		case 0:
			respond(w, standardTurn(0))
		case 1:
			respond(w, testCall("plan", "update_state", plan))
		case 2:
			respond(w, testCall("v1", "validate_candidate", candidate))
		case 3:
			respond(w, candidateFinal("C1")) // Cannot bypass a failed validation.
		case 4:
			if !strings.Contains(fmt.Sprint(history), "Final response rejected") {
				t.Error("premature final did not produce feedback")
			}
			candidate.Edits[0].ItemIDs = []string{"P1"}
			candidate.Edits = append(candidate.Edits, model.Edit{OldText: "Legacy.Log()", NewText: "Modern.Log()", ItemIDs: []string{"P2"}})
			respond(w, testCall("v2", "validate_candidate", candidate))
		case 5:
			respond(w, candidateFinal("C2"))
		default:
			t.Error("unexpected extra model request")
		}
		turn++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Content = content
	in.Config.MaxTurns = 7
	in.SaveRepairState = func(s model.RepairState) error { saved = s; return nil }
	in.ValidateCandidate = func(_ context.Context, request model.CandidateRequest) (model.CandidateValidation, error) {
		validations++
		result := model.CandidateValidation{CandidateID: fmt.Sprintf("C%d", validations), PlanRevision: request.PlanRevision, Passed: validations == 2}
		if validations == 1 {
			result.Diagnostics = []model.CandidateDiagnostic{{Check: "legacy_symbols", Line: 2, LineBasis: "candidate", Excerpt: "Legacy.Log()", Message: "Legacy.Log remains"}}
		} else if len(request.Edits) != 2 || request.Edits[0].OldText != "Legacy.Save()" {
			t.Error("second candidate did not replace complete original-based proposal")
		}
		return result, nil
	}
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || out.CandidateID != "C2" || len(out.Edits) != 2 || validations != 2 || out.Usage.Turns != 7 {
		t.Fatalf("repair loop failed: %+v %v validations=%d", out, err, validations)
	}
	if saved.RequestPending || saved.Usage.Turns != 7 || saved.ValidationCount != 2 || saved.LastCandidate.Result.CandidateID != "C2" {
		t.Fatalf("incorrect checkpoint: %+v", saved)
	}
}

func TestRepairResumptionRestoresPlanRulesAndCumulativeBudgets(t *testing.T) {
	prior := initializedRepairState()
	prior.Usage = model.Usage{InputTokens: 900, CachedTokens: 90, OutputTokens: 400, CostUSD: .005, Turns: 4}
	prior.ToolCalls = 5
	prior.ReadBytes = 1000
	prior.ValidationCount = 1
	prior.ElapsedMS = 250
	req := testCandidate()
	prior.LastCandidate = &model.CandidateRecord{Request: req, Result: model.CandidateValidation{CandidateID: "C0", PlanRevision: 1, Passed: false, Diagnostics: []model.CandidateDiagnostic{{Message: "previous check failed"}}}}
	calls, reads := 0, 0
	var saved model.RepairState
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		if calls == 0 {
			if len(history) != 1 || !strings.Contains(fmt.Sprint(history), "previouslyReadRules") || !strings.Contains(fmt.Sprint(history), "Use Modern.Save()") || !strings.Contains(fmt.Sprint(history), "previous check failed") {
				t.Errorf("resume context not rebuilt: %v", history)
			}
			respond(w, testCall("resume-validate", "validate_candidate", req))
		} else {
			respond(w, goodItem())
		}
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &prior
	in.Config.MaxTurns = 7
	read := in.ReadRule
	in.ReadRule = func(id string) (string, error) { reads++; return read(id) }
	in.SaveRepairState = func(s model.RepairState) error { saved = s; return nil }
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || out.Usage.Turns != 3 || out.Usage.InputTokens != 200 || saved.Usage.Turns != 7 || saved.Usage.InputTokens != 1100 || saved.ValidationCount != 2 || saved.ToolCalls != 6 || saved.ElapsedMS < 250 || reads != 2 {
		t.Fatalf("resume counters/state failed: out=%+v state=%+v err=%v reads=%d", out, saved, err, reads)
	}
	if prior.Usage.Turns != 4 || prior.ValidationCount != 1 {
		t.Error("input journal mutated through aliases")
	}
	out, err = Run(context.Background(), Input{Config: in.Config, File: in.File, Content: in.Content, Rules: in.Rules, CandidateRules: in.CandidateRules, ReadRule: in.ReadRule, SaveRepairState: in.SaveRepairState, ValidateCandidate: in.ValidateCandidate, RepairState: &saved})
	if err != nil || out.Outcome != "modified" || out.Usage.Turns != 0 || calls != 2 {
		t.Fatalf("saved final review was not recovered without another request: %+v %v calls=%d", out, err, calls)
	}
}

func TestUnknownInFlightRequestPersistsAndNeedsAcknowledgment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	var saved model.RepairState
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; cancel(); <-release }))
	defer srv.Close()
	in := testInput(srv.URL)
	in.SaveRepairState = func(s model.RepairState) error { saved = s; return nil }
	out, err := Run(ctx, in)
	close(release)
	if !errors.Is(err, context.Canceled) || !IsUsageUnknown(err) || !out.Usage.Uncertain || !saved.RequestPending || !saved.Usage.Uncertain || saved.Usage.Turns != 1 || saved.ElapsedMS >= 180000 || calls != 1 {
		t.Fatalf("ambiguous cancellation not journaled: out=%+v state=%+v err=%v calls=%d", out, saved, err, calls)
	}
	in.RepairState = &saved
	out, err = Run(context.Background(), in)
	if err == nil || !IsUsageUnknown(err) || calls != 1 || out.Usage.Turns != 0 {
		t.Fatalf("unacknowledged request replayed: %+v %v", out, err)
	}
	in.AllowUncertainResume = true
	in.Config.MaxCostUSD = 1
	in.Config.InputPricePerMillion = 1
	in.Config.OutputPricePerMillion = 1
	_, err = Run(context.Background(), in)
	if err == nil || calls != 1 {
		t.Fatal("uncertain usage resumed with cost cap")
	}
}

func TestAcknowledgedUnpricedResumePreservesUsageUncertainty(t *testing.T) {
	prior := initializedRepairState()
	prior.Plan.Items[0].Status = "blocked"
	prior.Plan.Items[0].HoldReason = "呼び出し元のAPI契約を確認する必要があります。"
	prior.RequestPending = true
	prior.RequestID = "old"
	prior.Usage = model.Usage{Uncertain: true, Turns: 1}
	calls := 0
	var saved model.RepairState
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; respond(w, finalItem("needs_human", nil, nil)) }))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &prior
	in.AllowUncertainResume = true
	in.SaveRepairState = func(s model.RepairState) error { saved = s; return nil }
	out, err := Run(context.Background(), in)
	if err != nil || calls != 1 || out.Usage.Turns != 1 || saved.RequestPending || !saved.Usage.Uncertain || saved.Usage.Turns != 2 {
		t.Fatalf("explicit recovery failed: %+v %+v %v", out, saved, err)
	}
}

func TestJournalFailureBeforeRequestSendsNothing(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; respond(w, goodItem()) }))
	defer srv.Close()
	in := testInput(srv.URL)
	in.SaveRepairState = func(model.RepairState) error { return errors.New("disk full") }
	_, err := Run(context.Background(), in)
	if err == nil || !IsFatal(err) || calls != 0 {
		t.Fatalf("request escaped persistence failure: %v calls=%d", err, calls)
	}
}

func TestValidatorIsolationErrorStopsInsteadOfModelRetry(t *testing.T) {
	calls, validations := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		respond(w, testCall("validate", "validate_candidate", testCandidate()))
	}))
	defer srv.Close()
	state := initializedRepairState()
	in := testInput(srv.URL)
	in.RepairState = &state
	in.ValidateCandidate = func(context.Context, model.CandidateRequest) (model.CandidateValidation, error) {
		validations++
		return model.CandidateValidation{}, errors.New("rollback could not restore worktree")
	}
	_, err := Run(context.Background(), in)
	if err == nil || !IsFatal(err) || calls != 1 || validations != 1 {
		t.Fatalf("isolation failure continued: %v calls=%d validations=%d", err, calls, validations)
	}
}

func TestCandidateInputErrorIsRepairableWithoutValidationCharge(t *testing.T) {
	state := initializedRepairState()
	calls, validations := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		switch calls {
		case 0:
			req := testCandidate()
			req.Edits[0].OldText = "Absent.Save()"
			respond(w, testCall("bad", "validate_candidate", req))
		case 1:
			if !strings.Contains(fmt.Sprint(history), "original target") {
				t.Error("invalid edit diagnostic not returned")
			}
			respond(w, testCall("fixed", "validate_candidate", testCandidate()))
		case 2:
			respond(w, goodItem())
		}
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &state
	in.SaveRepairState = func(s model.RepairState) error { state = s; return nil }
	validate := in.ValidateCandidate
	in.ValidateCandidate = func(ctx context.Context, req model.CandidateRequest) (model.CandidateValidation, error) {
		validations++
		return validate(ctx, req)
	}
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "modified" || validations != 1 || state.ValidationCount != 1 || calls != 3 {
		t.Fatalf("input correction failed: %+v %v validations=%d", out, err, validations)
	}
}

func TestValidationAndElapsedLimitsSurviveResume(t *testing.T) {
	for _, kind := range []string{"validation", "elapsed", "cost"} {
		t.Run(kind, func(t *testing.T) {
			state := initializedRepairState()
			calls, validations := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				respond(w, testCall("validate", "validate_candidate", testCandidate()))
			}))
			defer srv.Close()
			in := testInput(srv.URL)
			in.RepairState = &state
			switch kind {
			case "validation":
				state.ValidationCount = 3
			case "elapsed":
				state.ElapsedMS = 180000
			case "cost":
				state.Usage.CostUSD = .999
				in.Config.MaxCostUSD = 1
				in.Config.InputPricePerMillion = 10
				in.Config.OutputPricePerMillion = 10
			}
			in.ValidateCandidate = func(context.Context, model.CandidateRequest) (model.CandidateValidation, error) {
				validations++
				return model.CandidateValidation{}, nil
			}
			_, err := Run(context.Background(), in)
			expectedCalls := 0
			if kind == "validation" {
				expectedCalls = 1
			}
			if err == nil || calls != expectedCalls || validations != 0 || IsFatal(err) {
				t.Fatalf("budget reset or wrong classification: %v calls=%d validations=%d", err, calls, validations)
			}
		})
	}
}

func TestBaseHashUsesOriginalBytesProvidedByEngine(t *testing.T) {
	original := sha256.Sum256([]byte("\xef\xbb\xbfLegacy.Save()\r\n"))
	baseHash := hex.EncodeToString(original[:])
	state := initializedRepairState()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		if !strings.Contains(fmt.Sprint(history), baseHash) {
			t.Error("original bytes hash not sent")
		}
		if calls == 0 {
			req := testCandidate()
			req.BaseHash = baseHash
			respond(w, testCall("validate", "validate_candidate", req))
		} else {
			respond(w, goodItem())
		}
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.BaseHash = baseHash
	in.RepairState = &state
	if out, err := Run(context.Background(), in); err != nil || out.Outcome != "modified" {
		t.Fatalf("normalized input rejected raw-byte hash: %+v %v", out, err)
	}
}

func TestFinalCandidateRejectsStalePlanAndSkipBypass(t *testing.T) {
	in := testInput("")
	req := testCandidate()
	in.BaseHash = req.BaseHash
	state := initializedRepairState()
	state.LastCandidate = &model.CandidateRecord{Request: req, Result: model.CandidateValidation{CandidateID: "C1", PlanRevision: 1, Passed: true}}
	text := func(outcome, id string) string {
		return fmt.Sprintf(`{"outcome":%q,"candidateId":%q,"note":"理由"}`, outcome, id)
	}
	if out, err := parseCandidateFinal(text("modified", "C1"), state, in, []string{"R019"}); err != nil || len(out.RulesApplied) != 1 || out.RulesApplied[0] != "R019" {
		t.Fatalf("passed candidate not adopted: %+v %v", out, err)
	}
	if _, err := parseCandidateFinal(text("modified", "C0"), state, in, []string{"R019"}); err == nil {
		t.Fatal("stale candidate ID accepted")
	}
	state.Plan.Revision++
	if _, err := parseCandidateFinal(text("modified", "C1"), state, in, []string{"R019"}); err == nil {
		t.Fatal("candidate survived plan update")
	}
	if _, err := parseCandidateFinal(text("skipped", ""), state, in, []string{"R019"}); err == nil {
		t.Fatal("skipped bypassed unfinished modifications")
	}
	state.Plan.Items = nil
	state.Plan.RuleDecisions[0].Decision = "no_change"
	if _, err := parseCandidateFinal(text("skipped", ""), state, in, []string{"R019", "R020"}); err == nil {
		t.Fatal("skipped bypassed unreviewed candidate rule")
	}
	if out, err := parseCandidateFinal(text("skipped", ""), state, in, []string{"R019"}); err != nil || out.Outcome != "skipped" {
		t.Fatalf("reviewed no-change plan rejected: %+v %v", out, err)
	}
}

func TestMalformedCandidateMissingNewTextCannotMeanDeletion(t *testing.T) {
	var request model.CandidateRequest
	for _, edits := range []string{`[{"oldText":"a"}]`, `[{"oldText":"a","newText":null}]`, `null`} {
		text := fmt.Sprintf(`{"planRevision":1,"baseHash":"x","edits":%s,"addressedItemIds":["P1"]}`, edits)
		if err := strictRequiredJSON(text, &request, "planRevision", "baseHash", "edits", "addressedItemIds"); err == nil {
			t.Fatal("missing newText accepted as deletion")
		}
	}
	text := `{"planRevision":1,"baseHash":"x","edits":[{"oldText":"a","newText":""}],"addressedItemIds":["P1"]}`
	if err := strictRequiredJSON(text, &request, "planRevision", "baseHash", "edits", "addressedItemIds"); err != nil {
		t.Fatal(err)
	}
}

func TestHugeValidationOutputKeepsFailureUsefulAndJournalBounded(t *testing.T) {
	state := initializedRepairState()
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		if calls == 0 {
			respond(w, testCall("validate", "validate_candidate", testCandidate()))
		} else if calls == 1 {
			last := history[len(history)-1].(map[string]any)["output"].(string)
			if len(last) > maxToolBytes || !strings.Contains(last, "assertion failed") || !strings.Contains(last, "capturePayload") || strings.Contains(last, "response exceeds") {
				t.Errorf("large stdout hid useful validator output: %s", bounded(last, 1000))
			}
			hold := testPlan(1)
			hold.Items[0].Status = "blocked"
			hold.Items[0].HoldReason = "capturePayloadの代替API契約が資料にありません。"
			respond(w, testCall("hold", "update_state", hold))
		} else {
			respond(w, finalItem("needs_human", nil, nil))
		}
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.RepairState = &state
	in.SaveRepairState = func(s model.RepairState) error { state = s; return nil }
	in.ValidateCandidate = func(context.Context, model.CandidateRequest) (model.CandidateValidation, error) {
		result := model.CandidateValidation{CandidateID: "C1", PlanRevision: 1, Passed: false}
		for i := 0; i < 150; i++ {
			result.Checks = append(result.Checks, model.Check{Name: fmt.Sprintf("command%d", i), Status: "fail", Detail: "assertion failed\n" + strings.Repeat("x\x00\"", 16<<10)})
		}
		result.Diagnostics = []model.CandidateDiagnostic{{Check: "legacy_symbols", Message: "capturePayload remains", Excerpt: strings.Repeat("y", 2<<20)}}
		return result, nil
	}
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "needs_human" || calls != 3 {
		t.Fatalf("large output broke tool continuation: %+v %v", out, err)
	}
	if state.LastCandidate == nil || len(raw(state.LastCandidate.Result)) > 96<<10 || len(state.LastCandidate.Result.Diagnostics) > 20 {
		t.Fatalf("journal retained oversized validation output: %d", len(raw(state.LastCandidate)))
	}
}

func TestBoundedValidationEscapesCannotStallOrExceedTextBudget(t *testing.T) {
	for _, value := range []string{"\x00…", "\x00\x00\x00", "\\\"…", strings.Repeat("\x00", 10000)} {
		for _, limit := range []int{8, 9, 16, 64} {
			out := boundedJSONText(value, limit)
			if len(raw(out)) > limit {
				t.Fatalf("escaped output exceeded limit: %q %d", out, limit)
			}
		}
	}
}

func TestValidationReservesElapsedWithoutUnknownLLMUsage(t *testing.T) {
	for _, mode := range []string{"passed", "validator_error", "journal_error"} {
		t.Run(mode, func(t *testing.T) {
			state := initializedRepairState()
			state.ElapsedMS = 200
			state.LastCandidate = &model.CandidateRecord{Request: testCandidate(), Result: model.CandidateValidation{CandidateID: "previous-passed", PlanRevision: 1, Passed: true}}
			calls, validations := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls == 0 {
					respond(w, testCall("validate", "validate_candidate", testCandidate()))
				} else {
					respond(w, goodItem())
				}
				calls++
			}))
			defer srv.Close()
			in := testInput(srv.URL)
			in.RepairState = &state
			in.SaveRepairState = func(s model.RepairState) error {
				if mode == "journal_error" && s.ValidationCount == 1 && !s.RequestPending && s.ElapsedMS == 180000 {
					return errors.New("cannot reserve validation time")
				}
				state = s
				return nil
			}
			in.ValidateCandidate = func(context.Context, model.CandidateRequest) (model.CandidateValidation, error) {
				validations++
				if state.ElapsedMS != 180000 || state.ValidationCount != 1 || state.RequestPending || state.Usage.Uncertain || state.LastCandidate != nil {
					t.Fatalf("validation did not reserve time independently of LLM usage: %+v", state)
				}
				if mode == "validator_error" {
					return model.CandidateValidation{}, errors.New("validation interrupted")
				}
				return model.CandidateValidation{CandidateID: "C1", PlanRevision: 1, Passed: true}, nil
			}
			out, err := Run(context.Background(), in)
			if mode == "passed" {
				if err != nil || out.Outcome != "modified" || calls != 2 || validations != 1 {
					t.Fatalf("validation failed: %+v %v calls=%d validations=%d", out, err, calls, validations)
				}
			} else {
				if state.LastCandidate != nil {
					t.Fatal("older passed candidate survived newer validation error")
				}
				wantValidations := 1
				if mode == "journal_error" {
					wantValidations = 0
				}
				if err == nil || !IsFatal(err) || calls != 1 || validations != wantValidations {
					t.Fatalf("failed reservation/validation continued: %v calls=%d validations=%d", err, calls, validations)
				}
			}
			if state.ElapsedMS < 200 || state.ElapsedMS >= 180000 || state.RequestPending || state.Usage.Uncertain {
				t.Fatalf("handled validation did not restore measured time with known usage: %+v", state)
			}
		})
	}
}
