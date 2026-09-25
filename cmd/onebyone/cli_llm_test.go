package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/engine"
	"onebyone/internal/model"
)

func newCLILLMTestService(t *testing.T) (*engine.Service, string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	for _, name := range []string{"AZURE_OPENAI_API_KEY", "AZURE_OPENAI_AUTH_TOKEN", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(name, "")
	}
	path := filepath.Join(base, "app-settings.json")
	s := engine.New(path)
	t.Cleanup(s.Close)
	return s, path
}

func saveCLILLMTestConnection(t *testing.T, s *engine.Service) model.LLMConnection {
	t.Helper()
	input := `{"name":"CLI personal connection","provider":"openai","endpoint":"https://api.openai.com/v1/","deployment":"test-model","authMode":"api_key","credential":"PRIVATE-CLI-API-KEY"}`
	value, err := executeLLMCommand(context.Background(), s, []string{"save", "--input", "-"}, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	state, ok := value.(model.State)
	if !ok || len(state.LLMConnections) != 1 {
		t.Fatal("save did not return the registered connection")
	}
	return state.LLMConnections[0]
}

func TestCLILLMJSONRegistrationPersistsWithoutExposingAPIKey(t *testing.T) {
	s, config := newCLILLMTestService(t)
	connection := saveCLILLMTestConnection(t, s)
	if connection.ID == "" || connection.Credential != "" || !connection.CredentialSet {
		t.Fatal("save response exposed the API key or lost its presence")
	}
	connection.Name = "Updated CLI connection"
	encoded, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	inputFile := filepath.Join(t.TempDir(), "connection.json")
	if err := os.WriteFile(inputFile, encoded, 0600); err != nil {
		t.Fatal(err)
	}
	value, err := executeLLMCommand(context.Background(), s, []string{"save", "--input", inputFile}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := value.(model.State)
	if len(state.LLMConnections) != 1 || !state.LLMConnections[0].CredentialSet || state.LLMConnections[0].Name != connection.Name {
		t.Fatal("updating by ID duplicated the connection or cleared its credential")
	}
	body, err := json.Marshal(state)
	if err != nil || bytes.Contains(body, []byte("PRIVATE-CLI-API-KEY")) {
		t.Fatal("save returned a plaintext credential")
	}
	found := false
	if err := filepath.WalkDir(state.LLMSettingsPath, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".json") {
			return err
		}
		found = true
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if bytes.Contains(data, []byte("PRIVATE-CLI-API-KEY")) {
			t.Fatal("CLI registration wrote a plaintext API key")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("CLI registration did not persist private settings")
	}
	s.Close()
	restarted := engine.New(config)
	t.Cleanup(restarted.Close)
	value, err = executeLLMCommand(context.Background(), restarted, []string{"list"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	connections := value.([]model.LLMConnection)
	if len(connections) != 1 || connections[0].ID != connection.ID || !connections[0].CredentialSet || connections[0].Credential != "" {
		t.Fatal("list did not safely restore the connection")
	}
}

func TestCLILLMDestructiveCommandsRequireConfirmation(t *testing.T) {
	s, _ := newCLILLMTestService(t)
	connection := saveCLILLMTestConnection(t, s)
	for _, command := range []string{"delete", "clear"} {
		if _, err := executeLLMCommand(context.Background(), s, []string{command, "--id", connection.ID}, nil); err == nil {
			t.Fatalf("%s accepted missing --yes", command)
		}
	}
	if _, err := executeLLMCommand(context.Background(), s, []string{"logout", "--id", connection.ID}, nil); err == nil || !strings.Contains(err.Error(), "clear") {
		t.Fatal("logout must direct API-key connections to confirmed clear")
	}
	if connections := s.Snapshot().LLMConnections; len(connections) != 1 || !connections[0].CredentialSet {
		t.Fatal("unconfirmed mutation changed the connection")
	}
	if _, err := executeLLMCommand(context.Background(), s, []string{"clear", "--id", connection.ID, "--yes"}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().LLMConnections[0].CredentialSet {
		t.Fatal("confirmed clear retained the credential")
	}
	if _, err := executeLLMCommand(context.Background(), s, []string{"delete", "--id", connection.ID, "--yes"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().LLMConnections) != 0 {
		t.Fatal("confirmed delete retained the connection")
	}
}

func TestCLILLMSelectsRegisteredConnectionForWorkspace(t *testing.T) {
	s, config := newCLILLMTestService(t)
	connection := saveCLILLMTestConnection(t, s)
	root := filepath.Join(filepath.Dir(config), "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	initCLITestRepository(t, root)
	if _, err := s.CreateWorkspace("CLI workspace", root); err != nil {
		t.Fatal(err)
	}
	value, err := executeLLMCommand(context.Background(), s, []string{"select", "--id", connection.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state := value.(model.State)
	if state.SelectedLLMConnectionID != connection.ID || !state.Config.CredentialSet || state.Config.Credential != "" {
		t.Fatal("selection did not safely apply the chosen connection")
	}
}

func TestCLILLMOAuthValidationNeverStartsUnsupportedLogin(t *testing.T) {
	s, _ := newCLILLMTestService(t)
	connection := saveCLILLMTestConnection(t, s)
	for _, args := range [][]string{
		{"login"}, {"login", "--id", "missing"}, {"login", "--id", connection.ID},
		{"test"}, {"test", "--id", "missing"},
	} {
		if _, err := executeLLMCommand(context.Background(), s, args, nil); err == nil {
			t.Fatalf("invalid authentication operation succeeded: %v", args)
		}
	}
	for _, input := range []string{
		`{"name":"Unsupported OAuth","provider":"openai","authMode":"oauth"}`,
		`{"name":"Incomplete OAuth","provider":"azure","authMode":"oauth","endpoint":"https://sample.openai.azure.com/openai/v1/"}`,
	} {
		if _, err := executeLLMCommand(context.Background(), s, []string{"save", "--input", "-"}, strings.NewReader(input)); err == nil {
			t.Fatal("invalid OAuth configuration was registered")
		}
	}
	if len(s.Snapshot().LLMConnections) != 1 {
		t.Fatal("invalid OAuth setup changed saved connections")
	}
}

func TestCLILLMOAuthRegistersWithoutAuthenticationAndAllowsLogout(t *testing.T) {
	s, _ := newCLILLMTestService(t)
	input := `{"name":"Azure OAuth","provider":"azure","authMode":"oauth","endpoint":"https://sample.openai.azure.com/openai/v1/","deployment":"model","oauthTenantId":"12345678-1234-1234-1234-123456789abc","oauthClientId":"98765432-4321-4321-4321-abcdef123456"}`
	value, err := executeLLMCommand(context.Background(), s, []string{"save", "--input", "-"}, strings.NewReader(input))
	if err != nil {
		t.Fatal(err)
	}
	connection := value.(model.State).LLMConnections[0]
	if connection.OAuthSignedIn || connection.CredentialSet {
		t.Fatal("registration must not open a browser or imply authentication")
	}
	if _, err := executeLLMCommand(context.Background(), s, []string{"test", "--id", connection.ID}, nil); err == nil {
		t.Fatal("unsigned OAuth connection must fail before network access")
	}
	value, err = executeLLMCommand(context.Background(), s, []string{"logout", "--id", connection.ID}, nil)
	if err != nil || value.(model.State).LLMConnections[0].OAuthSignedIn {
		t.Fatalf("OAuth logout failed: %v", err)
	}
}

func TestCLILLMRejectsUnexpectedArgumentsAndCanceledMutation(t *testing.T) {
	s, _ := newCLILLMTestService(t)
	for _, args := range [][]string{
		nil, {"unknown"}, {"save"}, {"save", "--input", " "},
		{"list", "--id", "unexpected"}, {"list", "extra"},
		{"save", "--api-key", "SECRET-IN-FLAG"},
		{"select"}, {"delete", "--yes"}, {"clear", "--yes"}, {"logout"},
	} {
		if _, err := executeLLMCommand(context.Background(), s, args, nil); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		} else if strings.Contains(err.Error(), "SECRET-IN-FLAG") {
			t.Fatal("argument error repeated a credential")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executeLLMCommand(ctx, s, []string{"save", "--input", "-"}, strings.NewReader(`{"name":"Canceled"}`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled mutation proceeded: %v", err)
	}
	if len(s.Snapshot().LLMConnections) != 0 {
		t.Fatal("invalid CLI operations changed stored connections")
	}
}

func TestCLILLMRejectsMalformedOrUnknownJSONWithoutSavingSecrets(t *testing.T) {
	s, _ := newCLILLMTestService(t)
	for _, input := range []string{
		`{"name":"broken","credential":"PRIVATE-INVALID-INPUT"`,
		`{"name":"unknown field","credential":"PRIVATE-INVALID-INPUT","unexpected":true}`,
		`{"name":"trailing","credential":"PRIVATE-INVALID-INPUT"}{"name":"extra"}`,
		`null`,
	} {
		if _, err := executeLLMCommand(context.Background(), s, []string{"save", "--input", "-"}, strings.NewReader(input)); err == nil {
			t.Fatal("invalid connection JSON was accepted")
		} else if strings.Contains(err.Error(), "PRIVATE-INVALID-INPUT") {
			t.Fatal("JSON error exposed a credential")
		}
	}
	if len(s.Snapshot().LLMConnections) != 0 {
		t.Fatal("invalid JSON mutated stored connections")
	}
}

func TestCLILLMInvalidArgumentsCannotSwitchGlobalWorkspace(t *testing.T) {
	s, config := newCLILLMTestService(t)
	root := filepath.Join(filepath.Dir(config), "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	initCLITestRepository(t, root)
	first, err := s.CreateWorkspace("Original workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWorkspace("Other workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SelectWorkspace(first.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	s.Close()
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{
		{"llm", "unknown"},
		{"llm", "list", "--unexpected"},
		{"llm", "save"},
		{"llm", "login"},
		{"llm", "delete", "--id", "connection-id"},
	} {
		args := append(append([]string(nil), command...), "--config", config, "--workspace", second.ActiveWorkspaceID, "--json")
		var stdout, stderr bytes.Buffer
		if code := executeContext(context.Background(), args, nil, &stdout, &stderr); code != 1 {
			t.Fatalf("invalid arguments returned %d: %v", code, command)
		}
		after, err := os.ReadFile(config)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("invalid LLM arguments changed active workspace: %v", command)
		}
	}
}
