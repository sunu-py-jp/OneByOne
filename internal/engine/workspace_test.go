package engine

import (
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

func workspaceTestService(t *testing.T) (*Service, string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	for _, name := range []string{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_AUTH_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(name, "")
	}
	root := filepath.Join(base, "source")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	initTargetTestGit(t, root)
	return workspaceNewService(t, filepath.Join(base, "app", "app-settings.json")), root
}

func workspaceNewService(t *testing.T, path string) *Service {
	t.Helper()
	s := New(path)
	t.Cleanup(s.Close)
	return s
}

// Register through the personal API, then choose it for the active workspace.
// Workspace SaveConfig deliberately does not accept connection fields.

func workspaceSelectTestConnection(t *testing.T, s *Service, name string, c model.Config) model.State {
	t.Helper()
	st, err := s.SaveLLMConnection(model.LLMConnection{Name: name, Provider: c.Provider, Endpoint: c.Endpoint, Deployment: c.Deployment, AuthMode: c.AuthMode, Credential: c.Credential})
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, connection := range st.LLMConnections {
		if connection.Name == name {
			id = connection.ID
		}
	}
	if id == "" {
		t.Fatal("saved connection is absent from the personal list")
	}
	st, err = s.SelectLLMConnection(id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func assertPersonalCiphertexts(t *testing.T, directory string, values ...string) {
	t.Helper()
	found := false
	err := filepath.WalkDir(directory, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".json") {
			return nil
		}
		found = true
		body := readTest(t, path)
		for _, value := range values {
			if value != "" && strings.Contains(body, value) {
				t.Fatal("personal connection was stored in plaintext")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("no encrypted personal settings were written")
	}
}

// appBase is the local application's directory, never the source folder.
func settingsFile(appBase, id, name string) string {
	return filepath.Join(appBase, "workspaces", id, name)
}

func workspaceSettingPath(t *testing.T, s *Service, id string) string {
	t.Helper()
	path, err := s.workspacePath(id, "setting.json")
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestWorkspaceRequiresFolderAndCreatesOnlyLocalSettings(t *testing.T) {
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, "src", "A.txt"), []byte("source bytes must not change\n"))
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "source fixture")
	before := workspaceTree(t, root)
	if got := s.Snapshot(); len(got.Workspaces) != 0 || got.ActiveWorkspaceID != "" {
		t.Fatal("fresh application already has a workspace")
	}
	for _, invalid := range []string{"", "  ", filepath.Join(root, "missing"), filepath.Join(root, "src", "A.txt")} {
		if _, err := s.CreateWorkspace("new", invalid); err == nil {
			t.Fatalf("accepted target %q", invalid)
		}
	}
	if _, err := s.SaveConfig(DefaultConfig()); err == nil {
		t.Fatal("saved settings before choosing a target folder")
	}
	st, err := s.CreateWorkspace("  API更新  ", root)
	if err != nil {
		t.Fatal(err)
	}
	if !workspaceIDPattern.MatchString(st.ActiveWorkspaceID) || len(st.Workspaces) != 1 || st.Workspaces[0].Name != "API更新" || !sameRoot(st.Config.Root, root) {
		t.Fatal("creation lost identity, name, or target")
	}
	setting := workspaceSettingPath(t, s, st.ActiveWorkspaceID)
	expected := settingsFile(filepath.Dir(s.configPath), st.ActiveWorkspaceID, "setting.json")
	if !sameRoot(setting, expected) {
		t.Fatalf("settings are not under the local application: %s", setting)
	}
	queueRelative, err := filepath.Rel(filepath.Dir(setting), st.Config.QueuePath)
	if err != nil || queueRelative == ".." || strings.HasPrefix(queueRelative, ".."+string(filepath.Separator)) || filepath.Base(st.Config.QueuePath) != "queue.jsonl" {
		t.Fatal("default queue is not local to its workspace")
	}
	var document struct {
		Version int          `json:"version"`
		ID      string       `json:"id"`
		Name    string       `json:"name"`
		Config  model.Config `json:"config"`
	}
	if err := json.Unmarshal([]byte(readTest(t, setting)), &document); err != nil {
		t.Fatal(err)
	}
	if document.Version != workspaceSettingVersion || document.ID != st.ActiveWorkspaceID || document.Name != "API更新" || !sameRoot(document.Config.Root, root) {
		t.Fatal("setting.json did not contain complete workspace identity")
	}
	var app map[string]json.RawMessage
	if err := json.Unmarshal([]byte(readTest(t, s.configPath)), &app); err != nil {
		t.Fatal(err)
	}
	if len(app) != 2 || string(app["version"]) != "1" || string(app["activeWorkspaceId"]) != `"`+st.ActiveWorkspaceID+`"` {
		t.Fatal("app-settings.json should contain only version and active workspace ID")
	}
	assertWorkspaceTree(t, root, before)
}

func TestWorkspaceRestoresAllSettingsWithoutPlaintextConnection(t *testing.T) {
	s, root := workspaceTestService(t)
	before := workspaceTree(t, root)
	created, err := s.CreateWorkspace("persistent", root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := created.Config
	cfg.RGPath = filepath.Join(t.TempDir(), "custom-rg") // Restoration must not execute this path.
	cfg.RulePackageName = "team.oborules"
	cfg.IncludeGlobs, cfg.ExcludeGlobs = []string{"**/*.go", "**/*.ts"}, []string{"vendor/**", "generated/**"}
	cfg.CheckCommands = []model.Command{{Name: "validation", Executable: "git", Args: []string{"diff", "--check"}}}
	cfg.MaxAttempts, cfg.MaxTurns, cfg.MaxOutputTokens, cfg.MaxFileBytes, cfg.TimeoutSeconds = 2, 8, 4096, 65536, 120
	cfg.MaxCostUSD, cfg.InputPricePerMillion, cfg.CachedInputPricePerMillion, cfg.OutputPricePerMillion = 0.5, 2.5, 0.25, 10
	saved, err := s.SaveConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	connection := saved.Config
	connection.Provider, connection.Endpoint, connection.Deployment, connection.Credential = "openai", "https://personal.example.invalid", "personal-model", "FAKE-local-settings-key"
	selected := workspaceSelectTestConnection(t, s, "private connection", connection)
	if selected.Config.Credential != "" || !selected.Config.CredentialSet {
		t.Fatal("UI state exposed or lost a secret")
	}
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	paths := []string{s.configPath, workspaceSettingPath(t, s, created.ActiveWorkspaceID), selected.Config.QueuePath + ".session.json"}
	for _, path := range paths {
		data := readTest(t, path)
		for _, secret := range []string{connection.Endpoint, connection.Deployment, connection.Credential} {
			if strings.Contains(data, secret) {
				t.Fatalf("personal connection leaked into %s", filepath.Base(path))
			}
		}
	}
	var document struct {
		Config map[string]json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal([]byte(readTest(t, paths[1])), &document); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"provider", "endpoint", "deployment", "authMode", "credential", "credentialSet"} {
		if _, ok := document.Config[field]; ok {
			t.Fatalf("setting.json retained personal field %s", field)
		}
	}
	assertPersonalCiphertexts(t, selected.LLMSettingsPath, connection.Endpoint, connection.Deployment, connection.Credential)
	s.Close()
	reloaded := workspaceNewService(t, s.configPath)
	restored := reloaded.Snapshot()
	if restored.LastError != "" || restored.ReadOnly || restored.ActiveWorkspaceID != created.ActiveWorkspaceID {
		t.Fatalf("restart failed: %s", restored.LastError)
	}
	if !reflect.DeepEqual(storedConfig(restored.Config), storedConfig(saved.Config)) {
		t.Fatal("workspace settings changed across restart")
	}
	if restored.SelectedLLMConnectionID != selected.SelectedLLMConnectionID || !restored.Config.CredentialSet || reloaded.state.Config.Credential != connection.Credential {
		t.Fatal("personal connection selection was not restored independently")
	}
	assertWorkspaceTree(t, root, before)
}

func TestWorkspaceDuplicateSeparatesLocalPackageAssetsAndHistory(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("rg required to validate imported rules")
	}
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, "A.txt"), []byte("before\n"))
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "source fixture")
	before := workspaceTree(t, root)
	st, err := s.CreateWorkspace("original", root)
	if err != nil {
		t.Fatal(err)
	}
	pack := &rulepack.Package{Settings: rulepack.FromConfig(st.Config), Files: map[string][]byte{
		"rules/R001/rule.json":        fixtureRuleJSON(t, "Keep behavior", "", "Preserve behavior.", "", "", "", ""),
		"rules/R001/examples.txt":     []byte("independent auxiliary asset\n"),
		"patterns/legacy-symbols.txt": []byte("Legacy\n"),
	}}
	archive := filepath.Join(t.TempDir(), "base.oborules")
	if err := rulepack.Write(archive, pack); err != nil {
		t.Fatal(err)
	}
	archiveBefore := readTest(t, archive)
	original, err := s.ImportRulePackage(archive, "replace")
	if err != nil {
		t.Fatal(err)
	}
	task := model.Task{File: "A.txt", Status: "done", Attempts: 1, History: []model.Attempt{{ID: uid(), Number: 1, Outcome: "modified", Usage: model.Usage{InputTokens: 17}}}}
	if err := store.SaveQueue(original.Config.QueuePath, []model.Task{task}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.SelectWorkspace(original.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.DuplicateWorkspace("comparison")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ActiveWorkspaceID == original.ActiveWorkspaceID || duplicate.Config.QueuePath == original.Config.QueuePath || len(duplicate.Tasks) != 0 || !sameRoot(duplicate.Config.Root, root) || len(duplicate.Rules) != 1 {
		t.Fatal("duplicate did not isolate identity, history, and rules")
	}
	dir := filepath.Dir(workspaceSettingPath(t, s, duplicate.ActiveWorkspaceID))
	for _, path := range []string{duplicate.Config.RulesPath, duplicate.Config.LegacyPath, duplicate.Config.QueuePath} {
		relative, err := filepath.Rel(dir, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			t.Fatal("duplicate still references another workspace's assets")
		}
	}
	if duplicate.Config.RulesPath == original.Config.RulesPath || duplicate.Config.LegacyPath == original.Config.LegacyPath {
		t.Fatal("rule asset paths were shared")
	}
	copyAsset := filepath.Join(duplicate.Config.RulesPath, "R001", "examples.txt")
	if readTest(t, copyAsset) != "independent auxiliary asset\n" {
		t.Fatal("auxiliary rule asset not copied")
	}
	writeTest(t, copyAsset, []byte("copy-only change\n"))
	if readTest(t, filepath.Join(original.Config.RulesPath, "R001", "examples.txt")) != "independent auxiliary asset\n" {
		t.Fatal("editing duplicate mutated original rules")
	}
	firstAgain, err := s.SelectWorkspace(original.ActiveWorkspaceID)
	if err != nil || len(firstAgain.Tasks) != 1 || firstAgain.Tasks[0].Status != "done" || firstAgain.Usage.InputTokens != 17 {
		t.Fatalf("original history did not survive duplication: %v", err)
	}
	if readTest(t, archive) != archiveBefore {
		t.Fatal("duplicating a workspace changed the source package")
	}
	assertWorkspaceTree(t, root, before)
}

func TestWorkspaceTargetAndQueueRemainIndependent(t *testing.T) {
	s, root := workspaceTestService(t)
	before := workspaceTree(t, root)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWorkspace("second", root)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	for name, path := range map[string]string{
		"other workspace queue": first.Config.QueuePath,
		"source queue":          filepath.Join(root, "queue.jsonl"),
		"cleared queue":         "",
	} {
		cfg := second.Config
		cfg.QueuePath, cfg.MaxTurns = path, 14
		saved, err := s.SaveConfig(cfg)
		if err != nil || saved.Config.QueuePath != second.Config.QueuePath || saved.Config.MaxTurns != 14 {
			t.Fatalf("%s should preserve the managed queue and save unrelated settings: %v", name, err)
		}
	}
	for name, cfg := range map[string]model.Config{
		"changed root": func() model.Config { c := second.Config; c.Root = other; return c }(),
		"cleared root": func() model.Config { c := second.Config; c.Root = ""; return c }(),
	} {
		if _, err := s.SaveConfig(cfg); err == nil {
			t.Errorf("accepted %s", name)
		}
		got := s.Snapshot()
		if got.ActiveWorkspaceID != second.ActiveWorkspaceID || !sameRoot(got.Config.Root, root) || got.Config.QueuePath != second.Config.QueuePath {
			t.Fatal("rejected save changed active settings")
		}
	}
	renamed, err := s.RenameWorkspace("renamed")
	if err != nil || renamed.ActiveWorkspaceID != second.ActiveWorkspaceID || !sameRoot(renamed.Config.Root, root) {
		t.Fatalf("name-only edit failed: %v", err)
	}
	assertWorkspaceTree(t, root, before)
}

func TestWorkspaceKeepsExistingQueueAndHistoryAcrossSettingsSave(t *testing.T) {
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, "example.txt"), []byte("before\n"))
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "source fixture")
	// Standalone initialization can still supply a path. This also represents a
	// workspace saved before the desktop's output-location control was removed.
	cfg := DefaultConfig()
	cfg.Root = root
	cfg.QueuePath = filepath.Join(t.TempDir(), "existing-results", "queue.jsonl")
	created, err := s.SaveConfig(cfg)
	if err != nil || !sameRoot(created.Config.QueuePath, cfg.QueuePath) {
		t.Fatalf("initial standalone queue path was not preserved: %v", err)
	}
	cfg.QueuePath = created.Config.QueuePath
	task := model.Task{File: "example.txt", Status: "done", Attempts: 1, History: []model.Attempt{{ID: uid(), Number: 1, Outcome: "modified", Usage: model.Usage{InputTokens: 17}}}}
	if err := store.SaveQueue(cfg.QueuePath, []model.Task{task}); err != nil {
		t.Fatal(err)
	}
	queueBefore := readTest(t, cfg.QueuePath)
	other, err := s.CreateWorkspace("another workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := s.SelectWorkspace(created.ActiveWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	assertHistory := func(st model.State) {
		t.Helper()
		if st.Config.QueuePath != cfg.QueuePath || len(st.Tasks) != 1 || st.Tasks[0].Status != "done" || len(st.Tasks[0].History) != 1 || st.Usage.InputTokens != 17 {
			t.Fatalf("settings change detached the existing queue or its history: path=%s tasks=%+v usage=%+v error=%s", st.Config.QueuePath, st.Tasks, st.Usage, st.LastError)
		}
		if got := readTest(t, cfg.QueuePath); got != queueBefore {
			t.Fatal("settings change rewrote existing queue contents")
		}
	}
	assertHistory(selected)
	selected.Config.QueuePath = other.Config.QueuePath
	selected.Config.MaxTurns = 18
	saved, err := s.SaveConfig(selected.Config)
	if err != nil || saved.Config.MaxTurns != 18 {
		t.Fatalf("unrelated workspace setting was not saved: %v", err)
	}
	assertHistory(saved)
	_, stored, err := s.readWorkspaceSetting(created.ActiveWorkspaceID)
	if err != nil || stored.QueuePath != cfg.QueuePath || stored.MaxTurns != 18 {
		t.Fatalf("workspace file did not retain queue ownership: %v", err)
	}
	s.Close()
	reloaded := workspaceNewService(t, s.configPath)
	restored := reloaded.Snapshot()
	if restored.LastError != "" || restored.ActiveWorkspaceID != created.ActiveWorkspaceID || restored.Config.MaxTurns != 18 {
		t.Fatalf("workspace restart failed: %s", restored.LastError)
	}
	assertHistory(restored)
}

func TestWorkspaceLocalListAndSelectionNeedNoFolderDiscovery(t *testing.T) {
	s, root := workspaceTestService(t)
	before := workspaceTree(t, root)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWorkspace("second", root)
	if err != nil {
		t.Fatal(err)
	}
	otherRoot := t.TempDir()
	initTargetTestGit(t, otherRoot)
	if _, err = s.CreateWorkspace("different target", otherRoot); err != nil {
		t.Fatal(err)
	}
	active := s.Snapshot().ActiveWorkspaceID
	listed := s.Snapshot().Workspaces
	if len(listed) != 3 {
		t.Fatalf("local list omitted a workspace: got %d", len(listed))
	}
	ids := map[string]bool{}
	for _, w := range listed {
		ids[w.ID] = true
	}
	if !ids[first.ActiveWorkspaceID] || !ids[second.ActiveWorkspaceID] || !ids[active] {
		t.Fatal("local list omitted a workspace from one of the target folders")
	}
	for _, id := range []string{"", "../outside", "not-a-workspace", uid()} {
		if _, err = s.SelectWorkspace(id); err == nil {
			t.Fatalf("accepted invalid or unknown selection %q", id)
		}
		if s.Snapshot().ActiveWorkspaceID != active {
			t.Fatal("failed selection changed active workspace")
		}
	}
	selected, err := s.SelectWorkspace(first.ActiveWorkspaceID)
	if err != nil || selected.ActiveWorkspaceID != first.ActiveWorkspaceID || len(selected.Workspaces) != 3 || !sameRoot(selected.Config.Root, root) {
		t.Fatalf("local selection failed: %v", err)
	}
	assertWorkspaceTree(t, root, before)
}

func TestWorkspaceOtherApplicationDirectoryDoesNotLoadSourceSettings(t *testing.T) {
	s, root := workspaceTestService(t)
	created, err := s.CreateWorkspace("local only", root)
	if err != nil {
		t.Fatal(err)
	}
	// Source data that happens to use old-looking names must remain inert data.
	writeTest(t, filepath.Join(root, "OneByOne", "workspaces", created.ActiveWorkspaceID, "workspace.json"), []byte(`{"version":1,"id":"`+created.ActiveWorkspaceID+`","name":"untrusted source data"}`))
	before := workspaceTree(t, root)
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(t.TempDir(), "private"))
	other := workspaceNewService(t, filepath.Join(t.TempDir(), "app-settings.json"))
	if state := other.Snapshot(); len(state.Workspaces) != 0 || state.ActiveWorkspaceID != "" {
		t.Fatal("another app directory loaded source-stored metadata")
	}
	if _, err = other.SelectWorkspace(created.ActiveWorkspaceID); err == nil {
		t.Fatalf("missing local workspace was not reported: %v", err)
	}
	if len(other.Snapshot().LLMConnections) != 0 {
		t.Fatal("another local application inherited personal settings")
	}
	assertWorkspaceTree(t, root, before)
}

func TestWorkspaceDiskDirectoriesRestoreListWithoutRegistryItems(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWorkspace("second", root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := store.WriteJSON(s.configPath, map[string]any{"version": 1, "activeWorkspaceId": first.ActiveWorkspaceID}); err != nil {
		t.Fatal(err)
	}
	loaded := workspaceNewService(t, s.configPath)
	state := loaded.Snapshot()
	if state.LastError != "" || len(state.Workspaces) != 2 || state.ActiveWorkspaceID != first.ActiveWorkspaceID {
		t.Fatalf("disk workspace list was not restored: %s", state.LastError)
	}
	if _, err := loaded.SelectWorkspace(second.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspaceLocalSettingsRejectSymlinksAndKeepCurrentSelection(t *testing.T) {
	for _, kind := range []string{"file", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s, root := workspaceTestService(t)
			first, err := s.CreateWorkspace("first", root)
			if err != nil {
				t.Fatal(err)
			}
			second, err := s.CreateWorkspace("stay here", root)
			if err != nil {
				t.Fatal(err)
			}
			setting := workspaceSettingPath(t, s, first.ActiveWorkspaceID)
			victim := filepath.Join(t.TempDir(), "victim")
			if kind == "file" {
				writeTest(t, victim, []byte("untouched"))
				if err := os.Remove(setting); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, setting); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			} else {
				if err := os.Mkdir(victim, 0700); err != nil {
					t.Fatal(err)
				}
				writeTest(t, filepath.Join(victim, "sentinel"), []byte("untouched"))
				if err := os.RemoveAll(filepath.Dir(setting)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(victim, filepath.Dir(setting)); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			}
			before := workspaceTree(t, filepath.Dir(victim))
			if _, err := s.SelectWorkspace(first.ActiveWorkspaceID); err == nil {
				t.Fatal("accepted symlinked local settings")
			}
			if state := s.Snapshot(); state.ActiveWorkspaceID != second.ActiveWorkspaceID || s.leaseID != second.ActiveWorkspaceID || s.workspaceLease == nil {
				t.Fatal("failed selection changed active workspace or released its lock")
			}
			assertWorkspaceTree(t, filepath.Dir(victim), before)
		})
	}
}

func TestWorkspaceRejectedLocalMetadataKeepsPriorSelection(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWorkspace("stay here", root)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, workspaceSettingPath(t, s, first.ActiveWorkspaceID), []byte("invalid json"))
	selected, err := s.SelectWorkspace(first.ActiveWorkspaceID)
	if err == nil || selected.ActiveWorkspaceID != second.ActiveWorkspaceID || s.Snapshot().ActiveWorkspaceID != second.ActiveWorkspaceID || s.workspaceLease == nil || s.leaseID != second.ActiveWorkspaceID {
		t.Fatal("invalid local metadata replaced the active workspace")
	}
}

func TestWorkspaceProviderEndpointAndAuthChangesDropPreviousSecret(t *testing.T) {
	for _, change := range []string{"provider", "endpoint", "auth"} {
		t.Run(change, func(t *testing.T) {
			s, root := workspaceTestService(t)
			st, err := s.CreateWorkspace("connection", root)
			if err != nil {
				t.Fatal(err)
			}
			c := st.Config
			c.Endpoint, c.Deployment, c.Credential = "https://same.example.invalid", "model", "FAKE-old-connection-key"
			st = workspaceSelectTestConnection(t, s, "connection", c)
			connection := st.LLMConnections[0]
			switch change {
			case "provider":
				connection.Provider = "openai"
			case "endpoint":
				connection.Endpoint = "https://changed.example.invalid"
			case "auth":
				connection.AuthMode = "bearer"
			}
			st, err = s.SaveLLMConnection(connection)
			if err != nil || st.Config.CredentialSet || s.state.Config.Credential != "" {
				t.Fatalf("connection change retained its previous secret: %v", err)
			}
			s.Close()
			reloaded := workspaceNewService(t, s.configPath)
			if got := reloaded.Snapshot(); got.LastError != "" || got.Config.CredentialSet || got.Config.Endpoint != connection.Endpoint || got.Config.Provider != connection.Provider || got.Config.AuthMode != connection.AuthMode {
				t.Fatalf("cleared connection did not persist: %s", got.LastError)
			}
		})
	}
}

func TestWorkspaceCannotSwitchDuringExecution(t *testing.T) {
	s, root := workspaceTestService(t)
	st, err := s.CreateWorkspace("active", root)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	defer func() { s.mu.Lock(); s.state.Running = false; s.mu.Unlock() }()
	for name, op := range map[string]func() (model.State, error){
		"create":    func() (model.State, error) { return s.CreateWorkspace("x", root) },
		"select":    func() (model.State, error) { return s.SelectWorkspace(st.ActiveWorkspaceID) },
		"rename":    func() (model.State, error) { return s.RenameWorkspace("x") },
		"duplicate": func() (model.State, error) { return s.DuplicateWorkspace("x") },
		"save":      func() (model.State, error) { return s.SaveConfig(st.Config) },
	} {
		if _, err := op(); err == nil {
			t.Errorf("%s allowed during execution", name)
		}
	}
}

func TestWorkspaceCredentialPresenceUsesSelectedProvider(t *testing.T) {
	t.Setenv("AZURE_OPENAI_API_KEY", "FAKE-azure-env")
	t.Setenv("OPENAI_API_KEY", "")
	t.Setenv("ANTHROPIC_API_KEY", "FAKE-claude-env")
	for provider, want := range map[string]bool{"azure": true, "": true, "openai": false, "claude": true} {
		if got := safeConfig(model.Config{Provider: provider}).CredentialSet; got != want {
			t.Errorf("%s credential presence=%v", provider, got)
		}
	}
}

func TestWorkspaceQueueImportCannotChangeTargetRoot(t *testing.T) {
	s, root := workspaceTestService(t)
	st, err := s.CreateWorkspace("fixed", root)
	if err != nil {
		t.Fatal(err)
	}
	other := t.TempDir()
	path := filepath.Join(t.TempDir(), "import.jsonl")
	if err := store.SaveQueue(path, nil); err != nil {
		t.Fatal(err)
	}
	c := DefaultConfig()
	c.Root = other
	if err := store.WriteJSON(path+".session.json", manifest{Version: 1, Root: other, Config: c}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadQueue(path); err == nil || !strings.Contains(err.Error(), "別の対象フォルダ") {
		t.Fatalf("queue import could replace the fixed root: %v", err)
	}
	if after := s.Snapshot(); after.ActiveWorkspaceID != st.ActiveWorkspaceID || after.Config.Root != st.Config.Root || after.Config.QueuePath != st.Config.QueuePath {
		t.Fatal("rejected import changed active config")
	}
}
