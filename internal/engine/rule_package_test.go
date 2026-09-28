package engine

import (
	"bytes"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func rulePackageService(t *testing.T) (*Service, string, string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "app", "private"))
	for _, key := range []string{"OPENAI_API_KEY", "AZURE_OPENAI_API_KEY", "AZURE_OPENAI_AUTH_TOKEN", "ANTHROPIC_API_KEY"} {
		t.Setenv(key, "")
	}
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "A.txt"), []byte("Legacy.Save()\n"), 0600); err != nil {
		t.Fatal(err)
	}
	initTargetTestGit(t, source)
	s := New(filepath.Join(base, "app", "app-settings.json"))
	t.Cleanup(s.Close)
	if _, err := s.CreateWorkspace("rules", source); err != nil {
		t.Fatal(err)
	}
	return s, source, base
}

func ruleDefinitionBytes(t *testing.T, definition model.RuleDefinition) []byte {
	t.Helper()
	data, err := ruleformat.Encode(definition)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func rulePackageSelectConnection(t *testing.T, s *Service) string {
	t.Helper()
	st, err := s.SaveLLMConnection(model.LLMConnection{Name: "test LLM", Provider: "openai", Endpoint: "https://personal.example.invalid", Deployment: "personal-model", AuthMode: "api_key", Credential: "FAKE-personal-rule-package-key"})
	if err != nil {
		t.Fatal(err)
	}
	id := st.LLMConnections[len(st.LLMConnections)-1].ID
	if _, err = s.SelectLLMConnection(id); err != nil {
		t.Fatal(err)
	}
	return id
}

func assertRulePackageSourceUntouched(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 2 || entries[0].Name() != ".git" || !entries[0].IsDir() || entries[1].Name() != "A.txt" {
		t.Fatalf("workspace/package operation wrote into source: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(root, "A.txt"))
	if err != nil || string(body) != "Legacy.Save()\n" {
		t.Fatal("source content changed during package operation")
	}
}

func rulePackageFixture(t *testing.T, path, pattern string) *rulepack.Package {
	t.Helper()
	data := ruleDefinitionBytes(t, model.RuleDefinition{ID: "R019", Name: "Legacy.Save を変換する", Description: "契約を維持し新APIへ変更", ContentPattern: pattern, Body: "# 変換内容\n\nSaveをwriteに変える"})
	p := &rulepack.Package{Rules: []rulepack.Entry{{ID: "R019", Markdown: string(data)}}}
	if e := rulepack.Write(path, p); e != nil {
		t.Fatal(e)
	}
	return p
}
func assertWorkspaceExecutionSettings(t *testing.T, actual, expected model.Config) {
	t.Helper()
	values := func(c model.Config) []any {
		return []any{c.InputPricePerMillion, c.CachedInputPricePerMillion, c.OutputPricePerMillion}
	}
	if !reflect.DeepEqual(values(actual), values(expected)) {
		t.Fatalf("workspace execution settings changed: got %v, want %v", values(actual), values(expected))
	}
}

func TestRuleJSONImportExportSnapshotAndRestart(t *testing.T) {
	s, source, base := rulePackageService(t)
	connection := rulePackageSelectConnection(t, s)
	cfg := s.Snapshot().Config
	cfg.InputPricePerMillion, cfg.OutputPricePerMillion = 1, 2
	if _, e := s.SaveConfig(cfg); e != nil {
		t.Fatal(e)
	}
	input := filepath.Join(base, "rules.json")
	want := rulePackageFixture(t, input, "Legacy")
	st, e := s.ImportRulePackage(input, "replace")
	if e != nil {
		t.Fatal(e)
	}
	if len(st.Rules) != 1 || st.Rules[0].ID != want.Rules[0].ID || filepath.Base(st.Config.RulesPath) != "rules.json" || st.SelectedLLMConnectionID != connection {
		t.Fatal("import identity or workspace changed")
	}
	assertWorkspaceExecutionSettings(t, st.Config, cfg)
	original, e := os.ReadFile(input)
	if e != nil {
		t.Fatal(e)
	}
	output := filepath.Join(base, "export.json")
	if _, e = s.ExportRulePackage(output); e != nil {
		t.Fatal(e)
	}
	exported, e := os.ReadFile(output)
	if e != nil || !bytes.Equal(original, exported) {
		t.Fatal("raw markdown export changed")
	}
	s.Close()
	reopened := New(s.configPath)
	defer reopened.Close()
	if reopened.Snapshot().Rules[0].ID != st.Rules[0].ID {
		t.Fatal("rule id changed on restart")
	}
	assertRulePackageSourceUntouched(t, source)
}
func TestRuleJSONFailedImportLeavesSavedDefinitionsUntouched(t *testing.T) {
	s, _, base := rulePackageService(t)
	input := filepath.Join(base, "rules.json")
	rulePackageFixture(t, input, "Legacy")
	before, e := s.ImportRulePackage(input, "replace")
	if e != nil {
		t.Fatal(e)
	}
	bad := filepath.Join(base, "bad.json")
	rulePackageFixture(t, bad, "[")
	if _, e = s.ImportRulePackage(bad, "replace"); e == nil {
		t.Fatal("invalid regex imported")
	}
	if s.Snapshot().Config.RulesPath != before.Config.RulesPath {
		t.Fatal("failed import changed active definitions")
	}
	if _, e = s.ImportRulePackage(input, "invalid"); e == nil {
		t.Fatal("invalid mode accepted")
	}
}
func TestRuleJSONDuplicateOwnsIndependentAssets(t *testing.T) {
	s, source, base := rulePackageService(t)
	input := filepath.Join(base, "rules.json")
	rulePackageFixture(t, input, "Legacy")
	first, e := s.ImportRulePackage(input, "replace")
	if e != nil {
		t.Fatal(e)
	}
	copy, e := s.DuplicateWorkspace("copy")
	if e != nil {
		t.Fatal(e)
	}
	if copy.Config.RulesPath == first.Config.RulesPath || copy.Rules[0].ID != first.Rules[0].ID {
		t.Fatal("duplicate assets or identities invalid")
	}
	before, _ := os.ReadFile(copy.Config.RulesPath)
	if e = os.WriteFile(first.Config.RulesPath, []byte(`{"rules":[]}`), 0600); e != nil {
		t.Fatal(e)
	}
	after, _ := os.ReadFile(copy.Config.RulesPath)
	if !bytes.Equal(before, after) {
		t.Fatal("duplicate shares files")
	}
	assertRulePackageSourceUntouched(t, source)
}
