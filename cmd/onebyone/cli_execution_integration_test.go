package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"onebyone/internal/model"
	"onebyone/internal/rulepack"
)

const cliExecutionTestCredential = "cli-execution-local-fixture-credential"

func cliExecutionCommand(t *testing.T, config string, wantCode int, input any, args ...string) []byte {
	t.Helper()
	var encoded []byte
	if input != nil {
		var err error
		encoded, err = json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
	}
	var out, logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	args = append(append([]string{}, args...), "--config", config, "--json")
	code := executeContext(ctx, args, bytes.NewReader(encoded), &out, &logs)
	if strings.Contains(out.String(), cliExecutionTestCredential) || strings.Contains(logs.String(), cliExecutionTestCredential) {
		t.Fatal("CLI output exposed the connection credential")
	}
	if code != wantCode {
		t.Fatalf("CLI %v: exit %d, want %d\n%s\n%s", args, code, wantCode, logs.String(), out.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("CLI %v did not return a single JSON value: %s", args, out.String())
	}
	return append([]byte{}, out.Bytes()...)
}

func cliExecutionDecode(t *testing.T, data []byte, value any) {
	t.Helper()
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func cliExecutionCommitSources(t *testing.T, root string) {
	t.Helper()
	environment := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	for _, args := range [][]string{{"add", "--all"}, {"commit", "--quiet", "-m", "CLI execution fixture"}} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		command := exec.CommandContext(ctx, "git", append([]string{"-c", "user.name=OneByOne Test", "-c", "user.email=test@onebyone.local", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull}, args...)...)
		command.Dir, command.Env = root, environment
		output, err := command.CombinedOutput()
		cancel()
		if err != nil {
			t.Fatalf("commit isolated CLI sources: %v\n%s", err, output)
		}
	}
}

func cliExecutionResponse(w http.ResponseWriter, value any, toolName, callID string) {
	data, _ := json.Marshal(value)
	var item any
	if toolName != "" {
		item = map[string]any{"type": "function_call", "call_id": callID, "name": toolName, "arguments": string(data)}
	} else {
		item = map[string]any{"type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(data)}}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status": "completed", "output": []any{item},
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 30, "input_tokens_details": map[string]any{"cached_tokens": 0}},
	})
}

func cliExecutionTask(t *testing.T, tasks []model.Task, file string) model.Task {
	t.Helper()
	for _, task := range tasks {
		if task.File == file {
			return task
		}
	}
	t.Fatalf("task %q is missing", file)
	return model.Task{}
}

// No proposer or reviewer is injected: the public CLI restarts its Service for
// every command and uses the real provider codec, tool loop, validation gate,
// isolated worktree, independent review, history and report implementation.
// Only the HTTP model responses are supplied by a localhost test server.
func TestCLIExecutionHTTPWorkflowPersistsHistoricalResultsAndHumanHold(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg is required for CLI scan integration")
	}
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	for _, name := range []string{"OPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		t.Setenv(name, "")
	}
	root, config := filepath.Join(base, "source"), filepath.Join(base, "app", "settings.json")
	if err := os.MkdirAll(filepath.Join(root, "src"), 0700); err != nil {
		t.Fatal(err)
	}
	initCLITestRepository(t, root)
	const file = "src/update.js"
	const heldFile = "src/hold.js"
	const before = "export async function save(record) {\n  return ArchiveStore.save(record);\n}\n"
	const after = "export async function save(record) {\n  return await RecordStore.write(record);\n}\n"
	const heldBefore = "export function forward(record) {\n  return externalSave(record);\n}\n"
	for name, content := range map[string]string{file: before, heldFile: heldBefore} {
		if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(name)), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	cliExecutionCommitSources(t, root)

	rule := model.RuleDefinition{
		Version: 1, Name: "Update storage while preserving caller contracts", Pattern: "",
		Overview: "Use RecordStore.write instead of ArchiveStore.save and preserve callers' completion behavior.",
		Before:   "return ArchiveStore.save(value);", After: "return await RecordStore.write(value);",
		Notes: "Do not infer an external callback's ownership contract.", HoldConditions: "An external save contract is not available in the target.",
	}
	ruleJSON, err := json.Marshal(rule)
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "fixture.oborules")
	if err := rulepack.Write(archive, &rulepack.Package{
		Settings: rulepack.Settings{IncludeGlobs: []string{"**/*.js"}, ExcludeGlobs: []string{}, CheckCommands: []model.Command{}},
		Files:    map[string][]byte{"rules/R001/rule.json": ruleJSON, "patterns/legacy-symbols.txt": []byte(`\bArchiveStore\b`)},
	}); err != nil {
		t.Fatal(err)
	}

	var editorRequests, reviewRequests, heldRequests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" || r.Header.Get("Authorization") != "Bearer "+cliExecutionTestCredential {
			t.Error("CLI did not use the registered OpenAI connection")
			http.Error(w, "unexpected request", http.StatusBadRequest)
			return
		}
		var request struct {
			Model string            `json:"model"`
			Tools []json.RawMessage `json:"tools"`
			Input []json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil || len(request.Input) == 0 || request.Model != "local-test-model" {
			t.Error("invalid provider request from CLI")
			http.Error(w, "invalid model request", http.StatusBadRequest)
			return
		}
		var first struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}
		if err := json.Unmarshal(request.Input[0], &first); err != nil || first.Role != "user" {
			t.Error("missing independent file input")
			http.Error(w, "invalid file input", http.StatusBadRequest)
			return
		}
		var payload struct {
			File          string       `json:"file"`
			Content       string       `json:"content"`
			BaseHash      string       `json:"baseHash"`
			CandidateHash string       `json:"candidateHash"`
			Before        string       `json:"before"`
			After         string       `json:"after"`
			Rules         []model.Rule `json:"rules"`
		}
		if err := json.Unmarshal([]byte(first.Content), &payload); err != nil {
			t.Error("file input is not JSON")
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		if len(request.Tools) == 0 {
			reviewRequests.Add(1)
			if payload.File != file || payload.Before != before || payload.After != after || len(request.Input) != 1 || len(payload.Rules) != 1 || payload.Rules[0].ID != "R001" {
				t.Error("independent review did not receive only the expected target and rule")
				http.Error(w, "unexpected review context", http.StatusBadRequest)
				return
			}
			cliExecutionResponse(w, map[string]any{
				"baseHash": payload.BaseHash, "candidateHash": payload.CandidateHash, "verdict": "passed", "summary": "The storage call preserves the caller's returned value.",
				"assessments": []model.ReviewAssessment{{RuleID: "R001", Status: "satisfied", Reason: "The supported API is awaited and its result is returned."}}, "issues": []model.ReviewIssue{},
			}, "", "")
			return
		}
		if payload.File == heldFile {
			turn := heldRequests.Add(1)
			if payload.Content != heldBefore || turn > 2 {
				t.Error("human hold leaked another file context or continued unexpectedly")
				http.Error(w, "unexpected held-file request", http.StatusBadRequest)
				return
			}
			if turn == 1 {
				cliExecutionResponse(w, model.PlanUpdate{
					ExpectedRevision: 0,
					RuleDecisions:    []model.PlanDecision{{RuleID: "R001", Decision: "blocked", Reason: "The externalSave callback contract is outside this file and must be confirmed."}},
					Items:            []model.PlanItem{{ID: "P1", RuleID: "R001", Location: "externalSave(record)", Risk: "Changing the callback could change transaction ownership.", Change: "Confirm externalSave's ownership before updating storage.", Expected: "The external contract remains intact.", Status: "blocked", HoldReason: "The externalSave contract is unavailable."}},
				}, "update_state", "hold-plan")
			} else {
				cliExecutionResponse(w, map[string]string{"outcome": "needs_human", "candidateId": "", "note": "The externalSave contract must be confirmed before changing this file."}, "", "")
			}
			return
		}
		if payload.File != file || payload.Content != before {
			t.Error("editor received an unexpected source or another file's context")
			http.Error(w, "unexpected target", http.StatusBadRequest)
			return
		}
		switch editorRequests.Add(1) {
		case 1:
			cliExecutionResponse(w, model.PlanUpdate{
				ExpectedRevision: 0,
				RuleDecisions:    []model.PlanDecision{{RuleID: "R001", Decision: "modify", Reason: "The storage call uses the removed API."}},
				Items:            []model.PlanItem{{ID: "P1", RuleID: "R001", Location: "return ArchiveStore.save(record);", Risk: "The removed API will fail after the library update.", Change: "Await RecordStore.write and return its result.", Expected: "The save function returns the supported storage result.", Status: "proposed"}},
			}, "update_state", "save-plan")
		case 2:
			cliExecutionResponse(w, model.CandidateRequest{
				PlanRevision: 1, BaseHash: payload.BaseHash,
				Edits: []model.Edit{{OldText: "return ArchiveStore.save(record);", NewText: "return await RecordStore.write(record);", ItemIDs: []string{"P1"}}}, AddressedItemIDs: []string{"P1"},
			}, "validate_candidate", "save-candidate")
		case 3:
			var candidate model.CandidateValidation
			for _, row := range request.Input {
				var result struct {
					Type   string `json:"type"`
					CallID string `json:"call_id"`
					Output string `json:"output"`
				}
				if json.Unmarshal(row, &result) == nil && result.Type == "function_call_output" && result.CallID == "save-candidate" {
					if err := json.Unmarshal([]byte(result.Output), &candidate); err != nil {
						t.Errorf("candidate did not return structured validation: %s", result.Output)
					}
				}
			}
			if !candidate.Passed || candidate.CandidateID == "" || candidate.PlanRevision != 1 {
				t.Error("editor final answer arrived without a validated candidate")
				http.Error(w, "candidate validation failed", http.StatusBadRequest)
				return
			}
			cliExecutionResponse(w, map[string]string{"outcome": "modified", "candidateId": candidate.CandidateID, "note": "Updated the storage API and preserved the returned result."}, "", "")
		default:
			t.Error("CLI editor exceeded the expected complete tool loop")
			http.Error(w, "unexpected extra request", http.StatusBadRequest)
		}
	}))
	defer server.Close()

	cliExecutionCommand(t, config, 0, nil, "workspace", "create", "--name", "CLI HTTP workflow", "--root", root)
	cliExecutionCommand(t, config, 0, nil, "rules", "import", "--input", archive, "--mode", "replace")
	cliExecutionCommand(t, config, 0, map[string]any{"maxAttempts": 1, "maxTurns": 6, "timeoutSeconds": 15, "maxOutputTokens": 2048}, "settings", "update", "--input", "-")
	var registered model.State
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, model.LLMConnection{
		Name: "Local HTTP test", Provider: "openai", Endpoint: server.URL + "/v1/", Deployment: "local-test-model", AuthMode: "api_key", Credential: cliExecutionTestCredential,
	}, "llm", "save", "--input", "-"), &registered)
	if len(registered.LLMConnections) != 1 {
		t.Fatal("CLI-only setup did not register a connection")
	}
	cliExecutionCommand(t, config, 0, nil, "llm", "select", "--id", registered.LLMConnections[0].ID)
	var scanned model.State
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "scan"), &scanned)
	if len(scanned.Tasks) != 2 || editorRequests.Load()+heldRequests.Load()+reviewRequests.Load() != 0 {
		t.Fatal("scan omitted common-rule targets or sent an LLM request")
	}
	cliExecutionCommand(t, config, 0, nil, "selection", "--file", file)
	var firstRun model.State
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "run"), &firstRun)
	firstTask := cliExecutionTask(t, firstRun.Tasks, file)
	if firstTask.Status != "done" || len(firstRun.ExecutionRuns) != 1 || len(firstTask.History) != 1 || len(firstTask.History[0].Reviews) != 1 || firstTask.History[0].Reviews[0].Verdict != "passed" {
		t.Fatalf("CLI did not adopt a mechanically validated and independently reviewed edit: %+v", firstTask)
	}
	firstID := firstRun.ExecutionRuns[0].ID
	if firstID == "" || firstTask.History[0].ExecutionID != firstID || firstRun.Worktree == "" || firstTask.History[0].Commit == "" {
		t.Fatal("successful CLI execution did not persist its ID, worktree and commit")
	}
	for source, want := range map[string]string{"target": before, "execution": after} {
		var content model.TargetFileContent
		cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "files", "show", "--file", file, "--source", source), &content)
		if content.Content != want {
			t.Fatalf("%s preview did not use the correct persisted source: %q", source, content.Content)
		}
	}
	cliExecutionCommand(t, config, 0, nil, "selection", "--file", heldFile)
	var secondRun model.State
	cliExecutionDecode(t, cliExecutionCommand(t, config, 2, nil, "run"), &secondRun)
	held := cliExecutionTask(t, secondRun.Tasks, heldFile)
	if held.Status != "needs_human" || len(held.History) != 1 || len(held.History[0].Reviews) != 0 || held.History[0].Commit != "" {
		t.Fatalf("recorded human hold was not retained without review or commit: %+v", held)
	}
	var runs []model.ExecutionRun
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "runs", "list"), &runs)
	if len(runs) != 2 || held.History[0].ExecutionID == firstID || held.History[0].ExecutionID == "" {
		t.Fatal("separate CLI invocations were not retained as distinct executions")
	}
	var historical model.ExecutionRunResult
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "runs", "show", "--id", firstID), &historical)
	if historical.Run.ID != firstID || !reflect.DeepEqual(historical.TargetFiles, []string{file}) || cliExecutionTask(t, historical.State.Tasks, heldFile).Status != "pending" {
		t.Fatal("first execution borrowed the second execution's targets or held result")
	}
	var detail model.FileDetail
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "detail", "--run", firstID, "--file", file), &detail)
	if !detail.Cumulative || detail.Before != before || detail.After != after || !strings.Contains(detail.Diff, "+  return await RecordStore.write(record);") || len(detail.Changes) != 1 || detail.Changes[0].RuleID != "R001" || detail.Changes[0].Status != "fixed" || detail.Changes[0].SourceAttemptID != firstTask.History[0].ID {
		t.Fatalf("historical CLI detail omitted the adopted diff or rule attribution: %+v", detail)
	}
	beforeLine, afterLine := false, false
	for _, location := range detail.Changes[0].LineRanges {
		beforeLine = beforeLine || location.BeforeStart == 2 && location.BeforeEnd == 2
		afterLine = afterLine || location.AfterStart == 2 && location.AfterEnd == 2
	}
	if !beforeLine || !afterLine {
		t.Fatal("historical rule attribution lost the changed line on either side")
	}
	// Publishing creates one reviewable commit in the original repository. It
	// must not check out that branch or expose the held file's proposed edits.
	var preview model.ResultPublicationPreview
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "publish", "preview"), &preview)
	if preview.WorkspaceID != secondRun.ActiveWorkspaceID || preview.Revision == "" || len(preview.Files) != 1 || preview.Files[0].File != file || !reflect.DeepEqual(preview.Files[0].RulesApplied, []string{"R001"}) || !strings.Contains(preview.Message, file) || !strings.Contains(preview.Message, "R001") {
		t.Fatalf("publication preview omitted cumulative adopted edits or included a hold: %+v", preview)
	}
	var publishedDiff map[string]string
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, map[string]string{"workspaceId": preview.WorkspaceID, "revision": preview.Revision, "file": file}, "publish", "diff", "--input", "-"), &publishedDiff)
	if !strings.Contains(publishedDiff["diff"], "+  return await RecordStore.write(record);") {
		t.Fatal("publication diff did not show the reviewed adopted contents")
	}
	gitRead := func(directory string, args ...string) string {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = directory
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("read publication git state %v: %v %s", args, err, output)
		}
		return strings.TrimSpace(string(output))
	}
	originalBranch, originalHead := gitRead(root, "symbolic-ref", "HEAD"), gitRead(root, "rev-parse", "HEAD")
	workingHead := gitRead(firstRun.Worktree, "rev-parse", "HEAD")
	publicationInput := map[string]string{"workspaceId": preview.WorkspaceID, "revision": preview.Revision, "branch": "review/cli-storage", "title": "Update the storage API"}
	var publication model.ResultPublication
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, publicationInput, "publish", "create", "--input", "-"), &publication)
	if publication.Branch != "review/cli-storage" || publication.Commit == "" || publication.FileCount != 1 || publication.Message != preview.Message || publication.BaseCommit != preview.BaseCommit || publication.SourceCommit != preview.SourceCommit {
		t.Fatalf("publication result is incomplete: %+v", publication)
	}
	if got := gitRead(root, "rev-list", "--parents", "-n", "1", publication.Commit); got != publication.Commit+" "+preview.BaseCommit {
		t.Fatalf("publication is not a single commit based on the processing start: %s", got)
	}
	if gitRead(root, "rev-list", "--count", preview.BaseCommit+".."+publication.Commit) != "1" || gitRead(root, "show", publication.Branch+":"+file) != strings.TrimSpace(after) || gitRead(root, "show", publication.Branch+":"+heldFile) != strings.TrimSpace(heldBefore) {
		t.Fatal("published branch included internal history or unadopted changes")
	}
	if gitRead(root, "symbolic-ref", "HEAD") != originalBranch || gitRead(root, "rev-parse", "HEAD") != originalHead || gitRead(firstRun.Worktree, "rev-parse", "HEAD") != workingHead {
		t.Fatal("publishing checked out a branch or moved an existing HEAD")
	}
	if strings.Contains("\n"+gitRead(root, "worktree", "list", "--porcelain")+"\n", "\nbranch refs/heads/"+publication.Branch+"\n") {
		t.Fatal("published branch is occupied by an application worktree")
	}
	gitRead(root, "checkout", "--quiet", publication.Branch)
	if gitRead(root, "symbolic-ref", "HEAD") != "refs/heads/"+publication.Branch || gitRead(root, "rev-parse", "HEAD") != publication.Commit {
		t.Fatal("published branch could not be checked out in the original folder")
	}
	gitRead(root, "checkout", "--quiet", strings.TrimPrefix(originalBranch, "refs/heads/"))
	cliExecutionCommand(t, config, 1, publicationInput, "publish", "create", "--input", "-")
	if gitRead(root, "rev-parse", "refs/heads/"+publication.Branch) != publication.Commit {
		t.Fatal("repeat publication overwrote the existing branch")
	}
	var publishedPreview model.ResultPublicationPreview
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "publish", "preview"), &publishedPreview)
	if len(publishedPreview.Publications) != 1 || publishedPreview.Publications[0].Commit != publication.Commit {
		t.Fatal("publication history did not survive reopening the CLI")
	}
	var discarded model.State
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "discard", "--file", file, "--yes"), &discarded)
	restored := cliExecutionTask(t, discarded.Tasks, file)
	if restored.Status != "pending" || restored.CanDiscardChanges || len(restored.Discards) != 1 || restored.Discards[0].State != "done" || restored.Discards[0].Commit == "" || !reflect.DeepEqual(restored.History, firstTask.History) {
		t.Fatal("CLI discard did not compensate the edit while preserving original attempt evidence")
	}
	var restoredContent model.TargetFileContent
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "files", "show", "--file", file, "--source", "execution"), &restoredContent)
	if restoredContent.Content != before || cliExecutionTask(t, discarded.Tasks, heldFile).Status != "needs_human" {
		t.Fatal("CLI discard did not restore only the selected execution file")
	}
	var frozenAfterDiscard model.FileDetail
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "detail", "--run", firstID, "--file", file), &frozenAfterDiscard)
	if frozenAfterDiscard.Before != before || frozenAfterDiscard.After != after || frozenAfterDiscard.Diff != detail.Diff {
		t.Fatal("discard rewrote the historical execution's adopted diff")
	}
	reportPath := filepath.Join(base, "first-execution.json")
	var exported map[string]string
	cliExecutionDecode(t, cliExecutionCommand(t, config, 0, nil, "report", "--run", firstID, "--output", reportPath), &exported)
	if exported["path"] == "" {
		t.Fatal("CLI report did not return the selected output file")
	}
	report, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	var saved model.ExecutionRunResult
	cliExecutionDecode(t, report, &saved)
	if saved.Run.ID != firstID || !reflect.DeepEqual(saved.TargetFiles, []string{file}) || cliExecutionTask(t, saved.State.Tasks, heldFile).Status != "pending" || cliExecutionTask(t, saved.State.Tasks, file).Status != "done" || len(saved.State.LLMConnections) != 0 || bytes.Contains(report, []byte(cliExecutionTestCredential)) {
		t.Fatal("historical export mixed executions or exposed personal connection data")
	}
	if editorRequests.Load() != 3 || reviewRequests.Load() != 1 || heldRequests.Load() != 2 {
		t.Fatalf("unexpected HTTP loop counts: editor=%d review=%d hold=%d", editorRequests.Load(), reviewRequests.Load(), heldRequests.Load())
	}
}
