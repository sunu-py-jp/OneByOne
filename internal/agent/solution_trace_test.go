package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"onebyone/internal/model"
)

func TestSolutionTraceRecordsToolResultsAndIndependentReview(t *testing.T) {
	in := solutionFixture("", "azure")
	actions := []solutionTestAction{solutionRead("read_file", map[string]any{"file": "missing.vb", "version": "original", "offset": 0})}
	actions = append(actions, solutionReadActions(in, false)...)
	actions = append(actions, solutionStage("App/A.vb", "LegacySave", "ModernSave"), solutionStage("App/B.vb", "LegacySave", "ModernSave"), solutionDone("modified", "宣言と呼び出しを修正しました。"))
	actions = append(actions, solutionPassActions(in)...)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests >= len(actions) {
			t.Error("unexpected request")
			http.Error(w, "unexpected request", 400)
			return
		}
		action := actions[requests]
		requests++
		if requests == 1 {
			respond(w, map[string]any{"type": "reasoning", "encrypted_content": "private-reasoning-marker", "summary": []any{}}, testCall("S1", action.name, action.args))
			return
		}
		solutionReply(w, "azure", requests, action)
	}))
	defer srv.Close()
	in.Config.Endpoint = srv.URL
	var events []model.AgentTraceEvent
	in.Trace = func(event model.AgentTraceEvent) error { events = append(events, event); return nil }
	out, err := RunSolution(context.Background(), in)
	if err != nil || out.Status != "modified" || len(out.Files) != 2 {
		t.Fatalf("proposal = %+v, error = %v", out, err)
	}
	starts := map[string]model.AgentTraceEvent{}
	completed := map[string]bool{}
	sessions := map[int]string{}
	decisions := map[string]string{}
	requestsDone, failedTools, readSources := 0, 0, 0
	for _, event := range events {
		if event.Key == "" || event.Session < 1 || event.Phase == "" {
			t.Fatalf("missing event identity: %+v", event)
		}
		if _, err := time.Parse(time.RFC3339Nano, event.StartedAt); err != nil {
			t.Fatalf("invalid start: %+v", event)
		}
		if event.Status == "running" {
			if _, exists := starts[event.Key]; exists || completed[event.Key] {
				t.Fatalf("reused event key: %s", event.Key)
			}
			starts[event.Key] = event
			if event.Kind == "session" {
				sessions[event.Session] = event.Phase
			}
			continue
		}
		if completed[event.Key] {
			t.Fatalf("duplicate completion: %s", event.Key)
		}
		completed[event.Key] = true
		if _, err := time.Parse(time.RFC3339Nano, event.FinishedAt); err != nil {
			t.Fatalf("invalid finish: %+v", event)
		}
		if event.Kind == "tool" || event.Kind == "request" || event.Kind == "session" {
			start, ok := starts[event.Key]
			if !ok || start.StartedAt != event.StartedAt || start.Arguments != event.Arguments || start.Turn != event.Turn || start.Session != event.Session || start.Phase != event.Phase {
				t.Fatalf("completion lost its starting identity: %+v", event)
			}
		}
		switch event.Kind {
		case "request":
			requestsDone++
			if event.Turn < 1 || event.Arguments != "" || event.Output != "" || event.Usage.InputTokens != 100 || event.Usage.Turns != 1 {
				t.Fatalf("request should store usage, never provider payload: %+v", event)
			}
		case "tool":
			if event.Arguments == "" || event.CallID == "" || event.Output == "" || event.Turn < 1 {
				t.Fatalf("missing tool arguments/result: %+v", event)
			}
			if event.Status == "error" {
				failedTools++
				if event.Tool != "read_file" || !strings.HasPrefix(event.Output, "Tool error: ") {
					t.Fatalf("failed tool not recorded: %+v", event)
				}
			}
			if event.Tool == "read_file" && event.Status == "completed" {
				var args struct{ File, Version string }
				var result struct{ Content string }
				if json.Unmarshal([]byte(event.Arguments), &args) != nil || json.Unmarshal([]byte(event.Output), &result) != nil {
					t.Fatal("invalid saved tool payload")
				}
				if args.Version == "original" && result.Content != in.Files[args.File] {
					t.Fatalf("tool return was truncated or changed: %+v", event)
				}
				readSources++
			}
		case "decision":
			var decision struct{ Status, Note string }
			if json.Unmarshal([]byte(event.Output), &decision) != nil || decision.Note == "" || event.Status != "completed" {
				t.Fatalf("missing public decision: %+v", event)
			}
			decisions[event.Phase] = decision.Status
		}
	}
	for key := range starts {
		if !completed[key] {
			t.Errorf("started event %s never finished", key)
		}
	}
	if requestsDone != requests || failedTools != 1 || readSources != 12 || sessions[1] != "repair" || sessions[2] != "review" || len(sessions) != 2 || decisions["repair"] != "modified" || decisions["review"] != "passed" {
		t.Fatalf("incomplete activity: requests=%d/%d failures=%d reads=%d sessions=%v decisions=%v", requestsDone, requests, failedTools, readSources, sessions, decisions)
	}
	if saved := string(raw(events)); strings.Contains(saved, "private-reasoning-marker") || strings.Contains(saved, in.Config.Credential) || strings.Contains(saved, "Authorization") {
		t.Fatal("private provider data was recorded")
	}
}

func TestSolutionTraceFailureBlocksToolExecution(t *testing.T) {
	failure := errors.New("activity disk unavailable")
	callbacks := 0
	trace := &solutionTrace{callback: func(event model.AgentTraceEvent) error {
		callbacks++
		if event.Kind == "tool" {
			return failure
		}
		return nil
	}}
	activity, err := trace.beginSession(false)
	if err != nil {
		t.Fatal(err)
	}
	s := newSolutionSession(solutionFixture("", "azure"), false)
	action := solutionStage("App/A.vb", "LegacySave", "ModernSave")
	call := functionCall{CallID: "blocked", Name: action.name, Arguments: string(raw(action.args))}
	for range 2 {
		_, _, err = activity.execute(s, call, false)
		if !errors.Is(err, failure) || !IsFatal(err) || len(s.staged) != 0 {
			t.Fatalf("unrecorded tool executed or persistence error lost: staged=%v err=%v", s.staged, err)
		}
	}
	if callbacks != 2 {
		t.Fatalf("recorder continued after failure: %d callbacks", callbacks)
	}
}

func TestSolutionTracePersistenceErrorsStopRun(t *testing.T) {
	for _, target := range []struct {
		kind, status string
		requests     int
	}{
		{"session", "running", 0}, {"request", "running", 0}, {"request", "completed", 1},
		{"tool", "running", 1}, {"tool", "completed", 1}, {"decision", "completed", 1}, {"session", "completed", 1},
	} {
		t.Run(target.kind+"_"+target.status, func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				action := solutionDone("needs_human", "外部参照の確認が必要です。")
				if target.kind == "tool" {
					action = solutionRead("read_manifest", map[string]any{"offset": 0})
				}
				solutionReply(w, "azure", requests, action)
			}))
			defer srv.Close()
			in := solutionFixture(srv.URL, "azure")
			failure := errors.New("activity write failed")
			in.Trace = func(event model.AgentTraceEvent) error {
				if event.Kind == target.kind && event.Status == target.status {
					return failure
				}
				return nil
			}
			out, err := RunSolution(context.Background(), in)
			if !errors.Is(err, failure) || !IsFatal(err) || requests != target.requests || out.Status != "" || len(out.Files) != 0 {
				t.Fatalf("run continued without recording: %+v error=%v requests=%d", out, err, requests)
			}
			if target.requests > 0 && out.Usage.InputTokens != 100 {
				t.Fatalf("usage lost after trace failure: %+v", out.Usage)
			}
		})
	}
}

func TestSolutionTraceRecordsContextRecoveryAndRequestFailure(t *testing.T) {
	for _, failContext := range []bool{false, true} {
		t.Run(fmt.Sprint(failContext), func(t *testing.T) {
			requests := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if requests == 1 {
					json.NewEncoder(w).Encode(map[string]any{"status": "incomplete", "incomplete_details": map[string]any{"reason": "max_output_tokens"}, "usage": map[string]any{"input_tokens": 100, "output_tokens": 40}, "output": []any{}})
					return
				}
				solutionReply(w, "azure", requests, solutionDone("needs_human", "外部参照の確認が必要です。"))
			}))
			defer srv.Close()
			in := solutionFixture(srv.URL, "azure")
			failure := errors.New("context activity unavailable")
			var events []model.AgentTraceEvent
			in.Trace = func(event model.AgentTraceEvent) error {
				events = append(events, event)
				if failContext && event.Kind == "context" {
					return failure
				}
				return nil
			}
			out, err := RunSolution(context.Background(), in)
			if failContext {
				if !errors.Is(err, failure) || requests != 1 || out.Status != "" {
					t.Fatalf("context failure ignored: %+v %v requests=%d", out, err, requests)
				}
			} else if err != nil || requests != 2 || out.Status != "needs_human" {
				t.Fatalf("context recovery failed: %+v %v requests=%d", out, err, requests)
			}
			requestErrors, contexts := 0, 0
			for _, event := range events {
				if event.Kind == "request" && event.Status == "error" {
					requestErrors++
					if event.Output == "" || event.Usage.InputTokens != 100 {
						t.Fatalf("request failure missing: %+v", event)
					}
				}
				if event.Kind == "context" {
					contexts++
					if event.Session != 1 || event.Turn != 1 || !strings.Contains(event.Output, "saved state remains available") {
						t.Fatalf("context reason missing: %+v", event)
					}
				}
			}
			if requestErrors != 1 || contexts != 1 {
				t.Fatalf("request errors=%d contexts=%d", requestErrors, contexts)
			}
		})
	}
}

func TestSolutionTraceToolSummaryNamesTargetWithoutContent(t *testing.T) {
	for _, test := range []struct {
		tool string
		args map[string]any
		want []string
	}{
		{"read_file", map[string]any{"file": "Folder/App.vb", "version": "original", "offset": 24000, "content": "source-secret"}, []string{"Folder/App.vb", "変更前", "24000"}},
		{"read_file", map[string]any{"file": "App.vb", "version": "candidate", "offset": 0}, []string{"App.vb", "変更後"}},
		{"read_rules", map[string]any{"id": "R001", "offset": 12000, "content": "source-secret"}, []string{"R001", "12000"}},
		{"stage_edits", map[string]any{"file": "App.vb", "edits": []any{map[string]string{"oldText": "source-secret", "newText": "source-secret"}}}, []string{"App.vb"}},
	} {
		summary := solutionToolSummary(functionCall{Name: test.tool, Arguments: string(raw(test.args))})
		for _, wanted := range test.want {
			if !strings.Contains(summary, wanted) {
				t.Errorf("summary %q omits %q", summary, wanted)
			}
		}
		if strings.Contains(summary, "source-secret") {
			t.Fatalf("source in overview: %s", summary)
		}
	}
	long := solutionToolSummary(functionCall{Name: "read_file", Arguments: string(raw(map[string]any{"file": strings.Repeat("a", 1000) + "\nunsafe", "version": "original"}))})
	if len(long) > 300 || strings.Contains(long, "\n") {
		t.Fatalf("unbounded overview: %q", long)
	}
}

func TestSolutionTraceSessionsRemainDistinctAfterReviewRejection(t *testing.T) {
	in := solutionFixture("", "openai")
	actions := append(solutionReadActions(in, false), solutionStage("App/A.vb", "LegacySave", "ModernSave"), solutionDone("modified", "宣言を変更しました。"))
	actions = append(actions, solutionDone("needs_changes", "呼び出し側も修正が必要です。"), solutionStage("App/B.vb", "LegacySave", "ModernSave"), solutionRead("read_manifest", map[string]any{"offset": 0}), solutionDone("modified", "呼び出し側も変更しました。"))
	actions = append(actions, solutionPassActions(in)...)
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests >= len(actions) {
			t.Error("unexpected request")
			http.Error(w, "unexpected", 400)
			return
		}
		action := actions[requests]
		requests++
		solutionReply(w, "openai", requests, action)
	}))
	defer srv.Close()
	in.Config.Endpoint = srv.URL
	sessions := []model.AgentTraceEvent{}
	firstTurns := map[int]int{}
	in.Trace = func(event model.AgentTraceEvent) error {
		if event.Kind == "session" && event.Status == "running" {
			sessions = append(sessions, event)
		}
		if event.Kind == "request" && event.Status == "running" && firstTurns[event.Session] == 0 {
			firstTurns[event.Session] = event.Turn
		}
		return nil
	}
	out, err := RunSolution(context.Background(), in)
	if err != nil || out.Status != "modified" || len(out.Reviews) != 2 || len(sessions) != 4 {
		t.Fatalf("review correction flow failed: %+v err=%v sessions=%+v", out, err, sessions)
	}
	for i, phase := range []string{"repair", "review", "repair", "review"} {
		if sessions[i].Session != i+1 || sessions[i].Phase != phase || firstTurns[i+1] != 1 {
			t.Fatalf("session/turn sequence lost: %+v first turns=%v", sessions, firstTurns)
		}
	}
}
