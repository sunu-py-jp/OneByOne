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
	"onebyone/internal/store"
)

func ruleEdit(id, title, pattern string) model.RuleEdit {
	return model.RuleEdit{ID: id, Name: title, Overview: "Legacy.Saveの呼び出しを修正する。", Before: "Legacy.Save()", After: "Modern.save()", Notes: "他のファイルは編集しない。", HoldConditions: "呼出し元の修正が必要。", Pattern: pattern}
}

func TestRuleEditorCreatesCommonAndIndividualRulesAndEditsOwnedCopy(t *testing.T) {
	s, source, base := rulePackageService(t)
	initial := s.Snapshot()
	common := ruleEdit("R001", "共通の注意", "")
	first, err := s.CreateRule(common)
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Rules) != 1 || !first.Rules[0].Always || first.Rules[0].Title != "共通の注意" || first.Config.RulePackageName == "" || first.Config.QueuePath != initial.Config.QueuePath || first.Config.Root != initial.Config.Root {
		t.Fatal("first rule did not initialize a local common-rule package")
	}
	individual := ruleEdit("R019", "Legacy.Saveを更新", `\.Save\s*\(`)
	second, err := s.CreateRule(individual)
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Rules) != 2 || second.Rules[1].Always || second.Config.RulesPath == first.Config.RulesPath {
		t.Fatal("individual rule did not create a complete new package version")
	}
	if _, err := s.SaveRule(common); err == nil {
		t.Fatal("save accepted a rule whose lease was released by changing editors")
	}
	opened, err := s.OpenRule("R001")
	if err != nil || opened.ReadOnly || opened.Rule.Overview != common.Overview || opened.Rule.Before != common.Before || opened.Rule.After != common.After {
		t.Fatalf("common editor failed to acquire its lease: %v", err)
	}
	updated := ruleEdit("R001", "変更した共通ルール", "")
	updated.ExpectedRevision = opened.Revision
	saved, err := s.SaveRule(updated)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Config.RulesPath == second.Config.RulesPath || saved.Rules[0].Title != "変更した共通ルール" || len(saved.Rules) != 2 || saved.Rules[1].ID != "R019" {
		t.Fatal("save changed unrelated rules or reused mutable resources")
	}
	old, err := os.ReadFile(filepath.Join(second.Config.RulesPath, "R001", "rule.json"))
	if err != nil || !bytes.Equal(old, ruleDefinitionBytes(t, editDefinition(common))) {
		t.Fatal("save modified an earlier package version")
	}
	archive := filepath.Join(base, "edited-rules.oborules")
	if _, err := s.ExportRulePackage(archive); err != nil {
		t.Fatal(err)
	}
	pack, err := rulepack.Read(archive)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pack.Files["rules/R001/rule.json"], ruleDefinitionBytes(t, editDefinition(updated))) || !bytes.Equal(pack.Files["rules/R019/rule.json"], ruleDefinitionBytes(t, editDefinition(individual))) {
		t.Fatal("export lost edited rule files")
	}
	for name := range pack.Files {
		if strings.Contains(name, "lock") {
			t.Fatal("editor lock leaked into exported rule assets")
		}
	}
	assertRulePackageSourceUntouched(t, source)
}

func TestRuleEditorFailedSaveKeepsSettingsRulesAndLease(t *testing.T) {
	s, source, _ := rulePackageService(t)
	edit := ruleEdit("R019", "existing", "Legacy")
	before, err := s.CreateRule(edit)
	if err != nil {
		t.Fatal(err)
	}
	settingPath, err := s.workspacePath(before.ActiveWorkspaceID, "setting.json")
	if err != nil {
		t.Fatal(err)
	}
	settings, err := os.ReadFile(settingPath)
	if err != nil {
		t.Fatal(err)
	}
	versions := filepath.Join(filepath.Dir(settingPath), "rule-packages")
	oldEntries, err := os.ReadDir(versions)
	if err != nil {
		t.Fatal(err)
	}
	edit.ExpectedRevision = ruleformat.Revision(editDefinition(edit))
	invalid := edit
	invalid.Pattern, invalid.Overview = "[invalid", "must not replace the saved rule"
	if _, err = s.SaveRule(invalid); err == nil {
		t.Fatal("invalid regex was saved")
	}
	current, err := os.ReadFile(settingPath)
	if err != nil || !bytes.Equal(settings, current) {
		t.Fatal("failed save changed workspace settings")
	}
	newEntries, err := os.ReadDir(versions)
	if err != nil || len(oldEntries) != len(newEntries) {
		t.Fatal("failed save retained an incomplete version")
	}
	if !reflect.DeepEqual(before.Config, s.Snapshot().Config) {
		t.Fatal("failed save changed active config")
	}
	fixed := ruleEdit("R019", "fixed", "Modern")
	fixed.ExpectedRevision = edit.ExpectedRevision
	if _, err := s.SaveRule(fixed); err != nil {
		t.Fatalf("failed save lost the active editor lease: %v", err)
	}
	if _, err := s.CreateRule(ruleEdit("r019", "case collision", "")); err == nil {
		t.Fatal("case-fold duplicate rule ID accepted")
	}
	if _, err := s.CreateRule(ruleEdit("../escape", "path escape", "")); err == nil {
		t.Fatal("rule ID escaped the package")
	}
	if err := s.CloseRule(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveRule(edit); err == nil {
		t.Fatal("closed editor retained write authority")
	}
	assertRulePackageSourceUntouched(t, source)
}

func TestRuleEditorOtherProcessLeaseAndReadOnlyService(t *testing.T) {
	s, source, _ := rulePackageService(t)
	edit := ruleEdit("R001", "locked", "")
	created, err := s.CreateRule(edit)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CloseRule(); err != nil {
		t.Fatal(err)
	}
	lockPath, err := s.ruleLockPath(created.ActiveWorkspaceID, edit.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherLease, _, err := store.AcquireWorkspace(lockPath, store.WorkspaceOwner{Owner: "other editor", Host: "other host"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = otherLease.Release() })
	opened, err := s.OpenRule(edit.ID)
	if err != nil || !opened.ReadOnly || opened.LockOwner != "other editor" || opened.LockHost != "other host" {
		t.Fatalf("per-rule OS lock was not reflected in the editor: %v", err)
	}
	if _, err := s.SaveRule(ruleEdit(edit.ID, "must not save", "")); err == nil {
		t.Fatal("busy rule was writable")
	}
	if err := otherLease.Release(); err != nil {
		t.Fatal(err)
	}
	opened, err = s.OpenRule(edit.ID)
	if err != nil || opened.ReadOnly {
		t.Fatalf("released rule could not be acquired: %v", err)
	}
	peer := New(s.configPath)
	t.Cleanup(peer.Close)
	view, err := peer.OpenRule(edit.ID)
	if err != nil || !view.ReadOnly || view.LockOwner == "" || view.Rule.Overview != edit.Overview {
		t.Fatalf("later service did not show rule and owner read-only: %v", err)
	}
	if _, err := peer.SaveRule(edit); err == nil {
		t.Fatal("read-only workspace service saved a rule")
	}
	s.Close()
	if _, err = peer.SelectWorkspace(created.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if view, err = peer.OpenRule(edit.ID); err != nil || view.ReadOnly {
		t.Fatalf("closed service left rule locked: %v", err)
	}
	changed := ruleEdit(edit.ID, "new editor", "")
	changed.ExpectedRevision = view.Revision
	if _, err := peer.SaveRule(changed); err != nil {
		t.Fatal(err)
	}
	assertRulePackageSourceUntouched(t, source)
}

func TestRuleEditorSwitchImportAndRunningReleaseOrRejectAsRequired(t *testing.T) {
	s, source, base := rulePackageService(t)
	edit := ruleEdit("R001", "first", "")
	created, err := s.CreateRule(edit)
	if err != nil {
		t.Fatal(err)
	}
	lockPath, err := s.ruleLockPath(created.ActiveWorkspaceID, edit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateWorkspace("second", source); err != nil {
		t.Fatal(err)
	}
	if owner, err := store.PeekWorkspace(lockPath); err != nil || owner != nil {
		t.Fatalf("workspace switch retained editor lease: %v", err)
	}
	if _, err = s.SelectWorkspace(created.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.OpenRule(edit.ID); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	if _, err := s.SaveRule(edit); err == nil {
		t.Fatal("rule saved during execution")
	}
	if _, err := s.CreateRule(ruleEdit("R002", "new", "")); err == nil {
		t.Fatal("rule created during execution")
	}
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	archive := filepath.Join(base, "replacement.oborules")
	rulePackageFixture(t, archive, rulepack.FromConfig(DefaultConfig()), "Legacy")
	if _, err = s.ImportRulePackage(archive, "replace"); err != nil {
		t.Fatal(err)
	}
	if owner, err := store.PeekWorkspace(lockPath); err != nil || owner != nil {
		t.Fatalf("package import retained a stale editor lease: %v", err)
	}
	if _, err := s.SaveRule(edit); err == nil {
		t.Fatal("editor lease survived replacement of its package")
	}
}

func TestRuleEditorRejectsOldDraftAfterReacquiringLease(t *testing.T) {
	first, source, _ := rulePackageService(t)
	original := ruleEdit("R001", "original", "")
	created, err := first.CreateRule(original)
	if err != nil {
		t.Fatal(err)
	}
	initial, err := first.OpenRule(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	draft := ruleEdit(original.ID, "old draft should not overwrite newer work", "")
	draft.ExpectedRevision = initial.Revision
	if err := first.CloseRule(); err != nil {
		t.Fatal(err)
	}
	if _, err := first.CreateWorkspace("away", source); err != nil {
		t.Fatal(err)
	}
	second := New(first.configPath)
	t.Cleanup(second.Close)
	if _, err := second.SelectWorkspace(created.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	otherEditor, err := second.OpenRule(original.ID)
	if err != nil || otherEditor.ReadOnly {
		t.Fatalf("second editor could not acquire released workspace/rule: %v", err)
	}
	newer := ruleEdit(original.ID, "newer saved change", "Modern")
	newer.ExpectedRevision = otherEditor.Revision
	if _, err := second.SaveRule(newer); err != nil {
		t.Fatal(err)
	}
	second.Close()
	if _, err := first.SelectWorkspace(created.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	latest, err := first.OpenRule(original.ID)
	if err != nil || latest.ReadOnly || latest.Revision == draft.ExpectedRevision {
		t.Fatalf("reopened editor did not get the new revision: %v", err)
	}
	if _, err := first.SaveRule(draft); err == nil || !strings.Contains(err.Error(), "他の編集で更新") {
		t.Fatalf("stale draft overwrote newer saved changes: %v", err)
	}
	check, err := first.OpenRule(original.ID)
	if err != nil || check.Rule.Overview != newer.Overview || check.Rule.Pattern != newer.Pattern || check.Rule.Title != newer.Name {
		t.Fatal("revision conflict changed saved rule content")
	}
	// The same lease may save after the user deliberately reconciles with the
	// latest revision; there is no permanent conflict state or lock loss.
	draft.ExpectedRevision = latest.Revision
	if _, err := first.SaveRule(draft); err != nil {
		t.Fatalf("reconciled edit could not save: %v", err)
	}
}

func TestRuleEditorStoresSeparateFieldsWithoutMarkdownAndRejectsMultilinePattern(t *testing.T) {
	s, source, _ := rulePackageService(t)
	edit := model.RuleEdit{ID: "R019", Name: "independent fields", Overview: "概要の説明\n# 変更前 という見出し自体の説明", Before: "old(\"value\");\r\n// ``` example fence\r\n", After: "await current(\"value\");\n", Notes: "# 備考\nこの文字列は入力内容の一部。", HoldConditions: "判断を保留する条件\n複数ファイルが必要。", Pattern: `\.Save\s*\(`}
	created, err := s.CreateRule(edit)
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(created.Config.RulesPath, edit.ID)
	entries, err := os.ReadDir(folder)
	if err != nil || len(entries) != 1 || entries[0].Name() != "rule.json" {
		t.Fatalf("structured rule was not stored as a single JSON definition: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(folder, "rule.json"))
	if err != nil {
		t.Fatal(err)
	}
	definition, err := ruleformat.Decode(data)
	if err != nil || !reflect.DeepEqual(definition, editDefinition(edit)) {
		t.Fatalf("saving parsed or mixed separately entered fields: %v", err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(data, &keys); err != nil || len(keys) != 8 || keys["markdown"] != nil || keys["body"] != nil {
		t.Fatal("saved definition includes generated Markdown or obsolete body metadata")
	}
	opened, err := s.OpenRule(edit.ID)
	if err != nil || !reflect.DeepEqual(opened.Rule, ruleformat.ToRule(edit.ID, editDefinition(edit))) {
		t.Fatalf("editor did not restore every field independently: %v", err)
	}
	invalid := edit
	invalid.ExpectedRevision, invalid.Pattern = opened.Revision, "Legacy\nModern"
	if _, err := s.SaveRule(invalid); err == nil || !strings.Contains(err.Error(), "1行") {
		t.Fatalf("multiline pattern was accepted: %v", err)
	}
	if s.Snapshot().Config.RulesPath != created.Config.RulesPath || s.ruleLease == nil {
		t.Fatal("rejected multiline pattern changed the package or editor ownership")
	}
	// Editing one field keeps all other input text exact, while the full
	// structured definition participates in stale-draft detection.
	updated := edit
	updated.ExpectedRevision, updated.Notes = opened.Revision, "新しい備考\n# 説明内の見出し"
	if _, err := s.SaveRule(updated); err != nil {
		t.Fatal(err)
	}
	stale := edit
	stale.ExpectedRevision = opened.Revision
	if _, err := s.SaveRule(stale); err == nil {
		t.Fatal("old draft overwrote a separately updated notes field")
	}
	s.Close()
	restarted := New(s.configPath)
	t.Cleanup(restarted.Close)
	state := restarted.Snapshot()
	if state.LastError != "" || len(state.Rules) != 1 || !reflect.DeepEqual(state.Rules[0], ruleformat.ToRule(edit.ID, editDefinition(updated))) {
		t.Fatalf("restart did not restore all independently saved fields: %s", state.LastError)
	}
	assertRulePackageSourceUntouched(t, source)
}
