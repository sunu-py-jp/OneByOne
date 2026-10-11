package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"onebyone/internal/rulepack"
)

func TestCLIRuleTemplateWithoutWorkspace(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	output := filepath.Join(base, "rules-template.csv")
	var stdout, stderr bytes.Buffer
	code := executeWithInput([]string{"--config", filepath.Join(base, "app-settings.json"), "--json", "rules", "template", "--output", output}, nil, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("template: exit %d: %s", code, stderr.String())
	}
	var result map[string]string
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result["path"] != output {
		t.Fatalf("unexpected output: %s (%v)", stdout.String(), err)
	}
	p, err := rulepack.Read(output)
	if err != nil || len(p.Rules) != 0 {
		t.Fatalf("template must be an empty, importable CSV: %v", err)
	}
}

func TestCLIRuleTemplateRejectsInvalidArgumentsBeforeWriting(t *testing.T) {
	output := filepath.Join(t.TempDir(), "rules-template.csv")
	for _, args := range [][]string{
		{"template"},
		{"template", "--output", output, "--unknown"},
		{"template", "--output", output, "extra"},
	} {
		if _, err := executeRulesCommand(context.Background(), nil, args, nil); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executeRulesCommand(ctx, nil, []string{"template", "--output", output}, nil); err == nil {
		t.Fatal("canceled template export succeeded")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid command wrote template: %v", err)
	}
}
