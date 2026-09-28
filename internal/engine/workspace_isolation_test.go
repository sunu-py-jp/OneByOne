package engine

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"

	"onebyone/internal/demopreset"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

type invalidWorkspaceFixture struct {
	id, path, body string
}

// These are existing, unreadable workspaces, not inputs for a migration. Their
// original settings must remain untouched while a different workspace is used.
func writeInvalidWorkspaceSiblings(t *testing.T, s *Service, root string) []invalidWorkspaceFixture {
	t.Helper()
	fixtures := []invalidWorkspaceFixture{}
	for _, kind := range []string{"retired field", "malformed JSON"} {
		id := uid()
		path := workspaceSettingPath(t, s, id)
		c := DefaultConfig()
		c.Root, c.QueuePath = root, filepath.Join(filepath.Dir(path), "runs", "queue.jsonl")
		if err := s.writeWorkspaceSetting(model.Workspace{ID: id, Name: kind, Root: root}, c); err != nil {
			t.Fatal(err)
		}
		body := `{"version":2,"config":`
		if kind == "retired field" {
			var saved map[string]any
			if err := json.Unmarshal([]byte(readTest(t, path)), &saved); err != nil {
				t.Fatal(err)
			}
			saved["config"].(map[string]any)["rulePackageName"] = "old.oborules"
			data, err := json.Marshal(saved)
			if err != nil {
				t.Fatal(err)
			}
			body = string(data)
		}
		writeTest(t, path, []byte(body))
		fixtures = append(fixtures, invalidWorkspaceFixture{id: id, path: path, body: body})
	}
	return fixtures
}

func assertInvalidWorkspacesRemainIsolated(t *testing.T, s *Service, fixtures []invalidWorkspaceFixture) {
	t.Helper()
	state := s.Snapshot()
	for _, fixture := range fixtures {
		if readTest(t, fixture.path) != fixture.body {
			t.Fatal("an unrelated operation rewrote or removed invalid workspace settings")
		}
		if _, _, err := s.readWorkspaceSetting(fixture.id); err == nil {
			t.Fatal("invalid settings became accepted through backward compatibility")
		}
		found := false
		for _, workspace := range state.Workspaces {
			if workspace.ID != fixture.id {
				continue
			}
			for _, issue := range workspace.Issues {
				if issue.ID == "settings" && issue.Message != "" {
					found = true
				}
			}
		}
		if !found {
			t.Fatalf("invalid workspace %s lost its own settings error in the list", fixture.id)
		}
	}
	if state.ActiveWorkspaceID != "" {
		for _, workspace := range state.Workspaces {
			if workspace.ID == state.ActiveWorkspaceID {
				for _, issue := range workspace.Issues {
					if issue.ID == "settings" {
						t.Fatal("a sibling's settings error was attached to the healthy active workspace")
					}
				}
			}
		}
	}
}

func TestWorkspaceIsolationKeepsHealthyCreationSelectionAndSavingIndependent(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("healthy first", root)
	if err != nil {
		t.Fatal(err)
	}
	invalid := writeInvalidWorkspaceSiblings(t, s, root)
	created, err := s.CreateWorkspace("healthy new", root)
	if err != nil {
		t.Fatalf("unrelated invalid settings blocked normal workspace creation: %v", err)
	}
	if len(created.Workspaces) != 4 || created.ActiveWorkspaceID == first.ActiveWorkspaceID {
		t.Fatal("new workspace was not selected or an existing workspace disappeared")
	}
	selected, err := s.SelectWorkspace(first.ActiveWorkspaceID)
	if err != nil {
		t.Fatalf("unrelated invalid settings blocked healthy selection: %v", err)
	}
	c := selected.Config
	c.Concurrency = 4
	saved, err := s.SaveConfig(c)
	if err != nil || saved.ActiveWorkspaceID != first.ActiveWorkspaceID || saved.Config.Concurrency != 4 {
		t.Fatalf("unrelated invalid settings blocked saving the healthy workspace: %v", err)
	}
	_, diskConfig, err := s.readWorkspaceSetting(first.ActiveWorkspaceID)
	if err != nil || diskConfig.Concurrency != 4 {
		t.Fatalf("healthy settings were not persisted: %v", err)
	}
	lease := s.workspaceLease
	if lease == nil || s.leaseID != first.ActiveWorkspaceID {
		t.Fatal("healthy workspace did not retain its edit lease")
	}
	for _, fixture := range invalid {
		if _, err = s.SelectWorkspace(fixture.id); err == nil {
			t.Fatal("invalid workspace selection was accepted")
		}
		current := s.Snapshot()
		if current.ActiveWorkspaceID != saved.ActiveWorkspaceID || !reflect.DeepEqual(current.Config, saved.Config) || s.workspaceLease != lease || s.leaseID != saved.ActiveWorkspaceID {
			t.Fatal("invalid selection changed the active workspace, its settings, or its edit lease")
		}
	}
	assertInvalidWorkspacesRemainIsolated(t, s, invalid)
}

func TestWorkspaceIsolationAllowsDemoCreationWithInvalidSiblings(t *testing.T) {
	s, root := workspaceTestService(t)
	invalid := writeInvalidWorkspaceSiblings(t, s, root)
	demoRoot, err := s.CreateDemoProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	created, err := s.CreateDemoWorkspace("healthy demo", demoRoot)
	if err != nil {
		t.Fatalf("unrelated invalid settings blocked demo workspace creation: %v", err)
	}
	if created.ActiveWorkspaceID == "" || created.Config.Root != demoRoot || len(created.Rules) != demopreset.CommonRuleCount+demopreset.IndividualRuleCount || len(created.Workspaces) != 3 {
		t.Fatal("demo workspace was not created with its bundled rules and existing workspace list")
	}
	if s.workspaceLease == nil || s.leaseID != created.ActiveWorkspaceID {
		t.Fatal("new demo did not own its edit lease")
	}
	assertInvalidWorkspacesRemainIsolated(t, s, invalid)
}

func TestWorkspaceIsolationRestartFallsBackFromInvalidActiveSettings(t *testing.T) {
	s, root := workspaceTestService(t)
	healthy, err := s.CreateWorkspace("healthy", root)
	if err != nil {
		t.Fatal(err)
	}
	invalid := writeInvalidWorkspaceSiblings(t, s, root)
	s.Close()
	for _, fixture := range invalid {
		if err := store.WriteJSON(s.configPath, appSettings{Version: 1, ActiveWorkspaceID: fixture.id}); err != nil {
			t.Fatal(err)
		}
		loaded := workspaceNewService(t, s.configPath)
		state := loaded.Snapshot()
		if state.ActiveWorkspaceID != healthy.ActiveWorkspaceID || state.LastError != "" || state.ReadOnly || loaded.workspaceLease == nil || loaded.leaseID != healthy.ActiveWorkspaceID {
			t.Fatalf("invalid saved selection prevented restoring the healthy workspace: active=%s, error=%s", state.ActiveWorkspaceID, state.LastError)
		}
		assertInvalidWorkspacesRemainIsolated(t, loaded, invalid)
		loaded.Close()
	}
}

func TestWorkspaceIsolationAllInvalidSettingsStillAllowNewWorkspace(t *testing.T) {
	s, root := workspaceTestService(t)
	invalid := writeInvalidWorkspaceSiblings(t, s, root)
	if err := store.WriteJSON(s.configPath, appSettings{Version: 1, ActiveWorkspaceID: invalid[0].id}); err != nil {
		t.Fatal(err)
	}
	s.Close()
	loaded := workspaceNewService(t, s.configPath)
	state := loaded.Snapshot()
	if state.ActiveWorkspaceID != "" || state.LastError != "" || loaded.workspaceLease != nil {
		t.Fatalf("all-invalid workspace list prevented a usable empty startup: active=%s, error=%s", state.ActiveWorkspaceID, state.LastError)
	}
	assertInvalidWorkspacesRemainIsolated(t, loaded, invalid)
	created, err := loaded.CreateWorkspace("start fresh", root)
	if err != nil || created.ActiveWorkspaceID == "" || len(created.Workspaces) != 3 {
		t.Fatalf("all-invalid existing settings blocked creating a fresh workspace: %v", err)
	}
	assertInvalidWorkspacesRemainIsolated(t, loaded, invalid)
}

func TestWorkspaceIsolationDoesNotOverwriteInvalidActiveSettings(t *testing.T) {
	s, root := workspaceTestService(t)
	created, err := s.CreateWorkspace("active", root)
	if err != nil {
		t.Fatal(err)
	}
	otherRoot := t.TempDir()
	initTargetTestGit(t, otherRoot)
	path := workspaceSettingPath(t, s, created.ActiveWorkspaceID)
	invalid := `{"version":2,"config":`
	writeTest(t, path, []byte(invalid))
	lease := s.workspaceLease
	c := created.Config
	c.Concurrency = 4
	for name, operation := range map[string]func() (model.State, error){
		"save":          func() (model.State, error) { return s.SaveConfig(c) },
		"change target": func() (model.State, error) { return s.ChangeTargetFolder(otherRoot) },
	} {
		if _, err := operation(); err == nil {
			t.Fatalf("%s accepted invalid settings for the active workspace", name)
		}
		state := s.Snapshot()
		if readTest(t, path) != invalid || state.ActiveWorkspaceID != created.ActiveWorkspaceID || !reflect.DeepEqual(state.Config, created.Config) || s.workspaceLease != lease {
			t.Fatalf("%s overwrote the invalid active settings or changed the active workspace", name)
		}
	}
}
