package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"onebyone/internal/model"
)

func checkProviderRequest(t *testing.T, r *http.Request, provider, secret string) {
	t.Helper()
	wantPath := "/v1/responses"
	if provider == "azure" {
		wantPath = "/openai/v1/responses"
	} else if provider == "claude" {
		wantPath = "/v1/messages"
	}
	if r.Method != "POST" || r.URL.Path != wantPath {
		t.Errorf("%s request = %s %s", provider, r.Method, r.URL.Path)
	}
	switch provider {
	case "azure":
		if r.Header.Get("api-key") != secret || r.Header.Get("Authorization") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("wrong Azure authentication")
		}
	case "openai":
		if r.Header.Get("Authorization") != "Bearer "+secret || r.Header.Get("api-key") != "" || r.Header.Get("x-api-key") != "" {
			t.Error("wrong OpenAI authentication")
		}
	case "claude":
		if r.Header.Get("x-api-key") != secret || r.Header.Get("anthropic-version") != "2023-06-01" || r.Header.Get("Authorization") != "" || r.Header.Get("api-key") != "" {
			t.Error("wrong Claude authentication/version")
		}
	}
}

func claudeReply(w http.ResponseWriter, reason string, usage map[string]any, blocks ...any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"id": "message-test", "type": "message", "role": "assistant", "stop_reason": reason, "content": blocks, "usage": usage})
}

func claudeText(text string) map[string]any { return map[string]any{"type": "text", "text": text} }

func claudeFinal() map[string]any {
	item := goodItem()
	return claudeText(item["content"].([]any)[0].(map[string]any)["text"].(string))
}

func TestProviderEndpoints(t *testing.T) {
	for _, tc := range []struct{ provider, endpoint, want string }{
		{"openai", "", "https://api.openai.com/v1/responses"},
		{"claude", "", "https://api.anthropic.com/v1/messages"},
		{" OpenAI ", "https://api.openai.com/v1/", "https://api.openai.com/v1/responses"},
		{"claude", "https://api.anthropic.com", "https://api.anthropic.com/v1/messages"},
		{"claude", "https://proxy.example/api/v1", "https://proxy.example/api/v1/messages"},
		{"claude", "http://127.0.0.1:123/messages", "http://127.0.0.1:123/messages"},
		{"openai", "http://[::1]:123", "http://[::1]:123/v1/responses"},
	} {
		got, err := NormalizeProviderEndpoint(tc.provider, tc.endpoint)
		if err != nil || got != tc.want {
			t.Errorf("%s %s => %q %v", tc.provider, tc.endpoint, got, err)
		}
	}
	for _, provider := range []string{"azure", "openai", "claude"} {
		for _, bad := range []string{"http://remote.example", "http://localhost:123", "https://user:secret@example.com", "https://example.com?api-key=secret", "https://example.com/a/../v1", "https://example.com/a%2fb/v1", "https://example.com//v1", "https://example.com/v1#secret"} {
			if _, err := NormalizeProviderEndpoint(provider, bad); err == nil {
				t.Errorf("%s accepted unsafe endpoint %s", provider, bad)
			}
		}
	}
	if _, err := NormalizeProviderEndpoint("unknown", "https://example.com"); err == nil {
		t.Error("unknown provider accepted")
	}
}

func TestAllProvidersConnectionAndEnvironmentCredentials(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "azure-env")
	t.Setenv("AZURE_OPENAI_AUTH_TOKEN", "azure-bearer-env")
	t.Setenv("OPENAI_API_KEY", "openai-env")
	t.Setenv("ANTHROPIC_API_KEY", "claude-env")
	for _, provider := range []string{"azure", "openai", "claude"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				checkProviderRequest(t, r, provider, provider+"-env")
				var req map[string]any
				_ = json.NewDecoder(r.Body).Decode(&req)
				if req["model"] != "my-deployment" || req["tools"] != nil || req["system"] != nil || req["instructions"] != nil || strings.Contains(fmt.Sprint(req), "Legacy") {
					t.Error("connection probe included migration inputs or replaced the configured model")
				}
				if provider == "claude" {
					if req["max_tokens"] != float64(512) || req["store"] != nil || req["input"] != nil || req["messages"] == nil {
						t.Error("Claude connection probe used Responses fields")
					}
					claudeReply(w, "end_turn", map[string]any{"input_tokens": 10, "output_tokens": 2}, claudeText("OK"))
				} else {
					if req["store"] != false || req["max_output_tokens"] != float64(512) {
						t.Error("Responses connection probe omitted limits")
					}
					respond(w, map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "OK"}}})
				}
			}))
			defer srv.Close()
			cfg := testInput(srv.URL).Config
			cfg.Provider, cfg.Credential = provider, ""
			if result, err := TestConnection(context.Background(), cfg); err != nil || result == "" || calls != 1 {
				t.Fatalf("probe %s: %q %v calls=%d", provider, result, err, calls)
			}
		})
	}
}

func TestAllProvidersToolLoopPreservesNativeHistoryAndUsage(t *testing.T) {
	for _, provider := range []string{"azure", "openai", "claude"} {
		t.Run(provider, func(t *testing.T) {
			calls, reads := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				turn := calls % 5
				calls++
				checkProviderRequest(t, r, provider, "test-secret")
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if req["model"] != "my-deployment" || req["previous_response_id"] != nil || req["conversation"] != nil {
					t.Error("model was replaced or remote history was used")
				}
				key := "input"
				if provider == "claude" {
					key = "messages"
					if req["input"] != nil || req["store"] != nil || req["include"] != nil || req["text"] != nil || req["max_tokens"] != float64(4096) {
						t.Error("Claude migration request contains Responses fields")
					}
					format := req["output_config"].(map[string]any)["format"].(map[string]any)
					if format["type"] != "json_schema" || format["schema"] == nil || format["name"] != nil {
						t.Error("Claude did not use its native structured-output schema")
					}
					for _, tool := range req["tools"].([]any) {
						definition := tool.(map[string]any)
						if definition["strict"] != true || definition["input_schema"] == nil || definition["parameters"] != nil {
							t.Error("Claude tools lack their native strict schema")
						}
					}
				}
				history := req[key].([]any)
				if len(history) != 1+turn*2 {
					t.Fatalf("history crossed file boundary or lost a tool: %d items at turn %d", len(history), turn)
				}
				if turn > 0 {
					last := history[len(history)-1].(map[string]any)
					var result string
					if provider == "claude" {
						if last["role"] != "user" {
							t.Error("Claude tool result must be in a user message")
						}
						block := last["content"].([]any)[0].(map[string]any)
						result, _ = block["content"].(string)
						if block["type"] != "tool_result" || block["is_error"] != false {
							t.Error("Claude tool result encoding is incorrect")
						}
						if turn == 1 {
							assistant := history[1].(map[string]any)
							thinking := assistant["content"].([]any)[0].(map[string]any)
							if assistant["role"] != "assistant" || thinking["signature"] != "thinking-signature" {
								t.Error("Claude native signed thinking was not preserved")
							}
						}
					} else {
						result, _ = last["output"].(string)
					}
					want := "Use Modern.Save()"
					if turn == 2 {
						want = "bounded context"
					}
					if turn <= 2 && result != want {
						t.Errorf("missing tool output: got %q want %q", result, want)
					}
				}
				if provider == "claude" {
					usage := map[string]any{"input_tokens": []int{100, 20, 30, 30, 30}[turn], "output_tokens": 40, "cache_read_input_tokens": 200}
					switch turn {
					case 0:
						usage["cache_read_input_tokens"] = 0
						usage["cache_creation_input_tokens"] = 200
						claudeReply(w, "tool_use", usage, map[string]any{"type": "thinking", "thinking": "internal thought", "signature": "thinking-signature"}, map[string]any{"type": "tool_use", "id": "call_1", "name": "read_rule", "input": map[string]any{"id": "R019"}})
					case 1:
						claudeReply(w, "tool_use", usage, map[string]any{"type": "tool_use", "id": "call_2", "name": "read_context", "input": map[string]any{"path": "src/context.txt", "startLine": 1, "endLine": 10}})
					case 2:
						claudeReply(w, "tool_use", usage, map[string]any{"type": "tool_use", "id": "plan", "name": "update_state", "input": testPlan(0)})
					case 3:
						claudeReply(w, "tool_use", usage, map[string]any{"type": "tool_use", "id": "validate", "name": "validate_candidate", "input": testCandidate()})
					case 4:
						claudeReply(w, "end_turn", usage, claudeFinal())
					}
				} else {
					switch turn {
					case 0:
						respond(w, map[string]any{"type": "function_call", "call_id": "call_1", "name": "read_rule", "arguments": `{"id":"R019"}`})
					case 1:
						respond(w, map[string]any{"type": "function_call", "call_id": "call_2", "name": "read_context", "arguments": `{"path":"src/context.txt","startLine":1,"endLine":10}`})
					case 2:
						respond(w, standardTurn(1))
					case 3:
						respond(w, standardTurn(2))
					case 4:
						respond(w, goodItem())
					}
				}
			}))
			defer srv.Close()
			for file := 0; file < 2; file++ {
				in := testInput(srv.URL)
				in.Config.Provider = provider
				in.Config.MaxTurns = 6
				in.Config.InputPricePerMillion, in.Config.CachedInputPricePerMillion, in.Config.OutputPricePerMillion = 1, .1, 2
				in.ReadContext = func(path string, start, end int) (string, error) {
					reads++
					if path != "src/context.txt" || start != 1 || end != 10 {
						t.Error("context scope arguments changed")
					}
					return "bounded context", nil
				}
				out, err := Run(context.Background(), in)
				if err != nil || out.Outcome != "modified" || len(out.Edits) != 1 || out.Usage.Turns != 6 || out.Usage.OutputTokens != 200 {
					t.Fatalf("%s run %d: %+v %v", provider, file, out, err)
				}
				input, cached, cost := 500, 150, .000765
				if provider == "claude" {
					input, cached, cost = 1210, 800, .00094
				}
				if out.Usage.InputTokens != input || out.Usage.CachedTokens != cached || math.Abs(out.Usage.CostUSD-cost) > 1e-12 {
					t.Fatalf("%s counted cache tokens incorrectly: %+v", provider, out.Usage)
				}
			}
			if calls != 10 || reads != 2 {
				t.Fatalf("unexpected calls=%d reads=%d", calls, reads)
			}
		})
	}
}

func TestClaudeInvalidResponsesPreserveUsageAndFailClosed(t *testing.T) {
	for _, reason := range []string{"refusal", "max_tokens", "model_context_window_exceeded", "pause_turn", "unknown"} {
		t.Run(reason, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				claudeReply(w, reason, map[string]any{"input_tokens": 10, "output_tokens": 4, "cache_read_input_tokens": 20}, claudeFinal())
			}))
			defer srv.Close()
			in := testInput(srv.URL)
			in.Config.Provider = "claude"
			out, err := Run(context.Background(), in)
			if err == nil || out.Outcome == "modified" || out.Usage.InputTokens != 30 || out.Usage.OutputTokens != 4 {
				t.Fatalf("invalid stop reason adopted or billed usage lost: %+v %v", out, err)
			}
		})
	}
	for _, usage := range []map[string]any{nil, {"input_tokens": -1, "output_tokens": 4}, {"input_tokens": 10, "output_tokens": 4, "cache_creation_input_tokens": 5, "cache_creation": map[string]any{"ephemeral_5m_input_tokens": 10}}, {"input_tokens": 10, "output_tokens": 4, "cache_read_input_tokens": -1}} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { claudeReply(w, "end_turn", usage, claudeFinal()) }))
		in := testInput(srv.URL)
		in.Config.Provider = "claude"
		out, err := Run(context.Background(), in)
		srv.Close()
		if !IsUsageUnknown(err) || !out.Usage.Uncertain || out.Outcome == "modified" {
			t.Fatalf("invalid Claude usage was not classified: %+v %v", out, err)
		}
	}
}

func TestClaudeDeniedContextReturnsNativeToolError(t *testing.T) {
	calls, reads := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		usage := map[string]any{"input_tokens": 10, "output_tokens": 4}
		if calls == 1 {
			claudeReply(w, "tool_use", usage, map[string]any{"type": "tool_use", "id": "denied", "name": "read_context", "input": map[string]any{"path": "OneByOne/workspaces/id/llm-settings.json", "startLine": 1, "endLine": 2}})
			return
		}
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		history := req["messages"].([]any)
		result := history[len(history)-1].(map[string]any)["content"].([]any)[0].(map[string]any)
		if result["is_error"] != true || result["tool_use_id"] != "denied" {
			t.Error("tool denial was not encoded as a native error result")
		}
		claudeReply(w, "end_turn", usage, claudeText(`{"outcome":"needs_human","candidateId":"","note":"確認が必要です"}`))
	}))
	defer srv.Close()
	in := testInput(srv.URL)
	in.Config.Provider = "claude"
	in.ReadContext = func(string, int, int) (string, error) { reads++; return "secret", nil }
	out, err := Run(context.Background(), in)
	if err != nil || out.Outcome != "needs_human" || reads != 0 || calls != 2 {
		t.Fatalf("context restriction failed: %+v %v reads=%d calls=%d", out, err, reads, calls)
	}
}

func TestNewProvidersKeepRedirectRetryAndBudgetLimits(t *testing.T) {
	for _, provider := range []string{"openai", "claude"} {
		t.Run(provider, func(t *testing.T) {
			for _, status := range []int{307, 401, 429, 500, 408} {
				var calls, redirected atomic.Int32
				destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { redirected.Add(1) }))
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Location", destination.URL)
					w.Header().Set("Retry-After", "0")
					w.WriteHeader(status)
					_, _ = w.Write([]byte(`{"error":{"type":"test-secret","code":"test-secret","message":"source text and test-secret"}}`))
				}))
				in := testInput(srv.URL)
				in.Config.Provider = provider
				out, err := Run(context.Background(), in)
				srv.Close()
				destination.Close()
				want := int32(1)
				if status == 429 {
					want = 3
				}
				if !IsFatal(err) || strings.Contains(err.Error(), "test-secret") || calls.Load() != want || redirected.Load() != 0 {
					t.Fatalf("provider=%s status=%d err=%v calls=%d redirects=%d", provider, status, err, calls.Load(), redirected.Load())
				}
				if (status == 500 || status == 408) && (!IsUsageUnknown(err) || !out.Usage.Uncertain) {
					t.Error("ambiguous HTTP failure lost its usage classification")
				}
			}
			var calls atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
			defer srv.Close()
			in := testInput(srv.URL)
			in.Config.Provider = provider
			in.Config.MaxCostUSD, in.Config.InputPricePerMillion, in.Config.OutputPricePerMillion = .00001, 10, 30
			if _, err := Run(context.Background(), in); err == nil || !strings.Contains(err.Error(), "cost limit") || calls.Load() != 0 {
				t.Fatalf("provider budget allowed a request: %v %d", err, calls.Load())
			}
		})
	}
}

func TestClaudeCacheWriteDurationsArePricedSeparately(t *testing.T) {
	data := []byte(`{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"OK"}],"usage":{"input_tokens":10,"output_tokens":4,"cache_read_input_tokens":40,"cache_creation_input_tokens":30,"cache_creation":{"ephemeral_5m_input_tokens":20,"ephemeral_1h_input_tokens":10}}}`)
	res, err := decodeClaudeResponse(data)
	if err != nil {
		t.Fatal(err)
	}
	c := &client{cfg: model.Config{Provider: "claude", MaxOutputTokens: 4096, InputPricePerMillion: 1, CachedInputPricePerMillion: .1, OutputPricePerMillion: 2}}
	var usage model.Usage
	if err := c.addUsage(&usage, res.Usage); err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 80 || usage.CachedTokens != 40 || math.Abs(usage.CostUSD-.000067) > 1e-12 {
		t.Fatalf("cache write premium or total input was miscounted: %+v", usage)
	}
}

func TestProviderProtocolErrorsNeverEchoCredentials(t *testing.T) {
	for _, provider := range []string{"azure", "openai", "claude"} {
		for _, field := range []string{"status", "reason", "type", "error"} {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if provider == "claude" {
					claudeReply(w, "test-secret", map[string]any{"input_tokens": 10, "output_tokens": 4}, claudeFinal())
					return
				}
				result := map[string]any{"status": "completed", "output": []any{goodItem()}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 4}}
				switch field {
				case "status":
					result["status"] = "test-secret"
				case "reason":
					result["status"] = "incomplete"
					result["incomplete_details"] = map[string]any{"reason": "test-secret"}
				case "type":
					result["output"] = []any{map[string]any{"type": "test-secret"}}
				case "error":
					result["error"] = map[string]any{"code": "test-secret", "message": "test-secret"}
				}
				_ = json.NewEncoder(w).Encode(result)
			}))
			in := testInput(srv.URL)
			in.Config.Provider = provider
			_, err := Run(context.Background(), in)
			srv.Close()
			if err == nil || strings.Contains(err.Error(), "test-secret") {
				t.Fatalf("%s %s echoed provider data: %v", provider, field, err)
			}
		}
	}
}
