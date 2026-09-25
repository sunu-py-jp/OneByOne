package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onebyone/internal/model"
)

func TestPersonalLLMConnectionsExistBeforeWorkspaceAndReuseWithoutSharingSecrets(t *testing.T) {
	s, root := workspaceTestService(t)
	connection := model.LLMConnection{Name: "team model", Provider: "openai", Endpoint: "https://personal-api.example.invalid/v1/", Deployment: "personal-model", AuthMode: "api_key", Credential: "FAKE-reusable-private-key"}
	st, err := s.SaveLLMConnection(connection)
	if err != nil || st.ActiveWorkspaceID != "" || len(st.Workspaces) != 0 || len(st.LLMConnections) != 1 {
		t.Fatalf("standalone connection registration failed: %v", err)
	}
	connection.ID = st.LLMConnections[0].ID
	if st.LLMConnections[0].Credential != "" || !st.LLMConnections[0].CredentialSet {
		t.Fatal("connection list exposed or lost secret presence")
	}
	first, err := s.CreateWorkspace("first", root)
	if err != nil || first.SelectedLLMConnectionID != "" {
		t.Fatalf("new workspace selected an LLM without user choice: %v", err)
	}
	selected, err := s.SelectLLMConnection(connection.ID)
	if err != nil || selected.Config.Endpoint != connection.Endpoint || s.state.Config.Credential != connection.Credential {
		t.Fatalf("selected global connection was not applied: %v", err)
	}
	second, err := s.CreateWorkspace("second", root)
	if err != nil || second.SelectedLLMConnectionID != "" || second.Config.CredentialSet {
		t.Fatalf("new workspace inherited the previous selection: %v", err)
	}
	if _, err = s.SelectLLMConnection(connection.ID); err != nil {
		t.Fatal(err)
	}
	// Workspace settings cannot mutate a reusable
	// connection or replace its endpoint with an imported form's stale value.
	malicious := s.Snapshot().Config
	malicious.Provider, malicious.Endpoint, malicious.Credential = "claude", "https://wrong.example.invalid", "FAKE-wrong-key"
	malicious.LLMConnectionID = "a-different-connection"
	malicious.IncludeGlobs = []string{"*.go"}
	after, err := s.SaveConfig(malicious)
	if err != nil || after.Config.Endpoint != connection.Endpoint || after.Config.Provider != connection.Provider || s.state.Config.Credential != connection.Credential || len(after.Config.IncludeGlobs) != 1 {
		t.Fatalf("workspace save changed its registered connection: %v", err)
	}
	firstAgain, err := s.SelectWorkspace(first.ActiveWorkspaceID)
	if err != nil || firstAgain.SelectedLLMConnectionID != connection.ID || firstAgain.Config.Endpoint != connection.Endpoint || len(firstAgain.Config.IncludeGlobs) != 0 {
		t.Fatalf("workspace selections or settings leaked into each other: %v", err)
	}
	if len(firstAgain.LLMConnections) != 1 {
		t.Fatal("workspace creation or save duplicated the named connection")
	}
	if err = s.persist(); err != nil {
		t.Fatal(err)
	}
	report, err := s.ExportReport("")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{report, firstAgain.Config.QueuePath + ".session.json"} {
		body := readTest(t, path)
		for _, private := range []string{connection.ID, connection.Name, connection.Endpoint, connection.Deployment, connection.Credential} {
			if strings.Contains(body, private) {
				t.Fatal("shared artifact contains a personal connection")
			}
		}
	}
	var saved workspaceSetting
	if err := json.Unmarshal([]byte(readTest(t, settingsFile(filepath.Dir(s.configPath), first.ActiveWorkspaceID, "setting.json"))), &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Config.LLMConnectionID != connection.ID || saved.Config.Credential != "" || saved.Config.Provider != "" || saved.Config.Endpoint != "" || saved.Config.Deployment != "" {
		t.Fatal("setting.json must hold only a nonsecret connection reference")
	}
	if after.Config.LLMConnectionID != connection.ID {
		t.Fatal("workspace save bypassed connection selection")
	}
	encoded, err := json.Marshal(s.Snapshot())
	if err != nil || strings.Contains(string(encoded), connection.Credential) {
		t.Fatal("public state leaks a registered credential")
	}
	s.Close()
	restarted := workspaceNewService(t, s.configPath)
	if got := restarted.Snapshot(); got.LastError != "" || got.SelectedLLMConnectionID != connection.ID || len(got.LLMConnections) != 1 {
		t.Fatalf("global connection and selection did not survive restart: %s", got.LastError)
	}
	if got, err := restarted.SelectWorkspace(second.ActiveWorkspaceID); err != nil || got.SelectedLLMConnectionID != connection.ID || len(got.Config.IncludeGlobs) != 1 {
		t.Fatalf("second workspace did not retain its own settings and shared connection choice: %v", err)
	}
}

func TestPersonalLLMConnectionEditsDoNotDependOnLocalWorkspaceLock(t *testing.T) {
	first, root := workspaceTestService(t)
	created, err := first.CreateWorkspace("shared", root)
	if err != nil {
		t.Fatal(err)
	}
	second := workspaceNewService(t, first.configPath)
	opened := second.Snapshot()
	if opened.ActiveWorkspaceID != created.ActiveWorkspaceID || !opened.ReadOnly {
		t.Fatal("peer is not read-only")
	}
	before := workspaceTree(t, root)
	localBefore := workspaceTree(t, filepath.Dir(first.configPath))
	st, err := second.SaveLLMConnection(model.LLMConnection{Name: "personal in read-only view", Provider: "claude", Endpoint: "https://api.anthropic.com/", Deployment: "configured-model", AuthMode: "api_key", Credential: "FAKE-readonly-local-key"})
	if err != nil || len(st.LLMConnections) != 1 || !st.ReadOnly {
		t.Fatalf("read-only workspace prevented independent connection registration: %v", err)
	}
	id := st.LLMConnections[0].ID
	if _, err = second.SelectLLMConnection(id); err == nil {
		t.Fatal("read-only workspace accepted a connection reference change")
	}
	if _, err = second.ClearLLMConnectionCredential(id); err != nil {
		t.Fatal(err)
	}
	if _, err = second.DeleteLLMConnection(id); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceTree(t, root, before)
	assertWorkspaceTree(t, filepath.Dir(first.configPath), localBefore)
}

func TestPersonalLLMConnectionDeleteClearsAllSelectionsAfterRestart(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := first.Config
	cfg.Provider, cfg.Endpoint, cfg.Deployment, cfg.Credential = "azure", "https://personal.example.invalid", "selected-model", "FAKE-deleted-key"
	selected := workspaceSelectTestConnection(t, s, "delete this connection", cfg)
	id := selected.SelectedLLMConnectionID
	second, err := s.DuplicateWorkspace("reuse")
	if err != nil || second.SelectedLLMConnectionID != id {
		t.Fatalf("duplicate did not reuse connection: %v", err)
	}
	if _, err = s.DeleteLLMConnection(id); err != nil {
		t.Fatal(err)
	}
	for _, workspace := range []string{first.ActiveWorkspaceID, second.ActiveWorkspaceID} {
		state, err := s.SelectWorkspace(workspace)
		if err != nil || state.SelectedLLMConnectionID != "" || state.Config.CredentialSet || state.Config.Endpoint != "" {
			t.Fatalf("deleted connection remains selected: %v", err)
		}
	}
	s.Close()
	reloaded := workspaceNewService(t, s.configPath)
	if st := reloaded.Snapshot(); st.LastError != "" || len(st.LLMConnections) != 0 || st.SelectedLLMConnectionID != "" || st.Config.CredentialSet {
		t.Fatalf("deleted connection returned after restart: %s", st.LastError)
	}
	if _, err = reloaded.SelectLLMConnection(id); err == nil {
		t.Fatal("deleted connection ID was accepted")
	}
}

func TestPersonalLLMConnectionMultipleAppInstancesPreserveEachOthersSavedEntries(t *testing.T) {
	first, _ := workspaceTestService(t)
	second := workspaceNewService(t, filepath.Join(t.TempDir(), "second.json"))
	connection := model.LLMConnection{Name: "first", Provider: "openai", Endpoint: "https://api.openai.com/v1/", Deployment: "configured-model", AuthMode: "api_key", Credential: "FAKE-one"}
	st, err := first.SaveLLMConnection(connection)
	if err != nil {
		t.Fatal(err)
	}
	id := st.LLMConnections[0].ID
	connection.Name, connection.Credential = "second", "FAKE-two"
	if _, err = second.SaveLLMConnection(connection); err != nil {
		t.Fatal(err)
	}
	// Saving through a stale service must transact against current disk state,
	// and deleting the first entry must preserve the second app's new entry.
	if _, err = first.DeleteLLMConnection(id); err != nil {
		t.Fatal(err)
	}
	first.Close()
	second.Close()
	third := workspaceNewService(t, filepath.Join(t.TempDir(), "third.json"))
	st = third.Snapshot()
	if st.LastError != "" || len(st.LLMConnections) != 1 || st.LLMConnections[0].Name != "second" || !st.LLMConnections[0].CredentialSet {
		t.Fatalf("one app overwrote another app's saved connection: %s", st.LastError)
	}
	if _, err = os.Stat(st.LLMSettingsPath); err != nil {
		t.Fatal(err)
	}
}

// A stalled connectivity test must not hold the service operation lock through
// its entire HTTP timeout while the user is trying to close the application.
func TestPersonalLLMConnectionTestCancellationReleasesShutdown(t *testing.T) {
	s, _ := workspaceTestService(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	st, err := s.SaveLLMConnection(model.LLMConnection{Name: "stalled test", Provider: "openai", Endpoint: server.URL, Deployment: "configured-model", AuthMode: "api_key", Credential: "FAKE-local-test-only"})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() { _, err := s.TestLLMConnection(st.LLMConnections[0].ID); finished <- err }()
	select {
	case <-entered:
	case err := <-finished:
		t.Fatalf("connection test never reached the mock server: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("connection test did not start")
	}
	closed := make(chan struct{})
	go func() { s.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("closing the app did not cancel its pending connectivity test")
	}
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("cancelled connectivity test succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown returned before the connectivity call ended")
	}
}

func TestPersonalLLMGetStateRefreshesOtherAppEditsAndDeletionWithoutWorkspaceLease(t *testing.T) {
	second, root := workspaceTestService(t)
	created, err := second.CreateWorkspace("local workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := created.Config
	cfg.Provider, cfg.Endpoint, cfg.Deployment, cfg.Credential = "openai", "https://old.example.invalid/v1/", "old-model", "FAKE-old-poll-key"
	selected := workspaceSelectTestConnection(t, second, "poll this connection", cfg)
	first := workspaceNewService(t, second.configPath)
	opened := first.Snapshot()
	if opened.ActiveWorkspaceID != created.ActiveWorkspaceID || !opened.ReadOnly || first.workspaceLease != nil {
		t.Fatal("viewer unexpectedly acquired the local workspace edit lease")
	}
	before := workspaceTree(t, root)
	connection := selected.LLMConnections[0]
	connection.Name, connection.Endpoint, connection.Deployment, connection.Credential = "edited in another app", "https://new.example.invalid/v1/", "new-model", "FAKE-new-poll-key"
	if _, err = second.SaveLLMConnection(connection); err != nil {
		t.Fatal(err)
	}
	// A snapshot can be stale, but the GUI's GetState poll must load the latest
	// private registry even when the selected local workspace is read-only.
	refreshed, err := first.GetState()
	if err != nil || !refreshed.ReadOnly || first.workspaceLease != nil || refreshed.SelectedLLMConnectionID != connection.ID || len(refreshed.LLMConnections) != 1 {
		t.Fatalf("read-only polling did not refresh the personal selection: %v", err)
	}
	if got := refreshed.LLMConnections[0]; got.Name != connection.Name || got.Endpoint != connection.Endpoint || got.Deployment != connection.Deployment || got.Credential != "" || !got.CredentialSet {
		t.Fatal("poll returned stale connection settings or exposed a credential")
	}
	if refreshed.Config.Endpoint != connection.Endpoint || refreshed.Config.Deployment != connection.Deployment || refreshed.Config.Credential != "" || !refreshed.Config.CredentialSet || first.state.Config.Credential != connection.Credential {
		t.Fatal("selected runtime config was not refreshed safely")
	}
	assertWorkspaceTree(t, root, before)
	other := cfg
	other.Endpoint, other.Deployment, other.Credential = "https://other.example.invalid", "other-model", "FAKE-other-poll-key"
	newSelection := workspaceSelectTestConnection(t, second, "another registered connection", other)
	workspaceFolder := filepath.Dir(settingsFile(filepath.Dir(second.configPath), created.ActiveWorkspaceID, "setting.json"))
	settingsAfterSelection := workspaceTree(t, workspaceFolder)
	refreshed, err = first.GetState()
	if err != nil || refreshed.SelectedLLMConnectionID != newSelection.SelectedLLMConnectionID || refreshed.Config.LLMConnectionID != newSelection.SelectedLLMConnectionID || refreshed.Config.Endpoint != other.Endpoint || first.state.Config.Credential != other.Credential {
		t.Fatalf("poll retained the old workspace connection reference: %v", err)
	}
	assertWorkspaceTree(t, workspaceFolder, settingsAfterSelection)
	if _, err = second.DeleteLLMConnection(newSelection.SelectedLLMConnectionID); err != nil {
		t.Fatal(err)
	}
	cleared, err := first.GetState()
	if err != nil || !cleared.ReadOnly || len(cleared.LLMConnections) != 1 || cleared.SelectedLLMConnectionID != "" || cleared.Config.CredentialSet || first.state.Config.Credential != "" || cleared.Config.Endpoint != "" {
		t.Fatalf("poll retained a deleted selection or stale credential: %v", err)
	}
	assertWorkspaceTree(t, root, before)
}
