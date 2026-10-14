package catalog

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/text/encoding/japanese"
	"onebyone/internal/model"
	"onebyone/internal/rulepack"
)

func TestSolutionMatchingIntersectsFrozenSourceAndInitialBuildDiagnostics(t *testing.T) {
	cfg := fixture(t)
	saveRules(t, cfg, &rulepack.Package{})
	writeRule(t, cfg, "COMMON", model.RuleDefinition{Name: "common", Description: "preserve behavior", Body: "Preserve unrelated behavior."})
	writeRule(t, cfg, "MATCH", model.RuleDefinition{Name: "match", Description: "repair", Body: "Replace legacy API.", PathPattern: "src/**/*.vb", ContentPattern: "legacy", DiagnosticPattern: `^BC30456\b.*Missing`})
	writeRule(t, cfg, "DISABLED", model.RuleDefinition{Name: "disabled", Description: "disabled", Body: "Never selected."})
	writeRule(t, cfg, "NO_MATCH", model.RuleDefinition{Name: "absent", Description: "absent", Body: "Never selected.", ContentPattern: "not present"})
	cfg.ExcludedRuleIDs = []string{"DISABLED"}
	files := []string{"App.sln", "Src/A.VB", "Src/B.vb", "Src/C.vb", "Src/Other.vb", "Outside/A.vb"}
	for _, file := range append(append([]string{}, files...), "Src/NotInSolution.vb") {
		text := "LEGACY.Call()"
		if file == "Src/Other.vb" {
			text = "Modern.Call()"
		}
		write(t, filepath.Join(cfg.Root, file), text)
	}
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	diagnostics := []model.SourceDiagnostic{
		{File: "Src/A.VB", Code: "BC30456", Message: "Missing member"},
		{File: "Src/B.vb", Code: "bc30456", Message: "Missing member"},
		{File: "Src/C.vb", Code: "BC30456", Message: "Other member"},
		{File: "Src/C.vb", Code: "BC99999", Message: "Missing member"},
		{File: "Src/Other.vb", Code: "BC30456", Message: "Missing member"},
		{File: "Outside/A.vb", Code: "BC30456", Message: "Missing member"},
		{File: "Src/NotInSolution.vb", Code: "BC30456", Message: "Missing member"},
	}
	rules, scopes, err := c.MatchSolutionRules(context.Background(), cfg.Root, files, diagnostics)
	if err != nil {
		t.Fatal(err)
	}
	if len(rules) != 2 || len(scopes["COMMON"]) != len(files) || !reflect.DeepEqual(scopes["MATCH"], []string{"Src/A.VB"}) || scopes["NO_MATCH"] != nil || scopes["DISABLED"] != nil {
		t.Fatalf("wrong intersection or authorized scope: %+v %+v", rules, scopes)
	}
	if rules[1].CandidateCount != 1 || c.Rules[1].CandidateCount != 0 {
		t.Fatal("matching mutated catalog or omitted counts")
	}
	write(t, filepath.Join(cfg.Root, "Src/A.VB"), "Modern.Call()")
	diagnostics[0].Message = "now repaired"
	if !reflect.DeepEqual(scopes["MATCH"], []string{"Src/A.VB"}) || rules[1].ContentPattern != "legacy" {
		t.Fatal("returned frozen scope changed with source or diagnostics")
	}
}

func TestSolutionMatchingPreservesEncodingAndExplicitCaseOverride(t *testing.T) {
	cfg := fixture(t)
	saveRules(t, cfg, &rulepack.Package{})
	writeRule(t, cfg, "CASE", model.RuleDefinition{Name: "case", Description: "case", ContentPattern: `(?-i)請求\.Save`, Body: "Keep explicit case."})
	writeRule(t, cfg, "FOLD", model.RuleDefinition{Name: "fold", Description: "fold", ContentPattern: `請求\.Save`, Body: "Ignore case."})
	write(t, filepath.Join(cfg.Root, "UTF8.vb"), "請求.SAVE()")
	encoded, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte("請求.Save()"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(cfg.Root, "SJIS.vb"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	_, scopes, err := c.MatchSolutionRules(context.Background(), cfg.Root, []string{"UTF8.vb", "SJIS.vb"}, nil)
	if err != nil || !reflect.DeepEqual(scopes["CASE"], []string{"SJIS.vb"}) || !reflect.DeepEqual(scopes["FOLD"], []string{"SJIS.vb", "UTF8.vb"}) {
		t.Fatalf("encoding/case semantics changed: %+v %v", scopes, err)
	}
}

func TestSolutionMatchingRejectsInvalidAuthorizedFiles(t *testing.T) {
	cfg := fixture(t)
	write(t, filepath.Join(cfg.Root, "A.vb"), "A")
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, files := range [][]string{{"A.vb", "A.vb"}, {"missing.vb"}, {"../outside.vb"}, {"A\\B.vb"}} {
		if _, _, err := c.MatchSolutionRules(context.Background(), cfg.Root, files, nil); err == nil {
			t.Fatalf("accepted invalid target list: %v", files)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.MatchSolutionRules(ctx, cfg.Root, []string{"A.vb"}, nil); err != context.Canceled {
		t.Fatalf("cancellation was lost: %v", err)
	}
}

func TestSolutionMatchingLegacyThreeFieldRulesRemainCommon(t *testing.T) {
	cfg := fixture(t)
	cfg.Mode = "solution"
	write(t, cfg.RulesPath, `{"rules":[{"id":"legacy","description":"Keep behavior","content":"Preserve existing behavior across the solution."}]}`)
	write(t, filepath.Join(cfg.Root, "App.sln"), "solution")
	write(t, filepath.Join(cfg.Root, "A.vb"), "Module A\nEnd Module")
	c, err := Load(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	rules, scopes, err := c.MatchSolutionRules(context.Background(), cfg.Root, []string{"App.sln", "A.vb"}, nil)
	if err != nil || len(rules) != 1 || !rules[0].Always || rules[0].PathPattern != "" || rules[0].ContentPattern != "" || rules[0].DiagnosticPattern != "" || !reflect.DeepEqual(scopes["legacy"], []string{"A.vb", "App.sln"}) {
		t.Fatalf("legacy solution rule was not treated as common: %+v %+v %v", rules, scopes, err)
	}
}
