package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onebyone/internal/model"
)

func TestCLICompleteSetupSelectionAndSettingsWithoutGUI(t *testing.T) {
	_, root, base := workspaceRulesFixture(t)
	config := filepath.Join(base, "cli", "app-settings.json")
	for name, content := range map[string]string{"a.txt": "first\n", "b.txt": "second\n"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"add", "."}, {"-c", "user.name=Test", "-c", "user.email=test@onebyone.local", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull, "commit", "--quiet", "-m", "Add fixture"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("commit fixture: %v: %s", err, output)
		}
	}
	invoke := func(input string, args ...string) []byte {
		t.Helper()
		var out, logs bytes.Buffer
		args = append([]string{"--config", config, "--json"}, args...)
		if code := executeWithInput(args, strings.NewReader(input), &out, &logs); code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, logs.String())
		}
		if !json.Valid(out.Bytes()) {
			t.Fatalf("not clean JSON: %s", out.String())
		}
		return append([]byte(nil), out.Bytes()...)
	}
	invoke("", "workspace", "create", "--name", "CLI-only", "--root", root)
	edit, _ := json.Marshal(workspaceRulesEdit())
	invoke(string(edit), "rules", "create", "--input", "-")
	invoke(`{"includeGlobs":["*.txt"],"maxTurns":8,"maxCostUSD":2,"inputPricePerMillion":1,"outputPricePerMillion":2}`, "settings", "update", "--input", "-")
	var state model.State
	_ = json.Unmarshal(invoke("", "scan"), &state)
	if len(state.Tasks) != 2 {
		t.Fatalf("wrong target count %d", len(state.Tasks))
	}
	_ = json.Unmarshal(invoke("", "selection", "--file", "b.txt"), &state)
	for _, task := range state.Tasks {
		if task.Excluded != (task.File != "b.txt") {
			t.Fatal("file selection did not replace full checked set")
		}
	}
	_ = json.Unmarshal(invoke("", "scan"), &state)
	for _, task := range state.Tasks {
		if task.Excluded != (task.File != "b.txt") {
			t.Fatal("rescan lost selection")
		}
	}
	_ = json.Unmarshal(invoke(`{"maxTurns":0,"maxCostUSD":0}`, "settings", "update", "--input", "-"), &state)
	if state.Config.MaxTurns != 0 || state.Config.MaxCostUSD != 0 || state.Config.OutputPricePerMillion != 2 || len(state.Config.IncludeGlobs) != 1 {
		t.Fatal("partial settings update reset unrelated fields or failed to clear limits")
	}
	var content model.TargetFileContent
	_ = json.Unmarshal(invoke("", "files", "show", "--file", "a.txt"), &content)
	if content.Content != "first\n" {
		t.Fatal("source content not readable")
	}
	var listing model.TargetFileList
	_ = json.Unmarshal(invoke("", "files", "list"), &listing)
	if len(listing.Files) != 2 {
		t.Fatal("source listing incorrect")
	}
	invoke("", "selection", "--none")
	var out, logs bytes.Buffer
	if code := executeWithInput([]string{"--config", config, "run", "--json"}, nil, &out, &logs); code != 1 {
		t.Fatal("run with no selected files/connection should fail")
	}
	invoke("", "selection", "--all")
	output := filepath.Join(base, "exported-result.json")
	invoke("", "report", "--output", output)
	if _, err := os.Stat(output); err != nil {
		t.Fatal(err)
	}
}

func TestCLISettingsRejectOwnedPathsAndMalformedSecretsWithoutChangingState(t *testing.T) {
	s, root, _ := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	before, _ := json.Marshal(s.Snapshot().Config)
	for _, body := range []string{`{"queuePath":"outside"}`, `{"credential":"PRIVATE-INPUT-SECRET"}`, `{"maxTurns":"PRIVATE-INPUT-SECRET"}`, `{"maxTurns":-1}`, `{"includeGlobs":null}`, `{} {}`, `null`, `{}`} {
		_, err := executeSettingsCommand(context.Background(), s, []string{"update", "--input", "-"}, strings.NewReader(body))
		if err == nil {
			t.Fatalf("invalid settings accepted: %s", body)
		}
		if strings.Contains(err.Error(), "PRIVATE-INPUT-SECRET") {
			t.Fatal("input error exposed credential")
		}
		after, _ := json.Marshal(s.Snapshot().Config)
		if !bytes.Equal(before, after) {
			t.Fatal("invalid settings changed workspace")
		}
	}
	for _, args := range [][]string{{"scan", "--queue", "outside"}, {"run", "--rg", "rg"}, {"scan", "--rules", "old-folder"}, {"scan", "--legacy", "old-file"}, {"selection"}, {"selection", "--all", "--none"}, {"discard", "--file", "a.txt"}, {"detail", "--file", "a.txt", "--attempt", "0"}} {
		var out, logs bytes.Buffer
		if _, _, err := parseOptions(args, &out, &logs); err == nil {
			t.Fatalf("invalid/obsolete options accepted: %v", args)
		}
	}
}

func TestCLIExecutionStatusIgnoresOtherRunsAndExcludedFiles(t *testing.T) {
	result := model.ExecutionRunResult{Run: model.ExecutionRun{Status: "completed"}, TargetFiles: []string{"current.txt"}, State: model.State{Tasks: []model.Task{
		{File: "current.txt", Status: "done"}, {File: "old.txt", Status: "needs_human"}, {File: "excluded.txt", Status: "failed", Excluded: true},
	}}}
	if executionResultExitCode(result) != 0 {
		t.Fatal("old or excluded attention leaked into current run exit code")
	}
	for _, status := range []string{"failed", "needs_human"} {
		result.State.Tasks[0].Status = status
		if executionResultExitCode(result) != 2 {
			t.Fatal("current target attention missing from exit code")
		}
	}
	result.Run.Error = "execution failed"
	if executionResultExitCode(result) != 1 {
		t.Fatal("execution error should take priority")
	}
}

func TestCLIJSONInputCancellationDoesNotWaitForEOFOrWriteSettings(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	config := filepath.Join(base, "app-settings.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	var out, logs bytes.Buffer
	done := make(chan int, 1)
	go func() {
		done <- executeContext(ctx, []string{"--config", config, "llm", "save", "--input", "-", "--json"}, reader, &out, &logs)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case code := <-done:
		if code != 130 {
			t.Fatalf("canceled input exit %d: %s", code, logs.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Ctrl-C blocked waiting for JSON stdin EOF")
	}
	var state model.State
	if err := json.Unmarshal(out.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.LLMConnections) != 0 {
		t.Fatal("canceled input registered a connection")
	}
	var dest model.LLMConnection
	if err := readCLIJSON(nil, "-", &dest); err == nil {
		t.Fatal("nil stdin was accepted")
	}
}

func TestCLIJSONInputRejectsOversizeAndNeverEchoesSecrets(t *testing.T) {
	for _, body := range []string{`{"credential":"PRIVATE-INPUT-SECRET",`, `{"unknown":"PRIVATE-INPUT-SECRET"}`, strings.Repeat(" ", maxCLIInputBytes+1)} {
		var dest model.LLMConnection
		err := readCLIJSON(strings.NewReader(body), "-", &dest)
		if err == nil || strings.Contains(err.Error(), "PRIVATE-INPUT-SECRET") {
			t.Fatal("unsafe JSON validation")
		}
	}
	var connection model.LLMConnection
	if err := readCLIJSON(strings.NewReader("\ufeff{\"name\":\"Windows UTF-8\"}"), "-", &connection); err != nil || connection.Name != "Windows UTF-8" {
		t.Fatal("UTF-8 BOM input was not accepted")
	}
}

func TestCLIHelpAndGlobalOptionsHaveNoSideEffects(t *testing.T) {
	config := filepath.Join(t.TempDir(), "must-not-exist", "app-settings.json")
	for _, args := range [][]string{{"rules", "update", "--help"}, {"llm", "login", "--help=false"}, {"workspace", "create", "--help"}, {"settings", "show", "--help=false"}} {
		var out, logs bytes.Buffer
		full := append([]string{"--config", config, "--workspace", "not-a-workspace", "--json"}, args...)
		if code := executeWithInput(full, nil, &out, &logs); code != 0 {
			t.Fatalf("help failed %v: %s", args, logs.String())
		}
		if _, err := os.Stat(filepath.Dir(config)); !os.IsNotExist(err) {
			t.Fatal("help initialized app storage")
		}
	}
}
