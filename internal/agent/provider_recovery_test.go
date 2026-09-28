package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"onebyone/internal/model"
)

func TestProviderRetryContinuesPastThreeTransientFailures(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 4 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		if n == 5 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		respond(w, goodItem())
	}))
	defer srv.Close()
	c, err := newClient(testInput(srv.URL).Config)
	if err != nil {
		t.Fatal(err)
	}
	defer c.http.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	response, err := c.request(ctx, []byte(`{}`), nil)
	if err != nil || calls.Load() != 6 || response.Usage == nil || !response.Usage.Uncertain || response.Usage.Requests != 6 {
		t.Fatalf("request %+v error %v calls %d", response, err, calls.Load())
	}
	usage := model.Usage{}
	if err := c.addUsage(&usage, response.Usage); err != nil || !usage.Uncertain || usage.Turns != 6 {
		t.Fatalf("usage %+v: %v", usage, err)
	}
}
func TestProviderRetryHonorsLongWaitAndCancelsPromptly(t *testing.T) {
	if delay, err := retryDelay("120", 0); err != nil || delay != 120*time.Second {
		t.Fatalf("delay=%s err=%v", delay, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	if err := waitProviderRetry(ctx, "rate limit", "120", 0, nil); !errors.Is(err, context.Canceled) || time.Since(started) > time.Second {
		t.Fatalf("cancel %v after %s", err, time.Since(started))
	}
}
func TestProviderAuthenticationFailureDoesNotRetry(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		io.WriteString(w, `{"error":{"code":"invalid_api_key","message":"test-secret"}}`)
	}))
	defer srv.Close()
	c, err := newClient(testInput(srv.URL).Config)
	if err != nil {
		t.Fatal(err)
	}
	_, err = c.request(context.Background(), []byte(`{}`), nil)
	if !IsFatal(err) || calls.Load() != 1 || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("error %v calls %d", err, calls.Load())
	}
}
func TestResponsesRequestsOmitOutputCap(t *testing.T) {
	for _, provider := range []string{"azure", "openai"} {
		c := &client{cfg: model.Config{Provider: provider, Deployment: "deployed"}}
		body, err := c.migrationRequest([]json.RawMessage{}, "instructions")
		if err != nil {
			t.Fatal(err)
		}
		var parsed map[string]any
		json.Unmarshal(body, &parsed)
		if _, exists := parsed["max_output_tokens"]; exists {
			t.Fatalf("%s editor has output cap", provider)
		}
		body, err = c.reviewRequest("{}")
		if err != nil {
			t.Fatal(err)
		}
		json.Unmarshal(body, &parsed)
		if _, exists := parsed["max_output_tokens"]; exists {
			t.Fatalf("%s review has output cap", provider)
		}
	}
}
func TestClaudeOutputLimitAdaptsToProvider(t *testing.T) {
	c := &client{cfg: model.Config{Provider: "claude"}}
	if c.outputLimit() != 8192 || !c.growOutputLimit() || c.outputLimit() != 16384 {
		t.Fatal("growth failed")
	}
	if !c.learnOutputCeiling("max_tokens: 16384 > 12000, which is the maximum allowed number of output tokens") || c.outputLimit() != 12000 || c.growOutputLimit() {
		t.Fatal("provider ceiling ignored")
	}
	response, err := decodeClaudeResponse([]byte(`{"type":"message","role":"assistant","stop_reason":"max_tokens","usage":{"input_tokens":3,"output_tokens":5},"content":[]}`))
	if err != nil || !IsOutputLimit(completed(response)) {
		t.Fatalf("truncation %v %v", err, completed(response))
	}
}
func TestIndependentReviewRecoversTruncationWithRecordedFindings(t *testing.T) {
	var in ReviewInput
	calls, saves, settled := 0, 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		switch calls {
		case 1:
			json.NewEncoder(w).Encode(map[string]any{"status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}, "usage": map[string]any{"input_tokens": 10, "output_tokens": 20}, "output": []any{}})
		case 2:
			respond(w, testCall("record-1", "record_review", map[string]any{"assessments": []any{map[string]any{"ruleId": in.Rules[0].ID, "status": "satisfied", "reason": "元の契約を維持している"}}, "issues": []any{}, "holdAssessments": []any{}, "removeIssueIds": []any{}}))
		case 3:
			respond(w, testCall("record-2", "record_review", map[string]any{"assessments": []any{map[string]any{"ruleId": in.Rules[1].ID, "status": "satisfied", "reason": "APIの置換と呼び出し順序を確認した"}}, "issues": []any{}, "holdAssessments": []any{}, "removeIssueIds": []any{}}))
		default:
			respond(w, reviewResponseItem(map[string]any{"verdict": "passed", "summary": "ファイル全体と全ルールの関係を確認しました"}))
		}
	}))
	defer srv.Close()
	in = reviewTestInput(srv.URL)
	in.SaveProgress = func(progress model.IndependentReview) error {
		saves++
		if len(progress.Assessments) != saves {
			t.Errorf("lost progress %+v", progress)
		}
		return nil
	}
	in.AfterRequest = func(usage model.Usage, err error) error { settled++; return err }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := Review(ctx, in)
	if err != nil || out.Verdict != "passed" || len(out.Assessments) != 2 || calls != 4 || saves != 2 || settled != 4 || out.Usage.Turns != 4 {
		t.Fatalf("result %+v err %v calls=%d saved=%d settled=%d", out, err, calls, saves, settled)
	}
}
func TestReviewRecoveryDoesNotApproveUnreadPagedSource(t *testing.T) {
	in := reviewTestInput("http://127.0.0.1:1")
	state := newReviewRecoveryState()
	if state.completeRead(in) {
		t.Fatal("unread source was complete")
	}
	for _, source := range []string{"before", "after"} {
		if _, err := state.execute(in, functionCall{Name: "read_review_source", Arguments: string(raw(map[string]any{"source": source, "offset": 0}))}); err != nil {
			t.Fatal(err)
		}
	}
	if state.completeRead(in) {
		t.Fatal("unread rules were complete")
	}
	for _, rule := range in.Rules {
		if _, err := state.execute(in, functionCall{Name: "read_review_rule", Arguments: string(raw(map[string]any{"ruleId": rule.ID, "offset": 0}))}); err != nil {
			t.Fatal(err)
		}
	}
	if !state.completeRead(in) {
		t.Fatal("complete reads not recognized")
	}
}
func TestReviewAcceptsMoreThanOldRuleAndIssueCaps(t *testing.T) {
	in := reviewTestInput("http://127.0.0.1:1")
	in.Rules = nil
	for i := 0; i < 300; i++ {
		in.Rules = append(in.Rules, ReviewRule{ID: strings.Repeat("R", i+1), Title: "rule", Body: "body", Always: true})
	}
	// IDs have separate identity syntax restrictions; use ordinary IDs instead.
	for i := range in.Rules {
		in.Rules[i].ID = fmt.Sprintf("R%03d", i)
	}
	if err := validateReviewInput(in, in.Config); err != nil {
		t.Fatal(err)
	}
	wire := reviewWire(in, "passed")
	assessments := []any{}
	for _, rule := range in.Rules {
		assessments = append(assessments, map[string]any{"ruleId": rule.ID, "status": "satisfied", "reason": "確認済み"})
	}
	wire["assessments"] = assessments
	if _, err := parseIndependentReview(string(raw(wire)), in); err != nil {
		t.Fatal(err)
	}
}

func TestIndependentReviewContextRecoveryRequiresWholeSourceAndEveryRule(t *testing.T) {
	var in ReviewInput
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		switch calls {
		case 1:
			w.WriteHeader(http.StatusBadRequest)
			io.WriteString(w, `{"error":{"code":"context_length_exceeded","message":"too long"}}`)
		case 2:
			respond(w, testCall("before", "read_review_source", map[string]any{"source": "before", "offset": 0}))
		case 3:
			respond(w, testCall("after", "read_review_source", map[string]any{"source": "after", "offset": 0}))
		case 4:
			respond(w, testCall("rule1", "read_review_rule", map[string]any{"ruleId": in.Rules[0].ID, "offset": 0}))
		case 5:
			respond(w, testCall("findings", "record_review", map[string]any{"assessments": []any{map[string]any{"ruleId": in.Rules[0].ID, "status": "satisfied", "reason": "公開契約を保持"}, map[string]any{"ruleId": in.Rules[1].ID, "status": "satisfied", "reason": "新APIの利用を確認"}}, "issues": []any{}, "holdAssessments": []any{}, "removeIssueIds": []any{}}))
		case 6:
			respond(w, reviewResponseItem(map[string]any{"verdict": "passed", "summary": "全体の確認"}))
		case 7:
			if !strings.Contains(string(raw(body)), "Read every before/after source") {
				t.Error("premature final did not return missing-read feedback")
			}
			respond(w, testCall("rule2", "read_review_rule", map[string]any{"ruleId": in.Rules[1].ID, "offset": 0}))
		default:
			respond(w, reviewResponseItem(map[string]any{"verdict": "passed", "summary": "両方のコード全体と全ルールの相互作用を確認"}))
		}
	}))
	defer srv.Close()
	in = reviewTestInput(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := Review(ctx, in)
	if err != nil || out.Verdict != "passed" || calls != 8 {
		t.Fatalf("review=%+v error=%v calls=%d", out, err, calls)
	}
}

func TestIndependentReviewRecordedProgressResumesOnlyForSameCandidate(t *testing.T) {
	in := reviewTestInput("http://127.0.0.1:1")
	in.PriorProgress = &model.IndependentReview{BaseHash: "different", CandidateHash: in.CandidateHash}
	if err := validateReviewInput(in, in.Config); err == nil {
		t.Fatal("mismatched saved review accepted")
	}
	in.PriorProgress = &model.IndependentReview{BaseHash: in.BaseHash, CandidateHash: in.CandidateHash, Assessments: []model.ReviewAssessment{{RuleID: "R001", Status: "satisfied", Reason: "確認済み"}}}
	saved := newReviewRecoveryState()
	saved.restore(in.PriorProgress)
	if len(saved.result(in).Assessments) != 1 || saved.completeRead(in) {
		t.Fatal("resumed findings or required source reads are wrong")
	}
}
