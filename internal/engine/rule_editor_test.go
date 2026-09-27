package engine

import (
	"onebyone/internal/model"
	"onebyone/internal/store"
	"path/filepath"
	"strings"
	"testing"
)

func ruleEdit(id, title, pattern string) model.RuleEdit {
	return model.RuleEdit{ID: id, Name: title, Description: "変更の意図と適用条件", Body: "# 自由な見出し\n\n対象だけを修正する", ContentPattern: pattern}
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
	if err != nil || !view.ReadOnly || view.LockOwner == "" || view.Rule.Summary != edit.Description {
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
	archive := filepath.Join(base, "replacement.json")
	rulePackageFixture(t, archive, "Legacy")
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
	if err != nil || check.Rule.Summary != newer.Description || check.Rule.ContentPattern != newer.ContentPattern || check.Rule.Title != newer.Name {
		t.Fatal("revision conflict changed saved rule content")
	}
	// The same lease may save after the user deliberately reconciles with the
	// latest revision; there is no permanent conflict state or lock loss.
	draft.ExpectedRevision = latest.Revision
	if _, err := first.SaveRule(draft); err != nil {
		t.Fatalf("reconciled edit could not save: %v", err)
	}
}
