package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"onebyone/internal/model"
)

func TestIncrementalPlanAndEditsRetainUnmentionedWork(t *testing.T) {
	catalog := []model.Rule{{ID: "R1", Always: true}, {ID: "R2", Always: true}}
	item := func(id, rule, location string) model.PlanItem {
		return model.PlanItem{ID: id, RuleID: rule, Location: location, Change: "変更する", Expected: "整合する", Status: "proposed"}
	}
	plan, err := MergeRepairPlan(model.RepairPlan{}, model.PlanUpdate{RuleDecisions: []model.PlanDecision{{RuleID: "R1", Decision: "modify", Reason: "最初の箇所"}}, Items: []model.PlanItem{item("P1", "R1", "first")}}, catalog, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := model.RepairState{Version: 1, Plan: plan}
	first := model.StagedEdit{ID: "E1", Edit: model.Edit{OldText: "first()", NewText: "betterFirst()", ItemIDs: []string{"P1"}}}
	if err := stageEdits(&state, model.StageEditsRequest{PlanRevision: 1, BaseHash: "base", Edits: []model.StagedEdit{first}}, "first()\nsecond()\n", "base"); err != nil {
		t.Fatal(err)
	}
	if err := requireCompletePlan(plan, []string{"R1", "R2"}); err == nil {
		t.Fatal("partial plan allowed final validation")
	}
	state.Plan, err = MergeRepairPlan(state.Plan, model.PlanUpdate{ExpectedRevision: 1, RuleDecisions: []model.PlanDecision{{RuleID: "R2", Decision: "modify", Reason: "次の箇所"}}, Items: []model.PlanItem{item("P2", "R2", "second")}}, catalog, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := model.StagedEdit{ID: "E2", Edit: model.Edit{OldText: "second()", NewText: "betterSecond()", ItemIDs: []string{"P2"}}}
	if err := stageEdits(&state, model.StageEditsRequest{PlanRevision: 2, BaseHash: "base", Edits: []model.StagedEdit{second}}, "first()\nsecond()\n", "base"); err != nil {
		t.Fatal(err)
	}
	second.NewText = "await betterSecond()"
	if err := stageEdits(&state, model.StageEditsRequest{PlanRevision: 2, BaseHash: "base", Edits: []model.StagedEdit{second}}, "first()\nsecond()\n", "base"); err != nil {
		t.Fatal(err)
	}
	request := stagedRequest(state, "base")
	if err := CheckCandidatePlan(state.Plan, request); err != nil {
		t.Fatal(err)
	}
	text, err := materializeEdits("first()\nsecond()\n", request.Edits)
	if err != nil || text != "betterFirst()\nawait betterSecond()\n" || len(state.Plan.Items) != 2 || len(state.Plan.RuleDecisions) != 2 {
		t.Fatalf("lost incremental work: %q %v %+v", text, err, state)
	}
	before := cloneRepairState(state)
	bad := second
	bad.OldText = "first()"
	if err := stageEdits(&state, model.StageEditsRequest{PlanRevision: 2, BaseHash: "base", Edits: []model.StagedEdit{bad}}, "first()\nsecond()\n", "base"); err == nil {
		t.Fatal("overlapping edit accepted")
	}
	if !reflect.DeepEqual(state, before) {
		t.Fatal("failed stage mutated durable state")
	}
}

func TestIncrementalPlanAndEditsHaveNoHistoricalItemOrByteCaps(t *testing.T) {
	update := model.PlanUpdate{RuleDecisions: []model.PlanDecision{{RuleID: "R1", Decision: "modify", Reason: "多数の独立箇所"}}}
	var original strings.Builder
	staged := []model.StagedEdit{}
	for i := 0; i < 400; i++ {
		id := fmt.Sprintf("P%d", i)
		line := fmt.Sprintf("source-%04d", i)
		update.Items = append(update.Items, model.PlanItem{ID: id, RuleID: "R1", Location: line, Change: strings.Repeat("具体的な変更 ", 50), Expected: "整合する", Status: "proposed"})
		original.WriteString(line + "\n")
		staged = append(staged, model.StagedEdit{ID: fmt.Sprintf("E%d", i), Edit: model.Edit{OldText: line, NewText: fmt.Sprintf("updated-%04d", i), ItemIDs: []string{id}}})
	}
	plan, err := MergeRepairPlan(model.RepairPlan{}, update, []model.Rule{{ID: "R1", Always: true}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := model.RepairState{Version: 1, Plan: plan}
	if err := stageEdits(&state, model.StageEditsRequest{PlanRevision: 1, BaseHash: "base", Edits: staged}, original.String(), "base"); err != nil {
		t.Fatal(err)
	}
	if err := CheckCandidatePlan(state.Plan, stagedRequest(state, "base")); err != nil {
		t.Fatal(err)
	}
	if len(raw(plan)) <= 64<<10 || len(state.StagedEdits) != 400 {
		t.Fatal("test did not exceed old caps")
	}
	context := repairContext(Input{File: "src/large.txt", Content: original.String()}, state, nil, "rollover")
	if historyBytes(context) > 100<<10 {
		t.Fatal("durable plan echoed wholesale into rollover")
	}
	var reconstructed strings.Builder
	offset := 0
	for {
		output, err := readSavedState(Input{}, state, string(raw(map[string]any{"section": "plan", "offset": offset})))
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Content    string `json:"content"`
			NextOffset int    `json:"nextOffset"`
			Complete   bool   `json:"complete"`
		}
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		reconstructed.WriteString(page.Content)
		if page.Complete {
			break
		}
		if page.NextOffset <= offset {
			t.Fatal("paging made no progress")
		}
		offset = page.NextOffset
	}
	if reconstructed.String() != string(raw(plan)) {
		t.Fatal("paged state lost plan content")
	}
}

func TestRulePagingRequiresContiguousCompleteReading(t *testing.T) {
	body := strings.Repeat("規則を確認する。\n", 10000)
	in := Input{Rules: []model.Rule{{ID: "R1"}}, ReadRule: func(string) (string, error) { return body, nil }}
	state := model.RepairState{Version: 1}
	cache := map[string]string{}
	if _, err := readRulePage(in, &state, cache, "R1", len(body)-len("規則を確認する。\n")); err != nil {
		t.Fatal(err)
	}
	if len(cache) != 0 {
		t.Fatal("tail-only read marked entire rule read")
	}
	offset := 0
	for {
		result, err := readRulePage(in, &state, cache, "R1", offset)
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Complete   bool `json:"complete"`
			NextOffset int  `json:"nextOffset"`
		}
		_ = json.Unmarshal([]byte(result), &page)
		if page.Complete {
			break
		}
		offset = page.NextOffset
	}
	if cache["R1"] != body {
		t.Fatal("full paged rule not available to planning")
	}
}

func TestRunOutputTruncationAndRolloverPreserveStagedEdits(t *testing.T) {
	content := "Legacy.Save()\nLegacy.Log()\n"
	turn, validations := 0, 0
	rollover := false
	var saved model.RepairState
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		history := requestHistory(t, r)
		if turn > 8 && len(history) == 1 {
			rollover = true
			if saved.Plan.Revision != 2 || len(saved.StagedEdits) != 2 {
				t.Error("rollover lost state")
			}
		}
		stage := func(id, old, new, item string) model.StageEditsRequest {
			return model.StageEditsRequest{PlanRevision: saved.Plan.Revision, BaseHash: testHash(content), Edits: []model.StagedEdit{{ID: id, Edit: model.Edit{OldText: old, NewText: new, ItemIDs: []string{item}, Attributions: []model.EditAttribution{}}}}, RemoveEditIDs: []string{}}
		}
		switch {
		case turn == 0:
			respond(w, standardTurn(0))
		case turn == 1:
			respond(w, testCall("plan1", "update_state", testPlan(0)))
		case turn == 2:
			respond(w, testCall("edit1", "stage_edits", stage("E1", "Legacy.Save()", "Modern.Save()", "P1")))
		case turn == 3:
			if len(saved.StagedEdits) != 1 {
				t.Error("first edit not checkpointed")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}, "output": []any{map[string]any{"type": "function_call", "call_id": "broken", "name": "stage_edits", "arguments": "{\"edits\":["}}, "usage": map[string]any{"input_tokens": 100, "output_tokens": 40}})
		case turn == 4:
			if len(history) != 1 || len(saved.StagedEdits) != 1 || !strings.Contains(fmt.Sprint(history), "discarded") {
				t.Error("truncated output was not safely recovered")
			}
			respond(w, testCall("plan2", "update_state", model.PlanUpdate{ExpectedRevision: 1, RuleDecisions: []model.PlanDecision{}, Items: []model.PlanItem{{ID: "P2", RuleID: "R019", Location: "Legacy.Log()", Change: "ログを更新", Expected: "Modern.Log()", Status: "proposed"}}}))
		case turn == 5:
			respond(w, testCall("edit2", "stage_edits", stage("E2", "Legacy.Log()", "Modern.Log()", "P2")))
		case turn < 42:
			respond(w, testCall(fmt.Sprintf("context-%d", turn), "read_context", map[string]any{"path": "src/helper.txt", "startLine": 1, "endLine": 10, "offset": 0}))
		case turn == 42:
			respond(w, testCall("validate", "validate_staged_candidate", map[string]any{"planRevision": 2, "baseHash": testHash(content)}))
		case turn == 43:
			respond(w, candidateFinal("C1"))
		default:
			t.Errorf("unexpected turn %d", turn)
			cancel()
			w.WriteHeader(400)
		}
		turn++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Content = content
	in.SaveRepairState = func(state model.RepairState) error { saved = cloneRepairState(state); return nil }
	in.ReadContext = func(string, int, int) (string, error) { return strings.Repeat("helper context ", 1300), nil }
	in.ValidateCandidate = func(_ context.Context, req model.CandidateRequest) (model.CandidateValidation, error) {
		validations++
		if len(req.Edits) != 2 || req.Edits[0].NewText != "Modern.Save()" || req.Edits[1].NewText != "Modern.Log()" {
			t.Error("assembled candidate lost changes")
		}
		return model.CandidateValidation{AttributionVersion: model.LineAttributionVersion, CandidateID: "C1", CandidateHash: "hash", PlanRevision: req.PlanRevision, Passed: true}, nil
	}
	out, err := Run(ctx, in)
	if err != nil || out.Outcome != "modified" || len(out.Edits) != 2 || !rollover || validations != 1 || saved.ToolCalls <= 32 {
		t.Fatalf("incremental run failed: %+v err=%v rollover=%v validations=%d calls=%d", out, err, rollover, validations, saved.ToolCalls)
	}
}

func testHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func TestPagedRuleBatchFailureDoesNotMarkUndeliveredBodiesRead(t *testing.T) {
	in := Input{Rules: []model.Rule{{ID: "R1"}, {ID: "R2"}}, ReadRule: func(id string) (string, error) {
		if id == "R2" {
			return "", fmt.Errorf("cannot read")
		}
		return "first valid body", nil
	}}
	state := model.RepairState{Version: 1}
	cache := map[string]string{}
	_, err := readRulesPaged(in, &state, cache, functionCall{Name: "read_rules", Arguments: `{"ids":["R1","R2"]}`})
	if err == nil || len(cache) != 0 || len(state.RuleReadOffsets) != 0 {
		t.Fatalf("failed batch recorded unseen text: cache=%v progress=%v err=%v", cache, state.RuleReadOffsets, err)
	}
}

func TestIncrementalStagingPreservesProtectedHumanHold(t *testing.T) {
	original := "safe()\nhold()\n"
	state := model.RepairState{Version: 1, Plan: model.RepairPlan{Revision: 1, RuleDecisions: []model.PlanDecision{{RuleID: "R1", Decision: "modify", Reason: "独立した変更と保留"}}, Items: []model.PlanItem{
		{ID: "P1", RuleID: "R1", Location: "safe", Change: "安全な箇所を更新", Expected: "動作を維持", Status: "proposed"},
		{ID: "P2", RuleID: "R1", Location: "hold", Change: "仕様を確認", Expected: "契約を確認", Status: "blocked", HoldReason: "外部所有者に確認", SourceLocations: []model.SourceLocation{{StartLine: 2, EndLine: 2, Excerpt: "hold()"}}},
	}}}
	safe := model.StagedEdit{ID: "E1", Edit: model.Edit{OldText: "safe()", NewText: "safeUpdated()", ItemIDs: []string{"P1"}}}
	if err := stageEdits(&state, model.StageEditsRequest{PlanRevision: 1, BaseHash: "base", Edits: []model.StagedEdit{safe}}, original, "base"); err != nil {
		t.Fatal(err)
	}
	saved := cloneRepairState(state)
	unsafe := model.StagedEdit{ID: "E2", Edit: model.Edit{OldText: "hold()", NewText: "changedHold()", ItemIDs: []string{"P1"}}}
	if err := stageEdits(&state, model.StageEditsRequest{PlanRevision: 1, BaseHash: "base", Edits: []model.StagedEdit{unsafe}}, original, "base"); err == nil {
		t.Fatal("edit changed protected human hold")
	}
	if !reflect.DeepEqual(state, saved) {
		t.Fatal("rejected held edit discarded prior safe work")
	}
	if err := CheckCandidatePlan(state.Plan, stagedRequest(state, "base")); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeCompletedContextLimitContinuesSavedCandidate(t *testing.T) {
	prior := initializedRepairState()
	prior.StagedEdits = []model.StagedEdit{{ID: "E1", Edit: testCandidate().Edits[0]}}
	calls := 0
	var saved model.RepairState
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	usage := map[string]any{"input_tokens": 100, "output_tokens": 40}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
		}
		switch calls {
		case 0:
			claudeReply(w, "model_context_window_exceeded", usage, map[string]any{"type": "tool_use", "id": "discard-me", "name": "stage_edits", "input": map[string]any{"invalid": "must never execute"}})
		case 1:
			messages, _ := request["messages"].([]any)
			if len(messages) != 1 || !strings.Contains(fmt.Sprint(messages), "discarded") || len(saved.StagedEdits) != 1 || saved.ToolCalls != 0 {
				t.Error("context cutoff did not safely restart from prior edits")
			}
			claudeReply(w, "tool_use", usage, map[string]any{"type": "tool_use", "id": "validate", "name": "validate_staged_candidate", "input": map[string]any{"planRevision": 1, "baseHash": testHash("Legacy.Save()\n")}})
		case 2:
			claudeReply(w, "end_turn", usage, claudeFinal())
		default:
			t.Error("unexpected extra request")
			cancel()
			w.WriteHeader(400)
		}
		calls++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.Provider = "claude"
	in.RepairState = &prior
	in.SaveRepairState = func(state model.RepairState) error { saved = cloneRepairState(state); return nil }
	out, err := Run(ctx, in)
	if err != nil || out.Outcome != "modified" || calls != 3 || len(out.Edits) != 1 || out.Edits[0].NewText != "Modern.Save()" {
		t.Fatalf("context cutoff ended work: %+v calls=%d err=%v", out, calls, err)
	}
}
