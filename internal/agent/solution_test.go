package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

type solutionTestAction struct {
	name         string
	args         any
	status, note string
}

func solutionRead(name string, args map[string]any) solutionTestAction {
	return solutionTestAction{name: name, args: args}
}

func solutionDone(status, note string) solutionTestAction {
	return solutionTestAction{status: status, note: note}
}

func solutionFixture(endpoint, provider string) SolutionInput {
	return SolutionInput{
		Config: model.Config{Provider: provider, Endpoint: endpoint, Deployment: "solution-model", Credential: "test-secret"},
		Files: map[string]string{
			"App.sln":        "Microsoft Visual Studio Solution File, Format Version 12.00\n",
			"App/App.vbproj": "<Project><ItemGroup><Compile Include=\"A.vb\"/><Compile Include=\"B.vb\"/></ItemGroup></Project>\n",
			"App/A.vb":       "Module A\n  Sub LegacySave()\n  End Sub\nEnd Module\n",
			"App/B.vb":       "Module B\n  Sub Run()\n    A.LegacySave()\n  End Sub\nEnd Module\n",
		},
		Rules:           []model.Rule{{ID: "R001", Summary: "Save API migration", Body: "Rename LegacySave to ModernSave across declaration and call sites."}},
		BuildOutput:     "App/B.vb(3,5): error BC30456: 'LegacySave' is not a member of 'A'.\n",
		PreviousFailure: "private-previous-editor-rationale",
	}
}

func solutionReadActions(in SolutionInput, reviewer bool) []solutionTestAction {
	actions := []solutionTestAction{
		solutionRead("read_manifest", map[string]any{"offset": 0}),
		solutionRead("read_build_output", map[string]any{"offset": 0}),
		solutionRead("read_rules", map[string]any{"id": "R001", "offset": 0}),
	}
	for _, file := range []string{"App.sln", "App/App.vbproj", "App/A.vb", "App/B.vb"} {
		actions = append(actions, solutionRead("read_file", map[string]any{"file": file, "version": "original", "offset": 0}))
		if reviewer {
			actions = append(actions, solutionRead("read_file", map[string]any{"file": file, "version": "candidate", "offset": 0}))
		}
	}
	return actions
}

func solutionStage(file, old, replacement string) solutionTestAction {
	return solutionRead("stage_edits", map[string]any{"file": file, "edits": []any{map[string]any{"id": "E1", "oldText": old, "newText": replacement, "ruleIds": []string{"R001"}}}, "removeEditIds": []string{}})
}

func solutionPassActions(in SolutionInput) []solutionTestAction {
	actions := solutionReadActions(in, true)
	actions = append(actions, solutionRead("record_assessments", map[string]any{"assessments": []model.ReviewAssessment{{RuleID: "R001", Status: "satisfied", Reason: "宣言と呼び出しの変更が整合しています。"}}}), solutionDone("passed", "ソリューション全体の変更を確認しました。"))
	return actions
}

func solutionReply(w http.ResponseWriter, provider string, n int, action solutionTestAction) {
	if provider == "claude" {
		usage := map[string]any{"input_tokens": 100, "output_tokens": 40}
		if action.name != "" {
			claudeReply(w, "tool_use", usage, map[string]any{"type": "tool_use", "id": fmt.Sprintf("S%d", n), "name": action.name, "input": action.args})
		} else {
			claudeReply(w, "end_turn", usage, map[string]any{"type": "text", "text": string(raw(map[string]any{"status": action.status, "note": action.note}))})
		}
		return
	}
	if action.name != "" {
		respond(w, testCall(fmt.Sprintf("S%d", n), action.name, action.args))
	} else {
		respond(w, map[string]any{"type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": string(raw(map[string]any{"status": action.status, "note": action.note}))}}})
	}
}

func TestRunSolutionAllProvidersAtomicMultiFileIndependentReview(t *testing.T) {
	for _, provider := range []string{"azure", "openai", "claude"} {
		t.Run(provider, func(t *testing.T) {
			fixture := solutionFixture("", provider)
			editor := append(solutionReadActions(fixture, false), solutionStage("App/A.vb", "LegacySave", "ModernSave"), solutionStage("App/B.vb", "LegacySave", "ModernSave"), solutionRead("read_manifest", map[string]any{"offset": 0}), solutionDone("modified", "private-editor-note"))
			reviewer := solutionPassActions(fixture)
			editorTurn, reviewTurn, requests := 0, 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					return
				}
				requests++
				if req["previous_response_id"] != nil || req["conversation"] != nil {
					t.Error("solution conversations must be independent")
				}
				instructions := fmt.Sprint(req["instructions"])
				historyKey := "input"
				if provider == "claude" {
					instructions, historyKey = fmt.Sprint(req["system"]), "messages"
				}
				isReview := strings.Contains(instructions, "You independently review")
				if isReview {
					body := string(raw(req))
					if strings.Contains(body, "private-editor-note") || strings.Contains(body, fixture.PreviousFailure) {
						t.Error("editor rationale leaked into independent reviewer")
					}
					if reviewTurn == 0 && len(req[historyKey].([]any)) != 1 {
						t.Error("review did not begin with fresh history")
					}
					for _, tool := range req["tools"].([]any) {
						if tool.(map[string]any)["name"] == "stage_edits" {
							t.Error("reviewer received editing tool")
						}
					}
					if reviewTurn >= len(reviewer) {
						t.Error("unexpected reviewer continuation")
						http.Error(w, "unexpected", 400)
						return
					}
					solutionReply(w, provider, requests, reviewer[reviewTurn])
					reviewTurn++
				} else {
					if editorTurn >= len(editor) {
						t.Error("unexpected editor continuation")
						http.Error(w, "unexpected", 400)
						return
					}
					solutionReply(w, provider, requests, editor[editorTurn])
					editorTurn++
				}
			}))
			defer srv.Close()
			in := solutionFixture(srv.URL, provider)
			reserved, settled := 0, 0
			var persisted model.Usage
			in.BeforeRequest = func(id string) error {
				if id == "" {
					t.Error("missing request identity")
				}
				reserved++
				return nil
			}
			in.AfterRequest = func(u model.Usage, err error) error { settled++; persisted = addReviewUsage(persisted, u); return err }
			before := string(raw(in.Files))
			out, err := RunSolution(context.Background(), in)
			if err != nil {
				t.Fatal(err)
			}
			if out.Status != "modified" || len(out.Files) != 2 || !reflect.DeepEqual(out.RulesApplied, []string{"R001"}) {
				t.Fatalf("unexpected solution proposal: %+v", out)
			}
			if len(out.Reviews) != 1 || out.Reviews[0].Verdict != "passed" || len(out.Reviews[0].CandidateHashes) != 4 || out.Reviews[0].CandidateHashes["App/A.vb"] == out.Reviews[0].OriginalHashes["App/A.vb"] {
				t.Fatalf("review evidence missing: %+v", out.Reviews)
			}
			if out.Usage != persisted || out.Usage.Turns != requests || out.Usage.InputTokens != requests*100 || reserved != requests || settled != requests {
				t.Fatalf("request usage not persisted once: %+v %+v", out.Usage, persisted)
			}
			if string(raw(in.Files)) != before {
				t.Fatal("agent mutated input files")
			}
			after, err := ApplySolutionEdits(in.Files, out.Files)
			if err != nil || strings.Contains(after["App/A.vb"], "LegacySave") || strings.Contains(after["App/B.vb"], "LegacySave") || after["App.sln"] != in.Files["App.sln"] {
				t.Fatalf("bad atomic candidate: %+v %v", after, err)
			}
		})
	}
}

func TestRunSolutionRejectedReviewRepairsWholeCandidate(t *testing.T) {
	fixture := solutionFixture("", "openai")
	editor := append(solutionReadActions(fixture, false), solutionStage("App/A.vb", "LegacySave", "IncorrectSave"), solutionStage("App/B.vb", "LegacySave", "ModernSave"), solutionRead("read_manifest", map[string]any{"offset": 0}), solutionDone("modified", "editor's first candidate"))
	editor = append(editor, solutionRead("read_state", map[string]any{"offset": 0}), solutionStage("App/A.vb", "LegacySave", "ModernSave"), solutionRead("read_manifest", map[string]any{"offset": 0}), solutionDone("modified", "review finding corrected"))
	reviewer := append([]solutionTestAction{solutionDone("needs_changes", "App/A.vb: IncorrectSave declaration disagrees with the ModernSave caller.")}, solutionPassActions(fixture)...)
	ei, ri, n := 0, 0, 0
	sawFeedback := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		n++
		if strings.Contains(fmt.Sprint(req["instructions"]), "You independently review") {
			if ri >= len(reviewer) {
				http.Error(w, "unexpected", 400)
				return
			}
			solutionReply(w, "openai", n, reviewer[ri])
			ri++
		} else {
			if strings.Contains(fmt.Sprint(req["input"]), "disagrees with") {
				sawFeedback = true
			}
			if ei >= len(editor) {
				http.Error(w, "unexpected", 400)
				return
			}
			solutionReply(w, "openai", n, editor[ei])
			ei++
		}
	}))
	defer srv.Close()
	out, err := RunSolution(context.Background(), solutionFixture(srv.URL, "openai"))
	if err != nil || out.Status != "modified" || !sawFeedback || len(out.Reviews) != 2 || out.Reviews[0].Verdict != "needs_changes" || out.Reviews[1].Verdict != "passed" {
		t.Fatalf("review repair was bypassed: %+v err=%v feedback=%t", out, err, sawFeedback)
	}
	for _, file := range out.Files {
		for _, edit := range file.Edits {
			if strings.Contains(edit.NewText, "Incorrect") {
				t.Fatal("rejected edit adopted")
			}
		}
	}
}

func TestSolutionApprovalRequiresCompleteSourcesAndEveryRule(t *testing.T) {
	in := solutionFixture("", "openai")
	s := newSolutionSession(in, true)
	decision := string(raw(map[string]any{"status": "passed", "note": "review complete"}))
	for _, action := range solutionReadActions(in, true) {
		if action.name == "read_file" && action.args.(map[string]any)["file"] == "App/App.vbproj" {
			continue
		}
		if _, err := s.execute(functionCall{Name: action.name, Arguments: string(raw(action.args))}); err != nil {
			t.Fatal(err)
		}
	}
	s.assessments["R001"] = model.ReviewAssessment{RuleID: "R001", Status: "satisfied", Reason: "checked"}
	if _, _, err := s.finish(decision); err == nil || !strings.Contains(err.Error(), "App/App.vbproj") {
		t.Fatalf("unread project approved: %v", err)
	}
	for _, version := range []string{"original", "candidate"} {
		if _, err := s.execute(functionCall{Name: "read_file", Arguments: string(raw(map[string]any{"file": "App/App.vbproj", "version": version, "offset": 0}))}); err != nil {
			t.Fatal(err)
		}
	}
	delete(s.assessments, "R001")
	if _, _, err := s.finish(decision); err == nil {
		t.Fatal("missing assessment accepted")
	}
	s.assessments["R001"] = model.ReviewAssessment{RuleID: "R001", Status: "violated", Reason: "wrong"}
	if _, _, err := s.finish(decision); err == nil {
		t.Fatal("violated rule approved")
	}
	if _, err := s.execute(functionCall{Name: "stage_edits", Arguments: `{}`}); err == nil {
		t.Fatal("reviewer executed editor tool")
	}
}

func TestSolutionLargeUTF8PagesCannotHideUnreadContent(t *testing.T) {
	s := newSolutionSession(solutionFixture("", "openai"), true)
	text := strings.Repeat("日本語\n", 50000)
	s.in.Files["App/Large.vb"] = text
	s.after = s.in.Files
	offset, pages := 0, 0
	for {
		value, err := s.execute(functionCall{Name: "read_file", Arguments: string(raw(map[string]any{"file": "App/Large.vb", "version": "original", "offset": offset}))})
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Content    string `json:"content"`
			NextOffset int    `json:"nextOffset"`
			Complete   bool   `json:"complete"`
		}
		if err := json.Unmarshal([]byte(value), &page); err != nil {
			t.Fatal(err)
		}
		pages++
		if page.Content != text[offset:offset+len(page.Content)] {
			t.Fatal("page changed source bytes")
		}
		if page.Complete {
			break
		}
		if coveredReviewBytes(s.read["original:App/Large.vb"], len(text)) {
			t.Fatal("partial read marked complete")
		}
		offset = page.NextOffset
	}
	if pages < 2 || !coveredReviewBytes(s.read["original:App/Large.vb"], len(text)) {
		t.Fatal("large source was not fully paged")
	}
	if _, err := s.execute(functionCall{Name: "read_file", Arguments: `{"file":"App/Large.vb","version":"original","offset":1}`}); err == nil {
		t.Fatal("accepted offset inside UTF-8 rune")
	}
}

func TestApplySolutionEditsUsesOriginalAnchorsAndIsAtomic(t *testing.T) {
	files := map[string]string{"A.vb": "abc DEF", "B.vb": "aaa"}
	result, err := ApplySolutionEdits(files, []SolutionFileEdit{{File: "A.vb", Edits: []model.Edit{{OldText: "abc", NewText: "DEF"}, {OldText: "DEF", NewText: "done"}}}})
	if err != nil || result["A.vb"] != "DEF done" {
		t.Fatalf("replaced introduced text: %+v %v", result, err)
	}
	for _, edits := range [][]SolutionFileEdit{
		{{File: "missing.vb", Edits: []model.Edit{{OldText: "a", NewText: "b"}}}},
		{{File: "A.vb", Edits: []model.Edit{{OldText: "abc", NewText: "new"}}}, {File: "B.vb", Edits: []model.Edit{{OldText: "aa", NewText: "new"}}}},
		{{File: "A.vb", Edits: []model.Edit{{OldText: "abc", NewText: "x"}, {OldText: "bc", NewText: "y"}}}},
		{{File: "A.vb", Edits: []model.Edit{{OldText: "abc", NewText: "x"}}}, {File: "A.vb", Edits: []model.Edit{{OldText: "DEF", NewText: "y"}}}},
		{{File: "A.vb", Edits: []model.Edit{{OldText: "abc", NewText: "\x00"}}}},
	} {
		if result, err := ApplySolutionEdits(files, edits); err == nil || result != nil {
			t.Fatalf("invalid batch partially accepted: %+v %v", result, err)
		}
		if files["A.vb"] != "abc DEF" || files["B.vb"] != "aaa" {
			t.Fatal("input map was mutated")
		}
	}
}

func TestSolutionUsageSettlementFailureCannotApprove(t *testing.T) {
	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		solutionReply(w, "openai", requests, solutionDone("needs_human", "missing source"))
	}))
	defer srv.Close()
	in := solutionFixture(srv.URL, "openai")
	in.BeforeRequest = func(string) error { return nil }
	in.AfterRequest = func(model.Usage, error) error { return errors.New("journal unavailable") }
	out, err := RunSolution(context.Background(), in)
	if err == nil || !IsFatal(err) || out.Status != "" || len(out.Files) != 0 || out.Usage.Turns != 1 || requests != 1 {
		t.Fatalf("persistence error lost or adopted: %+v %v", out, err)
	}
}

func TestSolutionRejectsUnsafeInputBeforeProviderRequest(t *testing.T) {
	for _, path := range []string{"../secret.vb", ".env", "OneByOne/settings.json", "App\\A.vb"} {
		in := solutionFixture("", "openai")
		in.Files[path] = "secret"
		if _, err := RunSolution(context.Background(), in); err == nil {
			t.Fatalf("accepted unsafe path %q", path)
		}
	}
}

func TestSolutionManifestUsesOnlyRuleIDAndDescription(t *testing.T) {
	in := solutionFixture("", "openai")
	in.Rules[0].PathPattern = "must-not-leak"
	in.Rules[0].ContentPattern = "must-not-leak"
	in.Rules[0].DiagnosticPattern = "must-not-leak"
	in.Rules[0].Always = true
	manifest := newSolutionSession(in, false).manifest()
	var catalog struct {
		Rules []map[string]any `json:"rules"`
	}
	if err := json.Unmarshal([]byte(manifest), &catalog); err != nil {
		t.Fatal(err)
	}
	if len(catalog.Rules) != 1 || len(catalog.Rules[0]) != 2 || catalog.Rules[0]["id"] != "R001" || catalog.Rules[0]["description"] != in.Rules[0].Summary || strings.Contains(manifest, "must-not-leak") {
		t.Fatalf("solution rule catalog contains file-mode metadata: %s", manifest)
	}
	for _, prompt := range []string{solutionInstructions, solutionReviewInstructions} {
		for _, field := range []string{"pathPattern", "contentPattern", "diagnosticPattern", "(always)"} {
			if strings.Contains(prompt, field) {
				t.Fatalf("solution prompt still relies on %s", field)
			}
		}
	}
}
