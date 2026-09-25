package engine

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
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

func rulePackageFixture(t *testing.T, path string, settings rulepack.Settings, pattern string) *rulepack.Package {
	t.Helper()
	p := &rulepack.Package{Settings: settings, Files: map[string][]byte{
		"rules/R019/rule.json":          ruleDefinitionBytes(t, model.RuleDefinition{Version: 1, Name: "Legacy.Save を変換する", Overview: "Legacy.Saveを新しい呼び出しにする。", Before: "Legacy.Save()", After: "Modern.save()", Notes: "呼出し順を変えない。", HoldConditions: "複数ファイルの同時編集が必要。", Pattern: pattern}),
		"rules/R019/examples/after.txt": []byte("Modern.save()\n"),
		"patterns/legacy-symbols.txt":   []byte("\\bLegacy\\b\n"),
	}}
	if err := rulepack.Write(path, p); err != nil {
		t.Fatal(err)
	}
	return p
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

func TestRulePackageImportExportLocalStorageAndRestart(t *testing.T) {
	s, source, base := rulePackageService(t)
	connectionID := rulePackageSelectConnection(t, s)
	workspaceConfig := s.Snapshot().Config
	workspaceConfig.MaxAttempts, workspaceConfig.MaxTurns, workspaceConfig.MaxOutputTokens, workspaceConfig.MaxFileBytes, workspaceConfig.TimeoutSeconds = 2, 11, 4096, 65536, 180
	workspaceConfig.MaxCostUSD, workspaceConfig.InputPricePerMillion, workspaceConfig.CachedInputPricePerMillion, workspaceConfig.OutputPricePerMillion = 3, 4, 0.4, 12
	before, err := s.SaveConfig(workspaceConfig)
	if err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.IncludeGlobs = []string{"*.txt"}
	cfg.ExcludeGlobs = []string{"generated/**"}
	cfg.RGPath = filepath.Join(base, "this-program-must-not-run")
	cfg.CheckCommands = []model.Command{{Name: "must not run on import", Executable: cfg.RGPath, Args: []string{"--fixture"}}}
	cfg.MaxTurns, cfg.MaxOutputTokens, cfg.TimeoutSeconds = 5, 2048, 120
	cfg.MaxCostUSD, cfg.InputPricePerMillion, cfg.CachedInputPricePerMillion, cfg.OutputPricePerMillion = 1.5, 3, 0.3, 10
	original := filepath.Join(base, "example.oborules")
	want := rulePackageFixture(t, original, rulepack.FromConfig(cfg), "\\bLegacy\\b")
	st, err := s.ImportRulePackage(original, "replace")
	if err != nil {
		t.Fatal(err)
	}
	if st.Config.RulePackageName != "example.oborules" || st.Config.Root != before.Config.Root || st.Config.QueuePath != before.Config.QueuePath || st.SelectedLLMConnectionID != connectionID || !st.Config.CredentialSet || st.Config.Credential != "" {
		t.Fatal("import changed target, output, connection, or exposed its key")
	}
	if !reflect.DeepEqual(rulepack.FromConfig(st.Config), want.Settings) || len(st.Rules) != 1 || len(st.Tasks) != 0 {
		t.Fatal("import did not load settings/rule preview without scanning")
	}
	assertWorkspaceExecutionSettings(t, st.Config, workspaceConfig)
	settingPath, err := s.workspacePath(st.ActiveWorkspaceID, "setting.json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(st.Config.RulesPath, filepath.Join(filepath.Dir(settingPath), "rule-packages")+string(filepath.Separator)) || strings.HasPrefix(st.Config.RulesPath, source+string(filepath.Separator)) {
		t.Fatal("package assets are not local to this workspace")
	}
	assertRulePackageSourceUntouched(t, source)
	containedExport := filepath.Join(filepath.Dir(settingPath), "must-not-create.oborules")
	// macOS' standard /var alias must not bypass the managed-directory guard.
	containedExport = strings.Replace(containedExport, "/private/var/", "/var/", 1)
	if _, err := s.ExportRulePackage(containedExport); err == nil {
		t.Fatal("export could write into managed workspace assets")
	}
	if _, err := os.Stat(containedExport); !os.IsNotExist(err) {
		t.Fatal("rejected export created an archive")
	}
	if err := os.Remove(original); err != nil {
		t.Fatal(err)
	}
	// Rule settings are exported; execution settings remain in the workspace.
	// Removing the originally selected archive is harmless to both.
	edited := st.Config
	edited.MaxTurns = 7
	edited.IncludeGlobs = []string{"**/*.txt"}
	if _, err = s.SaveConfig(edited); err != nil {
		t.Fatal(err)
	}
	export := filepath.Join(base, "edited.oborules")
	if returned, err := s.ExportRulePackage(export); err != nil || returned != export {
		t.Fatalf("export failed: %v", err)
	}
	packed, err := rulepack.Read(export)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(packed.Settings, rulepack.FromConfig(edited)) || !reflect.DeepEqual(packed.Files, want.Files) {
		t.Fatal("export lost edited settings or helper assets")
	}
	encoded, err := json.Marshal(packed.Settings)
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{source, before.Config.QueuePath, connectionID, "personal.example.invalid", "FAKE-personal", "maxAttempts", "maxTurns", "maxOutputTokens", "maxFileBytes", "timeoutSeconds", "maxCostUSD", "inputPricePerMillion", "cachedInputPricePerMillion", "outputPricePerMillion"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("exported package settings include workspace or LLM data")
		}
	}
	s.Close()
	reloaded := New(s.configPath)
	t.Cleanup(reloaded.Close)
	after := reloaded.Snapshot()
	if after.LastError != "" || after.Config.RulePackageName != st.Config.RulePackageName || after.Config.MaxTurns != 7 || after.Config.RulesPath != st.Config.RulesPath || len(after.Rules) != 1 || after.SelectedLLMConnectionID != connectionID || !after.Config.CredentialSet {
		t.Fatalf("local package did not restore independently: %s", after.LastError)
	}
	assertWorkspaceExecutionSettings(t, after.Config, edited)
	assertRulePackageSourceUntouched(t, source)
}

func TestRulePackageFailedImportKeepsSettingsAndPriorFiles(t *testing.T) {
	s, source, base := rulePackageService(t)
	good := filepath.Join(base, "good.oborules")
	rulePackageFixture(t, good, rulepack.FromConfig(DefaultConfig()), "Legacy")
	st, err := s.ImportRulePackage(good, "replace")
	if err != nil {
		t.Fatal(err)
	}
	setting, err := s.workspacePath(st.ActiveWorkspaceID, "setting.json")
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(setting)
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Dir(st.Config.RulesPath)
	parent = filepath.Dir(parent)
	entriesBefore, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(base, "invalid.oborules")
	rulePackageFixture(t, bad, rulepack.FromConfig(DefaultConfig()), "[invalid")
	if _, err = s.ImportRulePackage(bad, "replace"); err == nil {
		t.Fatal("invalid regex package was adopted")
	}
	after, err := os.ReadFile(setting)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed import changed prior settings")
	}
	entriesAfter, err := os.ReadDir(parent)
	if err != nil || len(entriesAfter) != len(entriesBefore) {
		t.Fatal("failed import retained its staging files")
	}
	current := s.Snapshot()
	if !reflect.DeepEqual(current.Config, st.Config) || len(current.Rules) != 1 || current.Rules[0].Pattern != "Legacy" {
		t.Fatal("failed import changed active rules")
	}
	if _, err = os.ReadFile(filepath.Join(st.Config.RulesPath, "R019", "rule.json")); err != nil {
		t.Fatal("failed import removed an earlier package")
	}
	assertRulePackageSourceUntouched(t, source)
}

func TestRulePackageNewImportRequiresScanAcrossRestart(t *testing.T) {
	s, _, base := rulePackageService(t)
	rulePackageSelectConnection(t, s)
	first := filepath.Join(base, "first.oborules")
	rulePackageFixture(t, first, rulepack.FromConfig(DefaultConfig()), "Legacy")
	if _, err := s.ImportRulePackage(first, "replace"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(base, "same-rules-new-filter.oborules")
	cfg := DefaultConfig()
	cfg.IncludeGlobs = []string{"*.go"}
	rulePackageFixture(t, second, rulepack.FromConfig(cfg), "Legacy")
	if _, err := s.ImportRulePackage(second, "replace"); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(1); err == nil || !strings.Contains(err.Error(), "対象抽出") {
		t.Fatalf("old queue could run under new package filters: %v", err)
	}
	s.Close()
	reloaded := New(s.configPath)
	t.Cleanup(reloaded.Close)
	if err := reloaded.Start(1); err == nil || !strings.Contains(err.Error(), "対象抽出") {
		t.Fatalf("restart lost imported-package extraction requirement: %v", err)
	}
}

func TestRulePackageDuplicateOwnsIndependentRuleAssets(t *testing.T) {
	s, source, base := rulePackageService(t)
	archive := filepath.Join(base, "rules.oborules")
	rulePackageFixture(t, archive, rulepack.FromConfig(DefaultConfig()), "Legacy")
	first, err := s.ImportRulePackage(archive, "replace")
	if err != nil {
		t.Fatal(err)
	}
	copy, err := s.DuplicateWorkspace("independent")
	if err != nil {
		t.Fatal(err)
	}
	if copy.Config.RulesPath == first.Config.RulesPath || copy.Config.LegacyPath == first.Config.LegacyPath || copy.ActiveWorkspaceID == first.ActiveWorkspaceID {
		t.Fatal("duplicate shares the original package files")
	}
	originalRule := filepath.Join(first.Config.RulesPath, "R019", "rule.json")
	copyRule := filepath.Join(copy.Config.RulesPath, "R019", "rule.json")
	before, err := os.ReadFile(copyRule)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(originalRule, ruleDefinitionBytes(t, model.RuleDefinition{Version: 1, Name: "original changed"}), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(copyRule)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("editing original rule assets changed the duplicate")
	}
	assertRulePackageSourceUntouched(t, source)
}

func assertWorkspaceExecutionSettings(t *testing.T, actual, expected model.Config) {
	t.Helper()
	values := func(c model.Config) []any {
		return []any{c.MaxAttempts, c.MaxTurns, c.MaxOutputTokens, c.MaxFileBytes, c.TimeoutSeconds, c.MaxCostUSD, c.InputPricePerMillion, c.CachedInputPricePerMillion, c.OutputPricePerMillion}
	}
	if !reflect.DeepEqual(values(actual), values(expected)) {
		t.Fatalf("workspace execution settings changed: got %v, want %v", values(actual), values(expected))
	}
}

func TestRulePackageBudgetsStayIndependentAcrossImportDuplicateSwitchAndRestart(t *testing.T) {
	s, _, base := rulePackageService(t)
	firstID := s.Snapshot().ActiveWorkspaceID
	firstConfig := s.Snapshot().Config
	firstConfig.MaxAttempts, firstConfig.MaxTurns, firstConfig.MaxOutputTokens, firstConfig.MaxFileBytes, firstConfig.TimeoutSeconds = 1, 6, 2048, 32768, 120
	firstConfig.MaxCostUSD, firstConfig.InputPricePerMillion, firstConfig.CachedInputPricePerMillion, firstConfig.OutputPricePerMillion = 1, 2, 0.2, 3
	if _, err := s.SaveConfig(firstConfig); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "shared.oborules")
	rulePackageFixture(t, archive, rulepack.FromConfig(DefaultConfig()), "Legacy")
	if _, err := s.ImportRulePackage(archive, "replace"); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.DuplicateWorkspace("different budget")
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceExecutionSettings(t, duplicate.Config, firstConfig)
	secondConfig := duplicate.Config
	secondConfig.MaxAttempts, secondConfig.MaxTurns, secondConfig.MaxOutputTokens, secondConfig.MaxFileBytes, secondConfig.TimeoutSeconds = 3, 17, 4096, 65536, 300
	secondConfig.MaxCostUSD, secondConfig.InputPricePerMillion, secondConfig.CachedInputPricePerMillion, secondConfig.OutputPricePerMillion = 9, 7, 0.7, 20
	if _, err = s.SaveConfig(secondConfig); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		id     string
		config model.Config
	}{{firstID, firstConfig}, {duplicate.ActiveWorkspaceID, secondConfig}} {
		selected, err := s.SelectWorkspace(item.id)
		if err != nil {
			t.Fatal(err)
		}
		assertWorkspaceExecutionSettings(t, selected.Config, item.config)
		imported, err := s.ImportRulePackage(archive, "replace")
		if err != nil {
			t.Fatal(err)
		}
		assertWorkspaceExecutionSettings(t, imported.Config, item.config)
	}
	s.Close()
	restarted := New(s.configPath)
	t.Cleanup(restarted.Close)
	assertWorkspaceExecutionSettings(t, restarted.Snapshot().Config, secondConfig)
	firstAgain, err := restarted.SelectWorkspace(firstID)
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceExecutionSettings(t, firstAgain.Config, firstConfig)
}

func TestRulePackageUnsupportedStoredRulesRemainUntouchedAndCanBeReplaced(t *testing.T) {
	s, source, base := rulePackageService(t)
	initial := s.Snapshot()
	oldFolder := filepath.Join(base, "unsupported-rules")
	writeTest(t, filepath.Join(oldFolder, "R019", "rule.md"), []byte("# previous Markdown rule\n"))
	writeTest(t, filepath.Join(oldFolder, "R019", "pattern.txt"), []byte("Legacy"))
	writeTest(t, filepath.Join(oldFolder, "R019", "name.txt"), []byte("previous separate title"))
	oldTree := workspaceTree(t, oldFolder)
	stored := initial.Config
	stored.RulesPath = oldFolder
	if err := s.writeWorkspaceSetting(model.Workspace{ID: initial.ActiveWorkspaceID, Name: "saved old settings", Root: source}, stored); err != nil {
		t.Fatal(err)
	}
	s.Close()
	restarted := New(s.configPath)
	t.Cleanup(restarted.Close)
	if state := restarted.Snapshot(); state.ActiveWorkspaceID != initial.ActiveWorkspaceID || state.LastError == "" || len(state.Rules) != 0 {
		t.Fatal("unsupported persisted rules were silently accepted or hid the workspace")
	}
	assertWorkspaceTree(t, oldFolder, oldTree)
	path := filepath.Join(base, "replacement.oborules")
	rulePackageFixture(t, path, rulepack.FromConfig(DefaultConfig()), "Legacy")
	replaced, err := restarted.ImportRulePackage(path, "replace")
	if err != nil || replaced.LastError != "" || len(replaced.Rules) != 1 || replaced.Config.RulesPath == oldFolder {
		t.Fatalf("a supported package could not replace rejected old rules: %v", err)
	}
	assertWorkspaceTree(t, oldFolder, oldTree)
	assertRulePackageSourceUntouched(t, source)
}
