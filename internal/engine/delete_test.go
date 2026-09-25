package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/store"
)

func TestDeleteWorkspaceRetainsManagedResultsAndNeverTouchesExternalData(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	first := s.Snapshot()
	managed := filepath.Dir(workspaceSettingPath(t, s, first.ActiveWorkspaceID))
	writeTest(t, filepath.Join(managed, "runs", "modified-worktree", "A.txt"), []byte("valuable uncommitted edit"))
	second, err := s.CreateWorkspace("next workspace", cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.SelectWorkspace(first.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.OpenRule("R001"); err != nil {
		t.Fatal(err)
	}
	sourceBefore, rulesBefore := workspaceTree(t, cfg.Root), workspaceTree(t, cfg.RulesPath)
	queueBefore := workspaceTree(t, filepath.Dir(cfg.QueuePath))
	managedBefore := workspaceTree(t, managed)
	deleted, err := s.DeleteWorkspace(first.ActiveWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if deleted.ActiveWorkspaceID != second.ActiveWorkspaceID || len(deleted.Workspaces) != 1 || s.leaseID != second.ActiveWorkspaceID {
		t.Fatal("deletion did not select and lock the next workspace")
	}
	if _, err := os.Stat(managed); !os.IsNotExist(err) {
		t.Fatal("deleted workspace still exists in the active workspace directory")
	}
	trash := filepath.Join(filepath.Dir(s.configPath), "workspace-trash")
	entries, err := os.ReadDir(trash)
	if err != nil || len(entries) != 1 {
		t.Fatalf("deleted workspace was not retained in local trash: %v", err)
	}
	assertWorkspaceTree(t, filepath.Join(trash, entries[0].Name()), managedBefore)
	assertWorkspaceTree(t, cfg.Root, sourceBefore)
	assertWorkspaceTree(t, cfg.RulesPath, rulesBefore)
	assertWorkspaceTree(t, filepath.Dir(cfg.QueuePath), queueBefore)
	if _, err = s.SelectWorkspace(first.ActiveWorkspaceID); err == nil {
		t.Fatal("deleted workspace could be selected again")
	}
	if _, err = os.Stat(managed); !os.IsNotExist(err) {
		t.Fatal("reopening a deleted workspace recreated its directory")
	}
	empty, err := s.DeleteWorkspace(second.ActiveWorkspaceID)
	if err != nil || empty.ActiveWorkspaceID != "" || len(empty.Workspaces) != 0 || s.workspaceLease != nil || s.ruleLease != nil || empty.Config.Root != "" || len(empty.LLMConnections) != len(first.LLMConnections) {
		t.Fatalf("deleting last workspace did not leave an empty app with personal connections: %v", err)
	}
	restored := workspaceNewService(t, s.configPath)
	if st := restored.Snapshot(); st.ActiveWorkspaceID != "" || len(st.Workspaces) != 0 {
		t.Fatal("deleted workspaces reappeared after restart")
	}
}

func TestWorkspaceLeasesStayExclusiveDuringArchiveAndRollback(t *testing.T) {
	s, source := workspaceTestService(t)
	created, err := s.CreateWorkspace("archive exclusion", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRule(ruleEdit("R001", "kept open", "")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.OpenRule("R001"); err != nil {
		t.Fatal(err)
	}
	workspaceLock, err := s.workspaceLockPath(created.ActiveWorkspaceID, ".edit.lock")
	if err != nil {
		t.Fatal(err)
	}
	ruleLock, err := s.ruleLockPath(created.ActiveWorkspaceID, "R001")
	if err != nil {
		t.Fatal(err)
	}
	managed := filepath.Dir(workspaceSettingPath(t, s, created.ActiveWorkspaceID))
	for _, path := range []string{workspaceLock, ruleLock} {
		if isAtOrWithin(managed, path) {
			t.Fatal("an editing lease is inside the directory being archived")
		}
	}
	root, err := os.OpenRoot(filepath.Dir(s.configPath))
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Mkdir("workspace-trash", 0700); err != nil {
		t.Fatal(err)
	}
	from, to := "workspaces/"+created.ActiveWorkspaceID, "workspace-trash/"+created.ActiveWorkspaceID
	if err = root.Rename(from, to); err != nil {
		t.Fatalf("could not archive a workspace while both editing leases are held: %v", err)
	}
	archived := true
	defer func() {
		if archived {
			_ = root.Rename(to, from)
		}
	}()
	assertExclusive := func() {
		t.Helper()
		for _, path := range []string{workspaceLock, ruleLock} {
			lease, owner, err := store.AcquireWorkspace(path, store.WorkspaceOwner{Owner: "competing editor"})
			if lease != nil {
				_ = lease.Release()
			}
			if err != nil || lease != nil || owner == nil {
				t.Fatalf("archive or rollback released editing exclusion at %s: %v", path, err)
			}
		}
	}
	assertExclusive()
	if _, _, err = s.readWorkspaceSetting(created.ActiveWorkspaceID); !os.IsNotExist(err) {
		t.Fatalf("a stale opener could still read the archived workspace: %v", err)
	}
	if err = root.Rename(to, from); err != nil {
		t.Fatalf("rollback failed with editing leases held: %v", err)
	}
	archived = false
	assertExclusive()
	if _, _, err = s.readWorkspaceSetting(created.ActiveWorkspaceID); err != nil {
		t.Fatalf("rollback did not restore the workspace settings: %v", err)
	}
}

func TestDeleteWorkspaceSelectionFailureRestoresWorkspaceAndKeepsLeases(t *testing.T) {
	s, source := workspaceTestService(t)
	created, err := s.CreateWorkspace("rollback", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.CreateRule(ruleEdit("R001", "kept open", "")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.OpenRule("R001"); err != nil {
		t.Fatal(err)
	}
	workspaceLease, ruleLease := s.workspaceLease, s.ruleLease
	managed := filepath.Dir(workspaceSettingPath(t, s, created.ActiveWorkspaceID))
	before := workspaceTree(t, managed)
	// Publishing the new selection fails after the directory has moved. This
	// exercises the real rollback path, including Windows' open-handle rules.
	if err = os.Remove(s.configPath); err != nil {
		t.Fatal(err)
	}
	if err = os.Mkdir(s.configPath, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteWorkspace(created.ActiveWorkspaceID); err == nil {
		t.Fatal("workspace deletion succeeded despite failing selection persistence")
	}
	if s.workspaceLease != workspaceLease || s.ruleLease != ruleLease || s.Snapshot().ActiveWorkspaceID != created.ActiveWorkspaceID {
		t.Fatal("failed deletion changed editing authority or active workspace")
	}
	assertWorkspaceTree(t, managed, before)
	for _, name := range []string{".edit.lock", "rule-locks/r001.lock"} {
		path, err := s.workspaceLockPath(created.ActiveWorkspaceID, name)
		if err != nil {
			t.Fatal(err)
		}
		lease, owner, err := store.AcquireWorkspace(path, store.WorkspaceOwner{Owner: "competing editor"})
		if lease != nil {
			_ = lease.Release()
		}
		if err != nil || lease != nil || owner == nil {
			t.Fatalf("failed deletion released editing exclusion: %v", err)
		}
	}
}

func TestDeletionRejectsStaleWorkspaceRunningAndOtherWorkspaceOwner(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWorkspace("second", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteWorkspace(first.ActiveWorkspaceID); err == nil {
		t.Fatal("stale deletion removed a workspace other than the active one")
	}
	if _, err = s.CreateRule(ruleEdit("R001", "rule", "")); err != nil {
		t.Fatal(err)
	}
	other := workspaceNewService(t, s.configPath)
	if !other.Snapshot().ReadOnly {
		t.Fatal("fixture did not acquire a read-only workspace")
	}
	if _, err = other.DeleteWorkspace(second.ActiveWorkspaceID); err == nil {
		t.Fatal("other workspace owner could delete workspace")
	}
	if _, err = other.DeleteRule("R001"); err == nil {
		t.Fatal("other workspace owner could delete rule")
	}
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	if _, err = s.DeleteWorkspace(second.ActiveWorkspaceID); err == nil {
		t.Fatal("running workspace could be deleted")
	}
	if _, err = s.DeleteRule("R001"); err == nil {
		t.Fatal("running rule could be deleted")
	}
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	if st := s.Snapshot(); st.ActiveWorkspaceID != second.ActiveWorkspaceID || len(st.Workspaces) != 2 || len(st.Rules) != 1 {
		t.Fatal("rejected deletion changed state")
	}
}

func TestDeleteWorkspaceRejectsManagedDirectoryUsedAsSource(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	managed := filepath.Dir(workspaceSettingPath(t, s, first.ActiveWorkspaceID))
	second, err := s.CreateWorkspace("workspace directory as source", root)
	if err != nil {
		t.Fatal(err)
	}
	// Legacy settings can still contain an unsafe target even though current
	// folder selection rejects it. Deletion must continue to protect that data.
	legacy := second.Config
	legacy.Root = managed
	if err = s.writeWorkspaceSetting(model.Workspace{ID: second.ActiveWorkspaceID, Name: "legacy target", Root: managed}, legacy); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SelectWorkspace(first.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	before := workspaceTree(t, managed)
	if _, err = s.DeleteWorkspace(first.ActiveWorkspaceID); err == nil {
		t.Fatal("deletion moved another workspace's source folder")
	}
	assertWorkspaceTree(t, managed, before)
}

func TestDeleteRuleCopiesRemainingRulesAndKeepsRunHistory(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	writeTest(t, filepath.Join(cfg.RulesPath, "R019", "examples.txt"), []byte("unchanged auxiliary asset"))
	before := s.Snapshot()
	rulesBefore, sourceBefore := workspaceTree(t, cfg.RulesPath), workspaceTree(t, cfg.Root)
	queueBefore := workspaceTree(t, filepath.Dir(cfg.QueuePath))
	deleted, err := s.DeleteRule("R001")
	if err != nil {
		t.Fatal(err)
	}
	if len(deleted.Rules) != 1 || deleted.Rules[0].ID != "R019" || deleted.Config.RulesPath == cfg.RulesPath || !reflect.DeepEqual(before.Tasks, deleted.Tasks) {
		t.Fatal("rule removal changed history or failed to publish an independent remaining rule")
	}
	if readTest(t, filepath.Join(deleted.Config.RulesPath, "R019", "examples.txt")) != "unchanged auxiliary asset" {
		t.Fatal("remaining rule auxiliary asset was not preserved")
	}
	if _, err = os.Stat(filepath.Join(deleted.Config.RulesPath, "R001")); !os.IsNotExist(err) {
		t.Fatal("deleted ID remains in the new rule package")
	}
	assertWorkspaceTree(t, cfg.RulesPath, rulesBefore)
	assertWorkspaceTree(t, cfg.Root, sourceBefore)
	assertWorkspaceTree(t, filepath.Dir(cfg.QueuePath), queueBefore)
	if err = s.Start(1); err == nil {
		t.Fatal("rule deletion allowed stale extracted candidates to execute")
	}
}

func TestDeleteInvalidRulesOneAtATimeAndCreateAfterLastDeletion(t *testing.T) {
	s, _, base := rulePackageService(t)
	if _, err := s.CreateRule(ruleEdit("R001", "first", "")); err != nil {
		t.Fatal(err)
	}
	initial, err := s.CreateRule(ruleEdit("R019", "second", ""))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "original.oborules")
	if _, err = s.ExportRulePackage(archive); err != nil {
		t.Fatal(err)
	}
	archiveBefore := readTest(t, archive)
	for _, id := range []string{"R001", "R019"} {
		writeTest(t, filepath.Join(initial.Config.RulesPath, id, "name.txt"), []byte("old "+id))
	}
	original := workspaceTree(t, initial.Config.RulesPath)
	if _, err = s.GetState(); err != nil {
		t.Fatal(err)
	}
	firstRemoved, err := s.DeleteRule("R001")
	issues := workspaceIssues(t, firstRemoved, initial.ActiveWorkspaceID)
	if err != nil || len(issues) != 1 || issues[0].RuleID != "R019" || len(firstRemoved.Rules) != 0 {
		t.Fatalf("another invalid rule blocked selected removal or was silently repaired: %#v / %v", issues, err)
	}
	if readTest(t, filepath.Join(firstRemoved.Config.RulesPath, "R019", "name.txt")) != "old R019" {
		t.Fatal("deleting first invalid rule changed the remaining legacy asset")
	}
	lastRemoved, err := s.DeleteRule("R019")
	if err != nil || len(lastRemoved.Rules) != 0 || lastRemoved.Config.RulesPath != "" || len(workspaceIssues(t, lastRemoved, initial.ActiveWorkspaceID)) != 0 || lastRemoved.LastError != "" {
		t.Fatalf("last-rule removal did not leave a clean empty rule list: %v", err)
	}
	assertWorkspaceTree(t, initial.Config.RulesPath, original)
	if readTest(t, archive) != archiveBefore {
		t.Fatal("deleting rules modified the original archive")
	}
	reloaded, err := s.SelectWorkspace(initial.ActiveWorkspaceID)
	if err != nil || reloaded.LastError != "" || len(reloaded.Rules) != 0 {
		t.Fatalf("empty rule list became an error on reload: %v", err)
	}
	created, err := s.CreateRule(ruleEdit("R002", "new rule", ""))
	if err != nil || len(created.Rules) != 1 || created.Rules[0].ID != "R002" {
		t.Fatalf("could not create a new rule after deleting the last rule: %v", err)
	}
}

func TestDeleteRuleHonorsIndependentRuleLease(t *testing.T) {
	s, _, _ := rulePackageService(t)
	initial, err := s.CreateRule(ruleEdit("R001", "locked", ""))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.CloseRule(); err != nil {
		t.Fatal(err)
	}
	path, err := s.ruleLockPath(initial.ActiveWorkspaceID, "R001")
	if err != nil {
		t.Fatal(err)
	}
	lease, owner, err := store.AcquireWorkspace(path, currentWorkspaceOwner())
	if err != nil || owner != nil {
		t.Fatalf("could not lock fixture rule: %v", err)
	}
	defer lease.Release()
	before := workspaceTree(t, initial.Config.RulesPath)
	if _, err = s.DeleteRule("R001"); err == nil {
		t.Fatal("deletion bypassed another editor's rule lease")
	}
	assertWorkspaceTree(t, initial.Config.RulesPath, before)
	if s.Snapshot().Config.RulesPath != initial.Config.RulesPath {
		t.Fatal("failed deletion published a new rule package")
	}
}
