package engine

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestWorkspaceIsolationProtectsSourceReferencedByUnreadableSettings(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("healthy", root)
	if err != nil {
		t.Fatal(err)
	}
	managed := filepath.Dir(workspaceSettingPath(t, s, first.ActiveWorkspaceID))
	second, err := s.CreateWorkspace("old workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	// Older settings may reference app-owned data as a source. An unsupported
	// field must prevent adopting those settings without hiding their paths
	// from the checks that protect another workspace's source directory.
	legacy := second.Config
	legacy.Root = managed
	if err = s.writeWorkspaceSetting(model.Workspace{ID: second.ActiveWorkspaceID, Name: "old workspace", Root: managed}, legacy); err != nil {
		t.Fatal(err)
	}
	setting := workspaceSettingPath(t, s, second.ActiveWorkspaceID)
	var saved map[string]any
	if err = json.Unmarshal([]byte(readTest(t, setting)), &saved); err != nil {
		t.Fatal(err)
	}
	saved["config"].(map[string]any)["rulePackageName"] = "old.oborules"
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, setting, data)
	if _, _, err = s.readWorkspaceSetting(second.ActiveWorkspaceID); err == nil || !strings.Contains(err.Error(), "rulePackageName") {
		t.Fatalf("unsupported settings should remain unreadable: %v", err)
	}
	if _, err = s.SelectWorkspace(first.ActiveWorkspaceID); err != nil {
		t.Fatalf("unrelated unsupported settings prevented healthy selection: %v", err)
	}
	before := workspaceTree(t, managed)
	if _, err = s.DeleteWorkspace(first.ActiveWorkspaceID); err == nil || !strings.Contains(err.Error(), "重なっている") {
		t.Fatalf("deletion must preserve the source referenced by unreadable settings: %v", err)
	}
	assertWorkspaceTree(t, managed, before)
	if readTest(t, setting) != string(data) {
		t.Fatal("the old workspace settings were rewritten during source protection")
	}
	if s.Snapshot().ActiveWorkspaceID != first.ActiveWorkspaceID {
		t.Fatal("rejected deletion changed the healthy active workspace")
	}
}
