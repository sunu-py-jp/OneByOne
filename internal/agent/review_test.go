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
	"strings"
	"sync/atomic"
	"testing"

	"onebyone/internal/model"
)

func reviewTestInput(endpoint string) ReviewInput {
	before, after := "function save() { return ParcelStore.save(); }\n", "async function save() { return await ParcelClient.write(); }\n"
	hash := func(s string) string { sum := sha256.Sum256([]byte(s)); return hex.EncodeToString(sum[:]) }
	return ReviewInput{
		Config: testInput(endpoint).Config, File: "src/workflows/save.js", Before: before, After: after,
		BaseHash: hash(before), CandidateHash: hash(after),
		Rules: []ReviewRule{
			{ID: "R001", Title: "挙動を保持", Body: "# 変更概要\n公開関数とエラー時の挙動を保持する。", Always: true},
			{ID: "R019", Title: "保存処理", Body: "# 変更概要\nParcelStore.save を ParcelClient.write に変え、非同期で完了を待つ。"},
		},
		Usage:         model.Usage{Turns: 1, InputTokens: 70, OutputTokens: 20},
		BeforeRequest: func(string) error { return nil },
		AfterRequest:  func(model.Usage, error) error { return nil },
	}
}

func reviewWire(in ReviewInput, verdict string) map[string]any {
	assessments := []any{
		map[string]any{"ruleId": "R001", "status": "satisfied", "reason": "公開関数と戻り値を保持しています。"},
		map[string]any{"ruleId": "R019", "status": "satisfied", "reason": "新しい保存APIの完了を待機しています。"},
	}
	issues := []any{}
	if verdict == "needs_changes" {
		assessments[1].(map[string]any)["status"] = "violated"
		assessments[1].(map[string]any)["reason"] = "保存APIへの引数が不足しています。"
		issues = append(issues, map[string]any{
			"ruleId": "R019", "location": "save", "lineBasis": "after", "excerpt": "ParcelClient.write()",
			"reason": "保存対象を渡していません。", "requestedChange": "保存対象を write に渡してください。",
		})
	}
	return map[string]any{"baseHash": in.BaseHash, "candidateHash": in.CandidateHash, "verdict": verdict, "summary": "レビューしました。", "assessments": assessments, "issues": issues}
}

func reviewResponseItem(wire map[string]any) map[string]any {
	return map[string]any{"type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(raw(wire))}}}
}

func TestIndependentReviewAllProvidersFreshContextAndDurableUsage(t *testing.T) {
	for _, provider := range []string{"azure", "openai", "claude"} {
		t.Run(provider, func(t *testing.T) {
			var in ReviewInput
			calls, reservations, settlements := 0, 0, 0
			requestIDs := map[string]bool{}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if reservations != calls || settlements != calls-1 {
					t.Errorf("request %d was sent before durable reservation", calls)
				}
				checkProviderRequest(t, r, provider, "test-secret")
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				for _, forbidden := range []string{"tools", "tool_choice", "include", "previous_response_id", "conversation"} {
					if _, exists := req[forbidden]; exists {
						t.Errorf("review request contains %s", forbidden)
					}
				}
				if req["model"] != in.Config.Deployment {
					t.Error("review changed the configured model")
				}
				historyKey := "input"
				if provider == "claude" {
					historyKey = "messages"
					if req["input"] != nil || req["store"] != nil || req["text"] != nil || req["max_tokens"] != float64(4096) {
						t.Error("review used Responses protocol fields for Claude")
					}
					format := req["output_config"].(map[string]any)["format"].(map[string]any)
					if format["type"] != "json_schema" || format["schema"] == nil || format["name"] != nil {
						t.Error("Claude review omitted native structured-output schema")
					}
				} else {
					if req["messages"] != nil || req["store"] != false || req["max_output_tokens"] != float64(4096) {
						t.Error("Responses review omitted statelessness or output limit")
					}
					format := req["text"].(map[string]any)["format"].(map[string]any)
					if format["name"] != "independent_review" || format["strict"] != true || format["schema"] == nil {
						t.Error("Responses review omitted strict review schema")
					}
				}
				history := req[historyKey].([]any)
				if len(history) != 1 || history[0].(map[string]any)["role"] != "user" {
					t.Fatalf("independent review inherited a conversation: %v", history)
				}
				var payload map[string]any
				if err := json.Unmarshal([]byte(history[0].(map[string]any)["content"].(string)), &payload); err != nil {
					t.Fatal(err)
				}
				if len(payload) != 6 || payload["file"] != in.File || payload["before"] != in.Before || payload["after"] != in.After || payload["baseHash"] != in.BaseHash || payload["candidateHash"] != in.CandidateHash {
					t.Errorf("review payload contains missing or extra context: %v", payload)
				}
				rules := payload["rules"].([]any)
				if len(rules) != 2 {
					t.Fatal("review did not receive all required rules")
				}
				for i, row := range rules {
					rule := row.(map[string]any)
					if len(rule) != 4 || rule["id"] != in.Rules[i].ID || rule["title"] != in.Rules[i].Title || rule["body"] != in.Rules[i].Body || rule["always"] != in.Rules[i].Always {
						t.Error("review rule body was summarized or included editor claims")
					}
				}
				wire := reviewWire(in, "passed")
				if provider == "claude" {
					claudeReply(w, "end_turn", map[string]any{"input_tokens": 70, "cache_read_input_tokens": 30, "output_tokens": 40}, claudeText(string(raw(wire))))
				} else {
					respond(w, reviewResponseItem(wire))
				}
			}))
			defer srv.Close()
			in = reviewTestInput(srv.URL)
			in.Config.Provider = provider
			in.BeforeRequest = func(id string) error {
				if id == "" || requestIDs[id] {
					t.Error("pending request has empty or repeated ID")
				}
				requestIDs[id] = true
				reservations++
				return nil
			}
			in.AfterRequest = func(usage model.Usage, requestErr error) error {
				settlements++
				if requestErr != nil || usage.Uncertain || usage.Turns != 1 || usage.InputTokens != 100 || usage.CachedTokens != 30 || usage.OutputTokens != 40 {
					t.Errorf("incorrect settled review delta: %+v %v", usage, requestErr)
				}
				return nil
			}
			// Even consecutive reviews on the same file start with one user message.
			for i := 0; i < 2; i++ {
				out, err := Review(context.Background(), in)
				if err != nil || out.Verdict != "passed" || out.ID != "" || out.CandidateID != "" || out.PlanRevision != 0 || out.Usage.Turns != 1 || out.Usage.InputTokens != 100 || len(out.Assessments) != 2 {
					t.Fatalf("independent review failed: %+v %v", out, err)
				}
			}
			if calls != 2 || reservations != 2 || settlements != 2 {
				t.Fatalf("review lifecycle mismatch: %d %d %d", calls, reservations, settlements)
			}
		})
	}
}

func TestIndependentReviewRejectsInvalidVerdicts(t *testing.T) {
	in := reviewTestInput("http://127.0.0.1:1")
	tests := []struct {
		name    string
		verdict string
		mutate  func(map[string]any)
	}{
		{"wrong base hash", "passed", func(w map[string]any) { w["baseHash"] = strings.Repeat("0", 64) }},
		{"wrong candidate hash", "passed", func(w map[string]any) { w["candidateHash"] = strings.Repeat("0", 64) }},
		{"model supplied identity", "passed", func(w map[string]any) { w["id"] = "self-approved" }},
		{"missing field", "passed", func(w map[string]any) { delete(w, "issues") }},
		{"null array", "passed", func(w map[string]any) { w["issues"] = nil }},
		{"missing rule assessment", "passed", func(w map[string]any) { w["assessments"] = w["assessments"].([]any)[:1] }},
		{"duplicate rule assessment", "passed", func(w map[string]any) { w["assessments"].([]any)[1] = w["assessments"].([]any)[0] }},
		{"unknown assessed rule", "passed", func(w map[string]any) { w["assessments"].([]any)[0].(map[string]any)["ruleId"] = "R999" }},
		{"unsupported assessment", "passed", func(w map[string]any) { w["assessments"].([]any)[0].(map[string]any)["status"] = "done" }},
		{"missing common rule assessment", "passed", func(w map[string]any) { w["assessments"] = w["assessments"].([]any)[1:] }},
		{"inapplicable common rule without reason", "passed", func(w map[string]any) {
			w["assessments"].([]any)[0].(map[string]any)["status"] = "not_applicable"
			w["assessments"].([]any)[0].(map[string]any)["reason"] = " "
		}},
		{"missing assessment reason", "passed", func(w map[string]any) { delete(w["assessments"].([]any)[0].(map[string]any), "reason") }},
		{"passed unresolved rule", "passed", func(w map[string]any) { w["assessments"].([]any)[0].(map[string]any)["status"] = "needs_human" }},
		{"passed with issues", "needs_changes", func(w map[string]any) { w["verdict"] = "passed" }},
		{"needs changes without issues", "passed", func(w map[string]any) { w["verdict"] = "needs_changes" }},
		{"violated without issue", "passed", func(w map[string]any) { w["assessments"].([]any)[0].(map[string]any)["status"] = "violated" }},
		{"unknown issue rule", "needs_changes", func(w map[string]any) { w["issues"].([]any)[0].(map[string]any)["ruleId"] = "R999" }},
		{"issue for satisfied rule", "needs_changes", func(w map[string]any) { w["issues"].([]any)[0].(map[string]any)["ruleId"] = "R001" }},
		{"fabricated excerpt", "needs_changes", func(w map[string]any) { w["issues"].([]any)[0].(map[string]any)["excerpt"] = "totallyMadeUp()" }},
		{"wrong source side", "needs_changes", func(w map[string]any) { w["issues"].([]any)[0].(map[string]any)["lineBasis"] = "before" }},
		{"unsupported source side", "needs_changes", func(w map[string]any) { w["issues"].([]any)[0].(map[string]any)["lineBasis"] = "candidate" }},
		{"missing general issue ID", "needs_changes", func(w map[string]any) { delete(w["issues"].([]any)[0].(map[string]any), "ruleId") }},
		{"null general issue ID", "needs_changes", func(w map[string]any) { w["issues"].([]any)[0].(map[string]any)["ruleId"] = nil }},
		{"empty requested change", "needs_changes", func(w map[string]any) { w["issues"].([]any)[0].(map[string]any)["requestedChange"] = " " }},
		{"needs changes with unresolved rule", "needs_changes", func(w map[string]any) { w["assessments"].([]any)[0].(map[string]any)["status"] = "needs_human" }},
		{"oversized summary", "passed", func(w map[string]any) { w["summary"] = strings.Repeat("x", 8193) }},
		{"unknown verdict", "passed", func(w map[string]any) { w["verdict"] = "approved" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wire := reviewWire(in, tc.verdict)
			tc.mutate(wire)
			if result, err := parseIndependentReview(string(raw(wire)), in); err == nil || result.Verdict != "" {
				t.Fatalf("invalid review accepted: %+v %v", result, err)
			}
		})
	}
	for _, text := range []string{"{}", "null", "```json\n{}\n```", string(raw(reviewWire(in, "passed"))) + "{}", strings.Repeat("x", maxToolBytes+1)} {
		if _, err := parseIndependentReview(text, in); err == nil {
			t.Error("malformed review accepted")
		}
	}
}

func TestIndependentReviewAcceptsGroundedChangesAndUncertainty(t *testing.T) {
	in := reviewTestInput("http://127.0.0.1:1")
	for _, verdict := range []string{"passed", "needs_changes", "needs_human"} {
		wire := reviewWire(in, verdict)
		if verdict == "needs_human" {
			wire["summary"] = "呼び出し側が Promise を扱えるか確認する必要があります。"
			wire["assessments"].([]any)[0].(map[string]any)["status"] = "needs_human"
		}
		out, err := parseIndependentReview(string(raw(wire)), in)
		if err != nil || out.Verdict != verdict {
			t.Fatalf("valid review %s rejected: %+v %v", verdict, out, err)
		}
	}
	// A behavior regression may be outside the supplied rule IDs. It must still
	// have an exact source excerpt, rather than inventing a rule or code location.
	wire := reviewWire(in, "needs_changes")
	wire["assessments"].([]any)[1].(map[string]any)["status"] = "satisfied"
	wire["issues"].([]any)[0].(map[string]any)["ruleId"] = ""
	if _, err := parseIndependentReview(string(raw(wire)), in); err != nil {
		t.Fatalf("grounded general regression rejected: %v", err)
	}
}

func TestIndependentReviewCommonRuleMayBeExplicitlyInapplicable(t *testing.T) {
	in := reviewTestInput("http://127.0.0.1:1")
	in.Rules[0].Title = "var宣言の整理"
	in.Rules[0].Body = "var宣言をスコープと再代入の有無に応じてletまたはconstに変更する。"
	for _, verdict := range []string{"passed", "needs_changes", "needs_human"} {
		t.Run(verdict, func(t *testing.T) {
			wire := reviewWire(in, verdict)
			assessment := wire["assessments"].([]any)[0].(map[string]any)
			assessment["status"] = "not_applicable"
			assessment["reason"] = "変更前・変更後のどちらにもvarによる変数宣言が存在しません。"
			if verdict == "needs_human" {
				wire["assessments"].([]any)[1].(map[string]any)["status"] = "needs_human"
				wire["assessments"].([]any)[1].(map[string]any)["reason"] = "呼び出し元がPromiseに対応しているか、このファイルから判断できません。"
			}
			out, err := parseIndependentReview(string(raw(wire)), in)
			if err != nil || out.Verdict != verdict || len(out.Assessments) != 2 || out.Assessments[0].Status != "not_applicable" || out.Assessments[0].Reason != assessment["reason"] {
				t.Fatalf("explicit common assessment changed the review outcome: %+v %v", out, err)
			}
		})
	}
	// An inapplicable common rule must not hide another rule's unresolved hold.
	wire := reviewWire(in, "passed")
	wire["assessments"].([]any)[0].(map[string]any)["status"] = "not_applicable"
	wire["assessments"].([]any)[1].(map[string]any)["status"] = "needs_human"
	if _, err := parseIndependentReview(string(raw(wire)), in); err == nil {
		t.Fatal("inapplicable common rule allowed a passed verdict with an unresolved rule")
	}
}

func TestIndependentReviewTransportAcceptsInapplicableCommonRule(t *testing.T) {
	var in ReviewInput
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		wire := reviewWire(in, "passed")
		common := wire["assessments"].([]any)[0].(map[string]any)
		common["status"] = "not_applicable"
		common["reason"] = "変更前・変更後にvar宣言がなく、置換する箇所がありません。"
		respond(w, reviewResponseItem(wire))
	}))
	defer srv.Close()
	in = reviewTestInput(srv.URL)
	in.Rules[0] = ReviewRule{ID: "R001", Title: "var宣言の整理", Body: "var宣言を再代入に応じてletまたはconstへ変更する。", Always: true}
	out, err := Review(context.Background(), in)
	if err != nil || calls != 1 || out.Verdict != "passed" || len(out.Assessments) != 2 || out.Assessments[0].Status != "not_applicable" || out.Usage.Turns != 1 {
		t.Fatalf("valid common-rule assessment failed in provider transport: %+v %v calls=%d", out, err, calls)
	}
}

func TestIndependentReviewReservationFailureNeverSends(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	in := reviewTestInput(srv.URL)
	in.BeforeRequest = func(string) error { return errors.New("disk unavailable") }
	in.AfterRequest = func(model.Usage, error) error { t.Error("settled request that was never sent"); return nil }
	result, err := Review(context.Background(), in)
	if err == nil || !IsFatal(err) || calls.Load() != 0 || result.Usage.Turns != 0 {
		t.Fatalf("uncheckpointed review was sent: %+v %v calls=%d", result, err, calls.Load())
	}
}

func TestIndependentReviewSettlesUsageBeforeMalformedVerdictRejection(t *testing.T) {
	settlements := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respond(w, goodItem()) }))
	defer srv.Close()
	in := reviewTestInput(srv.URL)
	in.AfterRequest = func(usage model.Usage, requestErr error) error {
		settlements++
		if requestErr != nil || usage.InputTokens != 100 || usage.Turns != 1 {
			t.Errorf("valid provider usage was not settled before parsing: %+v %v", usage, requestErr)
		}
		return nil
	}
	out, err := Review(context.Background(), in)
	if err == nil || out.Verdict != "" || out.Usage.InputTokens != 100 || settlements != 1 {
		t.Fatalf("malformed verdict escaped accounting: %+v %v settlements=%d", out, err, settlements)
	}
}

func TestIndependentReviewSettlementFailureCannotApprove(t *testing.T) {
	var in ReviewInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { respond(w, reviewResponseItem(reviewWire(in, "passed"))) }))
	defer srv.Close()
	in = reviewTestInput(srv.URL)
	settlements := 0
	in.AfterRequest = func(model.Usage, error) error { settlements++; return errors.New("disk unavailable") }
	out, err := Review(context.Background(), in)
	if err == nil || !IsFatal(err) || out.Verdict != "" || out.Usage.InputTokens != 100 || settlements != 1 {
		t.Fatalf("review approved without settled usage: %+v %v settlements=%d", out, err, settlements)
	}
}

func TestIndependentReviewAmbiguousTransportNeverReplays(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		conn, _, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Error(err)
			return
		}
		conn.Close()
	}))
	defer srv.Close()
	in := reviewTestInput(srv.URL)
	settlements := 0
	in.AfterRequest = func(usage model.Usage, requestErr error) error {
		settlements++
		if !usage.Uncertain || usage.Turns != 1 || !IsUsageUnknown(requestErr) {
			t.Errorf("ambiguous usage not persisted: %+v %v", usage, requestErr)
		}
		return nil
	}
	out, err := Review(context.Background(), in)
	if !IsUsageUnknown(err) || !out.Usage.Uncertain || out.Verdict != "" || calls.Load() != 1 || settlements != 1 {
		t.Fatalf("ambiguous review replayed or approved: %+v %v calls=%d settlements=%d", out, err, calls.Load(), settlements)
	}
}

func TestIndependentReviewUnknownUsageCannotApprove(t *testing.T) {
	var in ReviewInput
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{reviewResponseItem(reviewWire(in, "passed"))}})
	}))
	defer srv.Close()
	in = reviewTestInput(srv.URL)
	settlements := 0
	in.AfterRequest = func(usage model.Usage, requestErr error) error {
		settlements++
		if !usage.Uncertain || !IsUsageUnknown(requestErr) {
			t.Error("missing usage did not become uncertain")
		}
		return nil
	}
	out, err := Review(context.Background(), in)
	if !IsUsageUnknown(err) || out.Verdict != "" || !out.Usage.Uncertain || settlements != 1 {
		t.Fatalf("review approved with missing usage: %+v %v", out, err)
	}
}

func TestIndependentReviewRejectsUnexpectedToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		respond(w, testCall("read", "read_rule", map[string]any{"id": "R019"}))
	}))
	defer srv.Close()
	out, err := Review(context.Background(), reviewTestInput(srv.URL))
	if err == nil || out.Verdict != "" || !strings.Contains(err.Error(), "cannot call tools") || out.Usage.Turns != 1 {
		t.Fatalf("reviewer tool call was accepted: %+v %v", out, err)
	}
}

func TestIndependentReviewLimitsPreventRequests(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1) }))
	defer srv.Close()
	for _, tc := range []struct {
		name   string
		mutate func(*ReviewInput)
	}{
		{"shared turns", func(in *ReviewInput) { in.Usage.Turns = in.Config.MaxTurns }},
		{"shared cost", func(in *ReviewInput) {
			in.Config.InputPricePerMillion, in.Config.OutputPricePerMillion, in.Config.MaxCostUSD = 1, 1, .0001
		}},
		{"uncertain priced budget", func(in *ReviewInput) {
			in.Config.InputPricePerMillion, in.Config.OutputPricePerMillion, in.Config.MaxCostUSD = 1, 1, 1
			in.Usage.Uncertain = true
		}},
		{"nonfinite prior cost", func(in *ReviewInput) { in.Usage.CostUSD = math.NaN() }},
		{"invalid original hash", func(in *ReviewInput) { in.BaseHash = "old-hash" }},
		{"uppercase candidate hash", func(in *ReviewInput) { in.CandidateHash = strings.ToUpper(in.CandidateHash) }},
		{"outside source", func(in *ReviewInput) { in.File = "../secret" }},
		{"oversized source", func(in *ReviewInput) { in.Before = strings.Repeat("x", (512<<10)+1) }},
		{"nonUTF8 source", func(in *ReviewInput) { in.After = string([]byte{0xff}) }},
		{"missing rules", func(in *ReviewInput) { in.Rules = nil }},
		{"duplicate rules", func(in *ReviewInput) { in.Rules[1] = in.Rules[0] }},
		{"empty rule", func(in *ReviewInput) { in.Rules[0].Body = " " }},
		{"oversized rule", func(in *ReviewInput) { in.Rules[0].Body = strings.Repeat("x", maxToolBytes+1) }},
		{"oversized combined rules", func(in *ReviewInput) {
			in.Rules = nil
			for i := 0; i < 6; i++ {
				in.Rules = append(in.Rules, ReviewRule{ID: fmt.Sprintf("R%03d", i), Body: strings.Repeat("x", maxToolBytes)})
			}
		}},
		{"oversized serialized request", func(in *ReviewInput) {
			in.Before, in.After = strings.Repeat("\x00", 512<<10), strings.Repeat("\x00", 512<<10)
		}},
		{"missing persistence", func(in *ReviewInput) { in.AfterRequest = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := reviewTestInput(srv.URL)
			tc.mutate(&in)
			in.BeforeRequest = func(string) error { t.Error("invalid review reserved a request"); return nil }
			if out, err := Review(context.Background(), in); err == nil || out.Usage.Turns != 0 {
				t.Fatalf("invalid or exhausted review sent: %+v %v", out, err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatalf("limits allowed %d requests", calls.Load())
	}
}
