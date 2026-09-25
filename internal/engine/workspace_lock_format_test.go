package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/store"
)

func legacyWorkspaceFixture(t *testing.T) (*Service, model.Workspace, string, workspaceSetting) {
	t.Helper()
	s, source := workspaceTestService(t)
	created, err := s.CreateWorkspace("existing workspace", source)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	path := workspaceSettingPath(t, s, created.ActiveWorkspaceID)
	var saved workspaceSetting
	if err = decodeLocalJSON(path, &saved); err != nil {
		t.Fatal(err)
	}
	saved.Version = 1
	if err = store.WriteJSON(path, saved); err != nil {
		t.Fatal(err)
	}
	return s, created.Workspaces[0], path, saved
}

func TestWorkspaceLockFormatKeepsOldEditorReadOnlyThenMigratesWithoutChangingSettings(t *testing.T) {
	original, w, setting, before := legacyWorkspaceFixture(t)
	oldPath, err := original.workspacePath(w.ID, ".edit.lock")
	if err != nil {
		t.Fatal(err)
	}
	oldLease, owner, err := store.AcquireWorkspace(oldPath, store.WorkspaceOwner{Owner: "old application"})
	if err != nil || owner != nil || oldLease == nil {
		t.Fatalf("could not simulate an older editing instance: %v", err)
	}
	defer oldLease.Release()
	s := workspaceNewService(t, original.configPath)
	if st := s.Snapshot(); !st.ReadOnly || st.WorkspaceLock == nil || st.WorkspaceLock.Owner != "old application" || st.ActiveWorkspaceID != w.ID {
		t.Fatalf("new lock namespace bypassed the older editor: %+v", st.WorkspaceLock)
	}
	if _, err = s.SaveConfig(s.Snapshot().Config); err == nil {
		t.Fatal("settings could be written while the older editor was active")
	}
	var after workspaceSetting
	if err = decodeLocalJSON(setting, &after); err != nil || !reflect.DeepEqual(before, after) {
		t.Fatalf("read-only opening migrated or changed the older settings: %v", err)
	}
	if err = oldLease.Release(); err != nil {
		t.Fatal(err)
	}
	selected, err := s.SelectWorkspace(w.ID)
	if err != nil || selected.ReadOnly {
		t.Fatalf("workspace did not migrate after the older editor closed: %v", err)
	}
	if err = decodeLocalJSON(setting, &after); err != nil {
		t.Fatal(err)
	}
	before.Version = workspaceSettingVersion
	if !reflect.DeepEqual(before, after) || after.Version == 1 {
		t.Fatal("migration changed settings or did not reject the old version reader")
	}
	// An older instance might have read version 1 before this transition. Its
	// activation path acquires the old lease and re-reads the version before
	// adopting editing authority, so it must now reject these settings.
	oldLease, owner, err = store.AcquireWorkspace(oldPath, store.WorkspaceOwner{Owner: "stale old opener"})
	if err != nil || owner != nil || oldLease == nil {
		t.Fatalf("migration did not release the old lease: %v", err)
	}
	if err = decodeLocalJSON(setting, &after); err != nil || after.Version == 1 {
		t.Fatalf("stale old opener could accept migrated settings: %v", err)
	}
	if err = oldLease.Release(); err != nil {
		t.Fatal(err)
	}
	if _, err = s.DeleteWorkspace(w.ID); err != nil {
		t.Fatalf("migrated workspace retained a blocking old handle: %v", err)
	}
}

func TestWorkspaceLockFormatFailureReleasesBothLeasesWithoutPublishing(t *testing.T) {
	original, w, setting, saved := legacyWorkspaceFixture(t)
	// A corrupt older document must fail under both leases, without being
	// silently normalized into the new format or leaving either lock held.
	saved.Config.Credential = "invalid-workspace-credential"
	if err := store.WriteJSON(setting, saved); err != nil {
		t.Fatal(err)
	}
	before := readTest(t, setting)
	if lease, _, _, err := original.obtainWorkspaceLease(w); err == nil || lease != nil {
		if lease != nil {
			_ = lease.Release()
		}
		t.Fatal("invalid workspace was migrated into an editable state")
	}
	if got := readTest(t, setting); got != before {
		t.Fatal("failed migration changed the settings")
	}
	external, err := original.workspaceLockPath(w.ID, ".edit.lock")
	if err != nil {
		t.Fatal(err)
	}
	old, err := original.workspacePath(w.ID, ".edit.lock")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{external, old} {
		lease, owner, err := store.AcquireWorkspace(path, store.WorkspaceOwner{Owner: "after failure"})
		if lease != nil {
			_ = lease.Release()
		}
		if err != nil || owner != nil || lease == nil {
			t.Fatalf("failed migration left a lease held: %s: %v", path, err)
		}
	}
}

func TestWorkspaceLockFormatDoesNotCreateMissingWorkspaceDirectories(t *testing.T) {
	s, source := workspaceTestService(t)
	w := model.Workspace{ID: uid(), Name: "not created", Root: source}
	lease, owner, fresh, err := s.obtainWorkspaceLease(w)
	if err != nil || lease == nil || owner != nil || !fresh {
		t.Fatalf("new workspace could not obtain its external lease: %v", err)
	}
	defer lease.Release()
	setting, err := s.workspacePath(w.ID, "setting.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Lstat(filepath.Dir(setting)); !os.IsNotExist(err) {
		t.Fatal("acquiring a lease recreated the missing workspace directory")
	}
}
