package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

func TestRulePackageMergePreservesRulesAndSettingsWithPortableSuffixes(t *testing.T) {
	s, source, base := rulePackageService(t)
	cfg := s.Snapshot().Config
	cfg.IncludeGlobs = []string{"*.txt"}
	cfg.CheckCommands = []model.Command{{Name: "existing check", Executable: "never-run-on-import"}}
	original := rulePackageFixture(t, filepath.Join(base, "original.oborules"), rulepack.FromConfig(cfg), "Legacy")
	before, err := s.ImportRulePackage(filepath.Join(base, "original.oborules"), "replace")
	if err != nil {
		t.Fatal(err)
	}
	incoming := &rulepack.Package{Settings: rulepack.FromConfig(DefaultConfig()), Files: map[string][]byte{}}
	for _, id := range []string{"r019", "R019_2", "R200"} {
		incoming.Files["rules/"+id+"/rule.json"] = ruleDefinitionBytes(t, editDefinition(ruleEdit(id, "incoming "+id, "Modern")))
		incoming.Files["rules/"+id+"/examples/helper.txt"] = []byte("helper for " + id)
	}
	incoming.Files["patterns/legacy-symbols.txt"] = []byte("\ufeff\\bLegacy\\b\r\nModern\n")
	path := filepath.Join(base, "incoming.oborules")
	if err := rulepack.Write(path, incoming); err != nil {
		t.Fatal(err)
	}
	archiveBefore, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	merged, err := s.ImportRulePackage(path, "merge")
	if err != nil {
		t.Fatal(err)
	}
	if len(merged.Rules) != 4 || merged.Config.RulePackageName != before.Config.RulePackageName || merged.Config.QueuePath != before.Config.QueuePath || !reflect.DeepEqual(rulepack.FromConfig(merged.Config), rulepack.FromConfig(before.Config)) {
		t.Fatal("merge lost existing rules, filtering/check settings, workspace output, or package name")
	}
	assertWorkspaceExecutionSettings(t, merged.Config, before.Config)
	packed, err := rulepack.Snapshot(merged.Config.RulesPath, merged.Config.LegacyPath, rulepack.FromConfig(merged.Config))
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range original.Files {
		if name != "patterns/legacy-symbols.txt" && !bytes.Equal(packed.Files[name], data) {
			t.Fatalf("merge changed an existing definition or helper: %s", name)
		}
		old, err := os.ReadFile(filepath.Join(filepath.Dir(before.Config.RulesPath), filepath.FromSlash(name)))
		if err != nil || !bytes.Equal(old, data) {
			t.Fatalf("merge modified a previous package revision: %s", name)
		}
	}
	for _, item := range []struct{ input, output string }{{"r019", "r019_3"}, {"R019_2", "R019_2"}, {"R200", "R200"}} {
		for _, suffix := range []string{"rule.json", "examples/helper.txt"} {
			if !bytes.Equal(packed.Files["rules/"+item.output+"/"+suffix], incoming.Files["rules/"+item.input+"/"+suffix]) {
				t.Fatalf("renamed rule and helper assets are inconsistent: %s / %s", item.input, suffix)
			}
		}
		rule, err := s.ReadRule(item.output)
		if err != nil || rule.ID != item.output || rule.Title != "incoming "+item.input {
			t.Fatalf("renamed directory did not retain its title and new catalog ID: %+v, %v", rule, err)
		}
	}
	if string(packed.Files["patterns/legacy-symbols.txt"]) != "\\bLegacy\\b\nModern\n" {
		t.Fatalf("legacy patterns were not merged without duplicates: %q", packed.Files["patterns/legacy-symbols.txt"])
	}
	archiveAfter, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(archiveBefore, archiveAfter) {
		t.Fatal("merge changed its imported archive")
	}
	exported := filepath.Join(base, "merged.oborules")
	if _, err := s.ExportRulePackage(exported); err != nil {
		t.Fatal(err)
	}
	reread, err := rulepack.Read(exported)
	if err != nil || !reflect.DeepEqual(reread.Files, packed.Files) {
		t.Fatalf("merge assets changed on export: %v", err)
	}
	s.Close()
	reloaded := New(s.configPath)
	t.Cleanup(reloaded.Close)
	if st := reloaded.Snapshot(); st.LastError != "" || len(st.Rules) != 4 || !reflect.DeepEqual(rulepack.FromConfig(st.Config), rulepack.FromConfig(before.Config)) {
		t.Fatal("merged rules/settings did not persist across restart")
	}
	assertRulePackageSourceUntouched(t, source)
}

func TestRulePackageMergeFailureLeavesPackageAndOpenEditorUntouched(t *testing.T) {
	s, source, base := rulePackageService(t)
	good := filepath.Join(base, "good.oborules")
	rulePackageFixture(t, good, rulepack.FromConfig(DefaultConfig()), "Legacy")
	before, err := s.ImportRulePackage(good, "replace")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.OpenRule("R019"); err != nil {
		t.Fatal(err)
	}
	lease := s.ruleLease
	setting := workspaceSettingPath(t, s, before.ActiveWorkspaceID)
	settingsBefore, err := os.ReadFile(setting)
	if err != nil {
		t.Fatal(err)
	}
	packages := filepath.Dir(filepath.Dir(before.Config.RulesPath))
	packageBefore := workspaceTree(t, packages)
	bad := filepath.Join(base, "bad.oborules")
	rulePackageFixture(t, bad, rulepack.FromConfig(DefaultConfig()), "[invalid")
	if _, err := s.ImportRulePackage(bad, "merge"); err == nil {
		t.Fatal("invalid merged pattern was published")
	}
	settingsAfter, err := os.ReadFile(setting)
	if err != nil || !bytes.Equal(settingsBefore, settingsAfter) || !reflect.DeepEqual(s.Snapshot().Config, before.Config) || s.ruleLease != lease {
		t.Fatal("failed merge changed settings, active state, or the editor lease")
	}
	// Staging creates and removes an entry in the package parent, so compare
	// the earlier revisions' contents rather than the parent's mtime.
	packageAfter := workspaceTree(t, packages)
	delete(packageBefore, ".")
	delete(packageAfter, ".")
	if !reflect.DeepEqual(packageBefore, packageAfter) {
		t.Fatal("failed merge left staging files or modified an earlier revision")
	}
	assertRulePackageSourceUntouched(t, source)
}

func TestRulePackageImportHonorsRuleLocksAndExplicitMode(t *testing.T) {
	s, _, base := rulePackageService(t)
	path := filepath.Join(base, "incoming.oborules")
	rulePackageFixture(t, path, rulepack.FromConfig(DefaultConfig()), "Legacy")
	before, err := s.ImportRulePackage(path, "replace")
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := s.ruleLockPath(before.ActiveWorkspaceID, "R019")
	if err != nil {
		t.Fatal(err)
	}
	lease, owner, err := store.AcquireWorkspace(lockPath, store.WorkspaceOwner{Owner: "other editor", Host: "test"})
	if err != nil || owner != nil {
		t.Fatalf("cannot acquire independent rule lease: %v", err)
	}
	defer lease.Release()
	for _, mode := range []string{"replace", "merge"} {
		if _, err := s.ImportRulePackage(path, mode); err == nil || !strings.Contains(err.Error(), "other editor") {
			t.Fatalf("%s ignored the active editor: %v", mode, err)
		}
	}
	if _, err := s.ImportRulePackage(path, ""); err == nil || !strings.Contains(err.Error(), "取り込み方法") {
		t.Fatal("import proceeded without an explicit overwrite/merge choice")
	}
	if !reflect.DeepEqual(s.Snapshot().Config, before.Config) {
		t.Fatal("rejected imports changed the saved configuration")
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
	if st, err := s.ImportRulePackage(path, "merge"); err != nil || len(st.Rules) != 2 {
		t.Fatalf("merge did not recover after the editor closed: %v", err)
	}
}

func TestMergedRuleIDsReserveOriginalsAndRespectMaximumLength(t *testing.T) {
	long := strings.Repeat("R", 64)
	inputs := []string{"R019", "R019_2", "R019_3", long}
	names := mergedRuleIDs([]string{"r019", "r019_3", long}, inputs)
	if names["R019"] != "R019_4" || names["R019_2"] != "R019_2" || names["R019_3"] != "R019_3_2" || names[long] != strings.Repeat("R", 62)+"_2" {
		t.Fatalf("collision suffixes were not stable and portable: %+v", names)
	}
	for _, id := range names {
		if !editableRuleID.MatchString(id) {
			t.Fatalf("suffix created a noneditable rule ID: %s", id)
		}
	}
}
