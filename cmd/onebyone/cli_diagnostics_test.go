package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestCLIDiagnosticsImportSettingsAndClear(t *testing.T) {
	s, root, base := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	input := filepath.Join(base, "diagnostics.jsonl")
	if err := os.WriteFile(input, []byte(`{"file":"src/a.cs","code":"CS0619","message":"API removed","line":4}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	value, err := executeDiagnosticsCommand(ctx, s, []string{"import", "--input", input})
	if err != nil {
		t.Fatal(err)
	}
	state := value.(model.State)
	if state.Diagnostics == nil || state.Diagnostics.Count != 1 || state.Config.DiagnosticsPath == input {
		t.Fatal("import not snapshotted")
	}
	if _, err := executeSettingsCommand(ctx, s, []string{"update", "--input", "-"}, strings.NewReader(`{"diagnosticsOnly":true}`)); err != nil {
		t.Fatal(err)
	}
	if !s.Snapshot().Config.DiagnosticsOnly {
		t.Fatal("diagnostics-only setting lost")
	}
	summary, err := executeDiagnosticsCommand(ctx, s, []string{"show"})
	if err != nil || summary.(*model.DiagnosticSummary).FileCount != 1 {
		t.Fatalf("summary=%+v err=%v", summary, err)
	}
	if _, err := executeDiagnosticsCommand(ctx, s, []string{"clear"}); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Config.DiagnosticsPath != "" || s.Snapshot().Config.DiagnosticsOnly {
		t.Fatal("clear failed")
	}
	if _, err := os.Stat(input); err != nil {
		t.Fatal("cleared caller's original input")
	}
}

func TestCLIDiagnosticsPreflightAndHelp(t *testing.T) {
	ctx := context.WithValue(context.Background(), cliArgumentValidationKey{}, true)
	for _, args := range [][]string{{"import", "--input", "missing.jsonl"}, {"clear"}, {"show"}} {
		if _, err := executeDiagnosticsCommand(ctx, nil, args); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"import"}, {"import", "--input", "-"}, {"clear", "unexpected"}} {
		if _, err := executeDiagnosticsCommand(ctx, nil, args); err == nil {
			t.Fatalf("accepted invalid args %v", args)
		}
	}
	var out, errOut bytes.Buffer
	if _, help, err := parseOptions([]string{"diagnostics", "--help"}, &out, &errOut); err != nil || !help || !strings.Contains(out.String(), "diagnostics.jsonl") {
		t.Fatalf("help failed %s %v", out.String(), err)
	}
}
