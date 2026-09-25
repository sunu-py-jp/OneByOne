package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"onebyone/internal/model"
)

func testInput(endpoint string) Input {
	// Explicit test budgets keep transport/journal boundary tests independent
	// of the application's optional defaults.
	return Input{Config: model.Config{Endpoint: endpoint, Deployment: "my-deployment", Credential: "test-secret", MaxAttempts: 3, MaxTurns: 5, MaxOutputTokens: 4096, TimeoutSeconds: 180},
		File: "src/example.txt", Content: "Legacy.Save()\n", CandidateRules: []string{"R019"}, SystemPrompt: "R019: Replace Legacy.Save with Modern.Save",
		Rules:           []model.Rule{{ID: "R019", Pattern: "Legacy"}},
		SaveRepairState: func(model.RepairState) error { return nil },
		ReviewCandidate: passedTestReview,
		ValidateCandidate: func(_ context.Context, req model.CandidateRequest) (model.CandidateValidation, error) {
			return model.CandidateValidation{CandidateID: "C1", CandidateHash: "candidate-hash", PlanRevision: req.PlanRevision, Passed: true}, nil
		},
		ReadRule: func(id string) (string, error) {
			if id == "R019" {
				return "Use Modern.Save()", nil
			}
			return "", errors.New("not found")
		},
		ReadContext: func(p string, start, end int) (string, error) { return "context", nil },
	}
}

// Editor transport tests isolate the final review request while preserving its
// shared turn reservation and usage persistence. review_test.go exercises the
// real reviewer client with all three native provider protocols.
func passedTestReview(_ context.Context, in ReviewInput) (model.IndependentReview, error) {
	requestID, err := repairRequestID()
	if err != nil {
		return model.IndependentReview{}, err
	}
	if err := in.BeforeRequest(requestID); err != nil {
		return model.IndependentReview{}, err
	}
	usage := model.Usage{Turns: 1}
	if err := in.AfterRequest(usage, nil); err != nil {
		return model.IndependentReview{}, err
	}
	assessments := []model.ReviewAssessment{}
	for _, rule := range in.Rules {
		assessments = append(assessments, model.ReviewAssessment{RuleID: rule.ID, Status: "satisfied", Reason: "Mock review confirms the required rule."})
	}
	return model.IndependentReview{BaseHash: in.BaseHash, CandidateHash: in.CandidateHash, Verdict: "passed", Summary: "Mock independent review passed.", Assessments: assessments, Issues: []model.ReviewIssue{}, Usage: usage}, nil
}

func finalItem(outcome string, _ []model.Edit, _ []string) map[string]any {
	candidateID := ""
	if outcome == "modified" {
		candidateID = "C1"
	}
	proposal := map[string]any{"outcome": outcome, "candidateId": candidateID, "note": "確認しました"}
	b, _ := json.Marshal(proposal)
	return map[string]any{"type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(b)}}}
}
func goodItem() map[string]any { return finalItem("modified", nil, nil) }
func testPlan(revision int) model.PlanUpdate {
	return model.PlanUpdate{ExpectedRevision: revision, RuleDecisions: []model.PlanDecision{{RuleID: "R019", Decision: "modify", Reason: "Save call needs updating"}}, Items: []model.PlanItem{{ID: "P1", RuleID: "R019", Location: "Legacy.Save()", Change: "Use Modern.Save()", Expected: "Modern save call", Status: "proposed"}}}
}
func testCandidate() model.CandidateRequest {
	sum := sha256.Sum256([]byte("Legacy.Save()\n"))
	return model.CandidateRequest{PlanRevision: 1, BaseHash: hex.EncodeToString(sum[:]), Edits: []model.Edit{{OldText: "Legacy.Save()", NewText: "Modern.Save()", ItemIDs: []string{"P1"}}}, AddressedItemIDs: []string{"P1"}}
}
func testCall(id, name string, args any) map[string]any {
	b, _ := json.Marshal(args)
	return map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(b)}
}
func standardTurn(turn int) map[string]any {
	switch turn {
	case 0:
		return testCall("read", "read_rule", map[string]any{"id": "R019"})
	case 1:
		return testCall("plan", "update_state", testPlan(0))
	case 2:
		return testCall("validate", "validate_candidate", testCandidate())
	default:
		return goodItem()
	}
}

func respond(w http.ResponseWriter, items ...any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": items, "usage": map[string]any{"input_tokens": 100, "output_tokens": 40, "input_tokens_details": map[string]any{"cached_tokens": 30}}})
}

func TestNormalizeEndpoint(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://resource.openai.azure.com", "https://resource.openai.azure.com/openai/v1/responses"},
		{"https://resource.openai.azure.com/openai/v1/", "https://resource.openai.azure.com/openai/v1/responses"},
		{"https://resource.services.ai.azure.com/api/projects/p/openai/v1", "https://resource.services.ai.azure.com/api/projects/p/openai/v1/responses"},
		{"https://resource.openai.azure.com/openai/v1/responses/", "https://resource.openai.azure.com/openai/v1/responses"},
		{"http://127.0.0.1:456/responses", "http://127.0.0.1:456/responses"},
		{"http://[::1]:456", "http://[::1]:456/openai/v1/responses"},
	} {
		got, err := NormalizeEndpoint(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("NormalizeEndpoint(%q) = %q, %v", tc.in, got, err)
		}
	}
	for _, bad := range []string{"", "resource.openai.azure.com", "http://example.com", "http://localhost", "ftp://127.0.0.1", "https://u:secret@example.com", "https://example.com?api-key=x", "https://example.com?", "https://example.com#token", "https://example.com/openai/deployments/x", "https://example.com/a/../responses", "https://example.com/a%2fb/responses", "https://example.com//responses"} {
		if result, err := NormalizeEndpoint(bad); err == nil {
			t.Errorf("accepted unsafe endpoint %q as %q", bad, result)
		}
	}
}

func TestRunStructuredProposalAndUsage(t *testing.T) {
	turn := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.URL.Path != "/openai/v1/responses" || r.Header.Get("api-key") != "test-secret" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["model"] != "my-deployment" || req["store"] != false || req["max_output_tokens"] != float64(4096) {
			t.Errorf("unexpected request settings: %v", req)
		}
		format := req["text"].(map[string]any)["format"].(map[string]any)
		if format["type"] != "json_schema" || format["strict"] != true {
			t.Errorf("no strict JSON schema")
		}
		respond(w, standardTurn(turn))
		turn++
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.InputPricePerMillion = 1
	in.Config.CachedInputPricePerMillion = .1
	in.Config.OutputPricePerMillion = 2
	out, err := Run(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if out.Outcome != "modified" || len(out.Edits) != 1 || out.Edits[0].NewText != "Modern.Save()" {
		t.Fatalf("bad proposal %+v", out)
	}
	if out.Usage.InputTokens != 400 || out.Usage.OutputTokens != 160 || out.Usage.CachedTokens != 120 || math.Abs(out.Usage.CostUSD-.000612) > 1e-12 || out.Usage.Turns != 5 {
		t.Errorf("bad usage %+v", out.Usage)
	}
}

func TestToolLoopPreservesReasoningAndIsolatesFiles(t *testing.T) {
	count := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count++
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		if _, ok := req["previous_response_id"]; ok {
			t.Error("server history must not be shared")
		}
		if _, ok := req["conversation"]; ok {
			t.Error("server conversation must not be shared")
		}
		if !reflect.DeepEqual(req["include"], []any{"reasoning.encrypted_content"}) {
			t.Error("encrypted reasoning not requested")
		}
		history := req["input"].([]any)
		if count%4 == 1 {
			if len(history) != 1 {
				t.Errorf("new file started with %d history items", len(history))
			}
			respond(w, map[string]any{"type": "reasoning", "id": fmt.Sprintf("r%d", count), "summary": []any{}, "encrypted_content": "encrypted-test"}, map[string]any{"type": "function_call", "call_id": "call_1", "name": "read_rule", "arguments": `{"id":"R019"}`})
		} else if count%4 == 2 {
			if len(history) != 4 {
				t.Fatalf("expected 4 items; got %v", history)
			}
			if history[1].(map[string]any)["encrypted_content"] != "encrypted-test" {
				t.Error("reasoning discarded")
			}
			if history[3].(map[string]any)["output"] != "Use Modern.Save()" {
				t.Error("rule not returned")
			}
			respond(w, standardTurn(1))
		} else {
			respond(w, standardTurn((count-1)%4))
		}
	}))
	defer srv.Close()
	for i := 0; i < 2; i++ {
		out, err := Run(context.Background(), testInput(srv.URL))
		if err != nil || out.Usage.Turns != 5 || out.Usage.InputTokens != 400 {
			t.Fatalf("run %d: %+v, %v", i, out, err)
		}
	}
}

func TestRejectInvalidExactEdits(t *testing.T) {
	for name, tc := range map[string]struct {
		original string
		edits    []model.Edit
	}{
		"empty":                   {"abc", nil},
		"ambiguous":               {"aba", []model.Edit{{OldText: "a", NewText: "x"}}},
		"overlapping occurrences": {"aaa", []model.Edit{{OldText: "aa", NewText: "b"}}},
		"overlapping edits":       {"abc", []model.Edit{{OldText: "abc", NewText: "x"}, {OldText: "bc", NewText: "y"}}},
		"absent":                  {"abc", []model.Edit{{OldText: "z", NewText: "x"}}},
		"empty old":               {"abc", []model.Edit{{OldText: "", NewText: "x"}}},
		"no change":               {"abc", []model.Edit{{OldText: "abc", NewText: "abc"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateExactEdits(tc.edits, tc.original); err == nil {
				t.Fatal("accepted invalid exact edits")
			}
		})
	}
	if err := validateExactEdits([]model.Edit{{OldText: "abc", NewText: ""}}, "abc"); err != nil {
		t.Fatal("deletion must remain possible", err)
	}
	for _, value := range []string{
		`{"outcome":"modified","edits":[{"oldText":"Legacy.Save()","newText":"Modern.Save()"}],"rulesApplied":["R019"],"note":"old wire format"}`,
		`{"outcome":"needs_human","candidateId":"","note":null}`,
		`{"outcome":"needs_human","note":"missing candidate ID"}`,
		`{"outcome":"needs_human","candidateId":"","note":"reason","writeFile":"/etc/passwd"}`,
		`{"outcome":"needs_human","candidateId":"","note":"reason"} {}`,
	} {
		if _, err := parseCandidateFinal(value, model.RepairState{}, testInput(""), nil); err == nil {
			t.Errorf("accepted invalid final: %s", value)
		}
	}
}

func TestRefusalIncompleteAndMissingUsageCannotSucceed(t *testing.T) {
	for name, result := range map[string]map[string]any{
		"incomplete":     {"status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}, "output": []any{goodItem()}},
		"refusal":        {"status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "refusal", "refusal": "no"}}}}},
		"missing usage":  {"status": "completed", "output": []any{goodItem()}},
		"invalid output": {"status": "completed", "output": []any{map[string]any{"type": "computer_call"}}},
	} {
		t.Run(name, func(t *testing.T) {
			if name != "missing usage" {
				result["usage"] = map[string]any{"input_tokens": 100, "output_tokens": 50}
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _ = json.NewEncoder(w).Encode(result) }))
			defer srv.Close()
			out, err := Run(context.Background(), testInput(srv.URL))
			if err == nil || out.Outcome == "modified" {
				t.Fatalf("invalid result accepted: %+v, %v", out, err)
			}
			if name == "missing usage" && (!IsUsageUnknown(err) || !out.Usage.Uncertain) {
				t.Fatalf("missing usage was not explicitly classified: %+v %v", out.Usage, err)
			}
			if name != "missing usage" && out.Usage.OutputTokens != 50 {
				t.Errorf("lost billed usage: %+v", out.Usage)
			}
		})
	}
}

func TestCostLimitPreventsRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); respond(w, goodItem()) }))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.MaxCostUSD = .00001
	in.Config.InputPricePerMillion = 10
	in.Config.OutputPricePerMillion = 30
	_, err := Run(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "cost limit") || calls.Load() != 0 {
		t.Fatalf("budget did not stop request: %v (%d calls)", err, calls.Load())
	}
	in.Config.InputPricePerMillion = 0
	_, err = Run(context.Background(), in)
	if err == nil || !IsFatal(err) || calls.Load() != 0 {
		t.Fatalf("missing prices must fail closed: %v", err)
	}
}

func TestCostLimitStopsNextTurnAndPreservesUsage(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		respond(w, map[string]any{"type": "function_call", "call_id": "call_1", "name": "read_rule", "arguments": `{"id":"R019"}`})
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.InputPricePerMillion = 1
	in.Config.OutputPricePerMillion = 1
	// First request reserves less than this. The 96 KiB rule makes the next
	// request's conservative token reservation exceed the remaining allowance.
	in.Config.MaxCostUSD = .05
	in.ReadRule = func(id string) (string, error) { return strings.Repeat("x", 80<<10), nil }
	out, err := Run(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "cost limit") || calls.Load() != 1 || out.Usage.InputTokens != 100 {
		t.Fatalf("budget failure: %+v, %v, calls=%d", out, err, calls.Load())
	}
}

func TestToolsCannotEscapeOrExecuteCommands(t *testing.T) {
	reads := 0
	in := testInput("")
	in.ReadContext = func(p string, s, e int) (string, error) { reads++; return "ok", nil }
	for _, file := range []string{"../secret", "/etc/passwd", "C:/Users/test/key", "src/../../secret", "src\\test", ".git/config", ".env", "src/AGENTS.md", ".codex/settings.json", "src/./file", "src//file", "x\u0000y", "src/AGENTS.md.", "src/CLAUDE.md ", "keys/service.pem", "keys/user.KEY", "keys/cert.pfx", "src/COM1.txt", "NUL", "user/.npmrc"} {
		args, _ := json.Marshal(map[string]any{"path": file, "startLine": 1, "endLine": 10})
		_, err := executeTool(in, functionCall{Name: "read_context", Arguments: string(args)}, map[string]string{})
		if err == nil {
			t.Errorf("accepted unsafe context %q", file)
		}
	}
	if reads != 0 {
		t.Errorf("unsafe paths reached callback %d times", reads)
	}
	for _, name := range []string{"shell", "write_file", "apply_patch"} {
		if _, err := executeTool(in, functionCall{Name: name, Arguments: `{}`}, map[string]string{}); err == nil {
			t.Errorf("accepted %s", name)
		}
	}
	_, err := executeTool(in, functionCall{Name: "read_rule", Arguments: `{"id":"../R019"}`}, map[string]string{})
	if err == nil {
		t.Error("rule traversal accepted")
	}
}

func TestUnknownToolIsBoundedAndFedBack(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls == 1 {
			respond(w, map[string]any{"type": "function_call", "name": "shell", "call_id": "call_1", "arguments": `{"command":"rm"}`})
			return
		}
		var req struct {
			Input []json.RawMessage `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if !strings.Contains(string(req.Input[len(req.Input)-1]), "Unknown tool") {
			t.Error("tool denial not sent back")
		}
		respond(w, finalItem("needs_human", []model.Edit{}, []string{}))
	}))
	defer srv.Close()
	out, err := Run(context.Background(), testInput(srv.URL))
	if err != nil || out.Outcome != "needs_human" || calls != 2 {
		t.Fatalf("%+v, %v", out, err)
	}
}

func TestTurnLimit(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		respond(w, map[string]any{"type": "function_call", "call_id": fmt.Sprintf("call_%d", calls), "name": "read_rule", "arguments": `{"id":"R019"}`})
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.MaxTurns = 2
	out, err := Run(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "MaxTurns") || calls != 2 || out.Usage.Turns != 2 {
		t.Fatalf("bad turn limit: %+v, %v", out, err)
	}
}

func TestRedirectCannotLeakCredentials(t *testing.T) {
	var reached atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached.Add(1); respond(w, goodItem()) }))
	defer destination.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(307)
	}))
	defer source.Close()
	_, err := Run(context.Background(), testInput(source.URL))
	if err == nil || !IsFatal(err) || reached.Load() != 0 {
		t.Fatalf("redirect followed: %v, reached=%d", err, reached.Load())
	}
}

func TestBearerAndEnvironmentAuthentication(t *testing.T) {
	t.Setenv("AZURE_OPENAI_AUTH_TOKEN", "env-token")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer env-token" || r.Header.Get("api-key") != "" {
			t.Error("wrong bearer headers")
		}
		respond(w, finalItem("needs_human", nil, nil))
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.AuthMode, in.Config.Credential = "bearer", ""
	if _, err := Run(context.Background(), in); err != nil {
		t.Fatal(err)
	}
}

func TestRateLimitRetriesOnlyExplicit429(t *testing.T) {
	for _, status := range []int{429, 500, 401} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"provider_error","message":"test-secret"}}`))
			}))
			defer srv.Close()
			_, err := Run(context.Background(), testInput(srv.URL))
			want := 1
			if status == 429 {
				want = 3
			}
			if err == nil || !IsFatal(err) || calls != want || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("bad HTTP behavior status %d calls %d: %v", status, calls, err)
			}
		})
	}
}

func TestConnectionNeverSendsSource(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req["store"] != false || strings.Contains(fmt.Sprint(req), "Legacy") || req["tools"] != nil {
			t.Error("connection probe sent migration content")
		}
		respond(w, map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "OK"}}})
	}))
	defer srv.Close()
	result, err := TestConnection(context.Background(), testInput(srv.URL).Config)
	if err != nil || result == "" {
		t.Fatal(result, err)
	}
}

func TestUnknownUsageClassificationSurvivesFatalWrapping(t *testing.T) {
	for _, status := range []int{500, 408} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"error":{"code":"provider_error"}}`))
			}))
			defer srv.Close()
			out, err := Run(context.Background(), testInput(srv.URL))
			if !IsFatal(err) || !IsUsageUnknown(err) || !out.Usage.Uncertain {
				t.Fatalf("classification lost: %+v %v", out.Usage, err)
			}
		})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"completed","usage":{"input_tokens":-1,"output_tokens":3},"output":[]}`))
	}))
	defer srv.Close()
	out, err := Run(context.Background(), testInput(srv.URL))
	if !IsUsageUnknown(err) || !out.Usage.Uncertain {
		t.Fatalf("invalid usage not classified: %+v %v", out.Usage, err)
	}
}
