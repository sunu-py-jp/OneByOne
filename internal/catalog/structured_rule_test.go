package catalog

import (
	"context"
	"errors"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"path/filepath"
	"strings"
	"testing"
)

func TestStructuredRulesUseSameMarkdownForCommonPromptAndReadRule(t *testing.T) {
	cfg := fixture(t)
	d := model.RuleDefinition{Version: 1, Name: "structured example", Overview: "overview", Before: "before();\n```\n", After: "after();", Notes: "notes", HoldConditions: "hold", Pattern: ""}
	writeRule(t, cfg, "R001", d)
	cat, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	expected := ruleformat.Markdown(d)
	read, err := cat.ReadRule("R001")
	if err != nil || read != expected || !strings.Contains(cat.SystemPrompt, expected) {
		t.Fatal("ReadRule and common prompt do not share the Markdown converter")
	}
	if cat.Rules[0].Overview != d.Overview || cat.Rules[0].Before != d.Before || cat.Rules[0].After != d.After || cat.Rules[0].Notes != d.Notes || cat.Rules[0].HoldConditions != d.HoldConditions {
		t.Fatal("state does not expose independent fields")
	}
	if strings.Contains(read, `"overview"`) || strings.Contains(read, "pattern") {
		t.Fatal("raw definition escaped into model Markdown")
	}
	d.Pattern = "unique-regex-not-in-fields"
	writeRule(t, cfg, "R001", d)
	individual, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := individual.ReadRule("R001")
	if body != expected || strings.Contains(individual.SystemPrompt, "before();") || strings.Contains(body, d.Pattern) {
		t.Fatal("individual prompt contains a full body or generated Markdown contains the pattern")
	}
	if cat.Hash == individual.Hash {
		t.Fatal("pattern change did not invalidate the catalog")
	}
}

func TestLegacyDefinitionFilesRejectedEvenAlongsideRuleJSON(t *testing.T) {
	for _, name := range []string{"rule.md", "pattern.txt", "name.txt", "Rule.md"} {
		t.Run(name, func(t *testing.T) {
			cfg := fixture(t)
			write(t, filepath.Join(cfg.RulesPath, "R001", name), "old")
			_, err := Load(context.Background(), cfg)
			if err == nil || !strings.Contains(err.Error(), "旧形式") {
				t.Fatalf("legacy file not explicitly rejected: %v", err)
			}
			var diagnostic *DiagnosticError
			if !errors.As(err, &diagnostic) || diagnostic.RuleID != "R001" || diagnostic.Section != "" {
				t.Fatalf("legacy rejection lost its structured rule destination: %#v", diagnostic)
			}
		})
	}
}

func TestLegacyPatternDiagnosticTargetsFiltering(t *testing.T) {
	cfg := fixture(t)
	cfg.LegacyPath = filepath.Join(t.TempDir(), "legacy-symbols.txt")
	write(t, cfg.LegacyPath, "[")
	_, err := Load(context.Background(), cfg)
	var diagnostic *DiagnosticError
	if !errors.As(err, &diagnostic) || diagnostic.RuleID != "" || diagnostic.Section != "filtering" {
		t.Fatalf("legacy pattern rejection lost its filtering destination: %v", err)
	}
}
