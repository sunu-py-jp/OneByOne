package engine

import (
	"testing"
)

func TestWorkspaceIsolationDeletionSelectsOnlyReadableWorkspace(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	invalid := writeInvalidWorkspaceSiblings(t, s, root)
	second, err := s.CreateWorkspace("second", root)
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteWorkspace(second.ActiveWorkspaceID)
	if err != nil || deleted.ActiveWorkspaceID != first.ActiveWorkspaceID || deleted.LastError != "" {
		t.Fatalf("invalid sibling interfered with deletion or next selection: %v, %s", err, deleted.LastError)
	}
	assertInvalidWorkspacesRemainIsolated(t, s, invalid)
	deleted, err = s.DeleteWorkspace(first.ActiveWorkspaceID)
	if err != nil || deleted.ActiveWorkspaceID != "" || deleted.LastError != "" || s.workspaceLease != nil {
		t.Fatalf("deleting the last readable workspace activated an invalid one: %v, %s", err, deleted.LastError)
	}
	assertInvalidWorkspacesRemainIsolated(t, s, invalid)
}

func TestWorkspaceIsolationRepairedSettingsClearOnlyTheirError(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("repairable", root)
	if err != nil {
		t.Fatal(err)
	}
	path := workspaceSettingPath(t, s, first.ActiveWorkspaceID)
	original := readTest(t, path)
	invalid := writeInvalidWorkspaceSiblings(t, s, root)
	second, err := s.CreateWorkspace("healthy", root)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, path, []byte(`{"name":"repairable","config":`))
	failed, err := s.SelectWorkspace(first.ActiveWorkspaceID)
	if err == nil || failed.ActiveWorkspaceID != second.ActiveWorkspaceID {
		t.Fatal("invalid selection replaced the current workspace")
	}
	found := false
	for _, workspace := range failed.Workspaces {
		if workspace.ID == first.ActiveWorkspaceID {
			for _, issue := range workspace.Issues {
				found = found || issue.ID == "settings"
			}
		}
	}
	if !found {
		t.Fatal("failed selection did not publish the workspace's settings error")
	}
	writeTest(t, path, []byte(original))
	selected, err := s.SelectWorkspace(first.ActiveWorkspaceID)
	if err != nil || selected.ActiveWorkspaceID != first.ActiveWorkspaceID {
		t.Fatalf("valid settings could not be reopened after repair: %v", err)
	}
	assertInvalidWorkspacesRemainIsolated(t, s, invalid)
}
