package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"onebyone/internal/demopreset"
	"onebyone/internal/engine"
	"onebyone/internal/model"
)

func initCLITestRepository(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is required for workspace fixtures")
	}
	environment := []string{}
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(strings.ToUpper(key), "GIT_") {
			environment = append(environment, entry)
		}
	}
	environment = append(environment, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	for _, args := range [][]string{
		{"init", "--quiet", "--template="},
		{"-c", "user.name=OneByOne Test", "-c", "user.email=test@onebyone.local", "-c", "commit.gpgsign=false", "-c", "core.hooksPath=" + os.DevNull, "commit", "--allow-empty", "--quiet", "-m", "Initial test repository"},
	} {
		command := exec.Command("git", args...)
		command.Dir, command.Env = root, environment
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("initialize workspace Git fixture: %v\n%s", err, output)
		}
	}
}

func TestCLISelectsWorkspaceSplitSettings(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	root := filepath.Join(base, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	initCLITestRepository(t, root)
	config := filepath.Join(base, "config.json")
	s := engine.New(config)
	t.Cleanup(s.Close)
	c := engine.DefaultConfig()
	c.Root, c.Deployment = root, "first-model"
	first, err := s.SaveConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	if err := applyCLIConnection(s, c); err != nil {
		t.Fatal(err)
	}
	second, err := s.DuplicateWorkspace("比較用")
	if err != nil {
		t.Fatal(err)
	}
	c = second.Config
	c.Deployment = "second-model"
	if _, err = s.SaveConfig(c); err != nil {
		t.Fatal(err)
	}
	if err := applyCLIConnection(s, c); err != nil {
		t.Fatal(err)
	}
	var out, logs bytes.Buffer
	s.Close()
	if code := execute([]string{"status", "--config", config, "--workspace", first.ActiveWorkspaceID, "--json"}, &out, &logs); code != 0 {
		t.Fatalf("workspace selection failed: %s", logs.String())
	}
	var state model.State
	if err = json.Unmarshal(out.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.ActiveWorkspaceID != first.ActiveWorkspaceID || state.Config.Deployment != "first-model" || state.Config.QueuePath == second.Config.QueuePath {
		t.Fatalf("selected a different workspace's settings: %+v", state.Workspaces)
	}
}

func TestCLIProviderSwitchDoesNotCarryCredentials(t *testing.T) {
	c := engine.DefaultConfig()
	c.Endpoint, c.Deployment, c.Credential = "https://azure.example", "old-deployment", "old-provider-secret"
	var out, logs bytes.Buffer
	o, _, err := parseOptions([]string{"run", "--provider", "claude", "--deployment", "chosen-model"}, &out, &logs)
	if err != nil {
		t.Fatal(err)
	}
	applyOptions(&c, o)
	if c.Provider != "claude" || c.Endpoint != "https://api.anthropic.com/" || c.Credential != "" || c.Deployment != "chosen-model" || c.AuthMode != "api_key" {
		t.Fatal("provider switch reused another provider's credentials or endpoint")
	}
}

func TestCLIOverridesDoNotModifyReusableConnection(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	root := filepath.Join(base, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	initCLITestRepository(t, root)
	s := engine.New(filepath.Join(base, "config.json"))
	t.Cleanup(s.Close)
	if _, err := s.CreateWorkspace("CLI test", root); err != nil {
		t.Fatal(err)
	}
	saved, err := s.SaveLLMConnection(model.LLMConnection{
		Name: "Reusable", Provider: "azure", Endpoint: "https://original.example.invalid/",
		Deployment: "model", AuthMode: "api_key", Credential: "FAKE-cli-test-secret",
	})
	if err != nil || len(saved.LLMConnections) != 1 {
		t.Fatalf("setup failed: %v", err)
	}
	originalID := saved.LLMConnections[0].ID
	if _, err := s.SelectLLMConnection(originalID); err != nil {
		t.Fatal(err)
	}
	cfg := s.Snapshot().Config
	cfg.Endpoint = "https://override.example.invalid/"
	if err := applyCLIConnection(s, cfg); err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	if st.SelectedLLMConnectionID == originalID || len(st.LLMConnections) != 2 || st.Config.Endpoint != cfg.Endpoint {
		t.Fatal("CLI override was not registered separately")
	}
	for _, connection := range st.LLMConnections {
		if connection.ID == originalID && (connection.Endpoint != "https://original.example.invalid/" || !connection.CredentialSet) {
			t.Fatal("CLI override altered the reusable connection")
		}
		if connection.ID != originalID && connection.CredentialSet {
			t.Fatal("CLI override copied the original connection credential")
		}
	}
	if err := applyCLIConnection(s, st.Config); err != nil || len(s.Snapshot().LLMConnections) != 2 {
		t.Fatalf("identical repeated flags registered a duplicate: %v", err)
	}
}

func TestStatusJSONContainsNoHumanOutputOrSecrets(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "config.json")
	if err := os.WriteFile(config, []byte(`{"version":1,"activeWorkspaceId":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AZURE_OPENAI_API_KEY", "test-secret-never-call-azure")
	var stdout, stderr bytes.Buffer
	if code := execute([]string{"status", "--config", config, "--json"}, &stdout, &stderr); code != 0 {
		t.Fatalf("status failed: code=%d %s", code, stderr.String())
	}
	var state model.State
	if err := json.Unmarshal(stdout.Bytes(), &state); err != nil {
		t.Fatalf("stdout is not one clean JSON value: %v\n%s", err, stdout.String())
	}
	if state.Config.Credential != "" || state.Config.CredentialSet || state.SelectedLLMConnectionID != "" || bytes.Contains(stdout.Bytes(), []byte("test-secret")) || stderr.Len() != 0 {
		t.Fatalf("status output disclosed credentials or included logs: %s %s", stdout.String(), stderr.String())
	}
}

func TestRetryUsesAppliedRulesNotCandidates(t *testing.T) {
	tasks := []model.Task{
		{File: "a.ext", Rules: []string{"R019"}},
		{File: "b.ext", RulesApplied: []string{"R019"}},
		{File: "c.ext", RulesApplied: []string{"R025"}},
	}
	files, err := selectRetryFiles(tasks, []string{"c.ext", "b.ext"}, "R019")
	if err != nil || !reflect.DeepEqual(files, []string{"b.ext", "c.ext"}) {
		t.Fatalf("wrong retry selection: %v %v", files, err)
	}
	if _, err := selectRetryFiles(tasks, []string{"missing.ext"}, ""); err == nil {
		t.Fatal("unknown file must be an error")
	}
	if _, err := selectRetryFiles(tasks, nil, "R999"); err == nil {
		t.Fatal("unapplied rule must not reset arbitrary candidates")
	}
}

func TestCLIConfigAndValidation(t *testing.T) {
	t.Setenv("ONEBYONE_CONFIG", filepath.Join(t.TempDir(), "env.json"))
	var stdout, stderr bytes.Buffer
	o, help, err := parseOptions([]string{"--config", "chosen.json", "run", "--limit", "20", "--json"}, &stdout, &stderr)
	if err != nil || help || o.config != "chosen.json" || o.limit != 20 || !o.json {
		t.Fatalf("incorrect option parsing: %+v %v %v", o, help, err)
	}
	for _, args := range [][]string{{"run", "--limit", "-1"}, {"retry"}, {"status", "--file", "a.ext"}, {"demo", "--root", "somewhere"}, {"status", "--endpoint", "https://example.com"}} {
		if _, _, err := parseOptions(args, &stdout, &stderr); err == nil {
			t.Errorf("invalid flags were accepted: %v", args)
		}
	}
}

func TestAppPreferencesRejectUnknownFieldsAndCredentials(t *testing.T) {
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	path := filepath.Join(base, "app-settings.json")
	for _, body := range []string{`{"credential":"never-print-me"}`, `{"version":999}`, `{} {}`, `null`} {
		if err := os.WriteFile(path, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		service := engine.New(path)
		st := service.Snapshot()
		service.Close()
		if st.LastError == "" || strings.Contains(st.LastError, "never-print-me") {
			t.Fatal("invalid app preferences accepted or secret disclosed")
		}
	}
}

func TestLocalCLIWorkflowDoesNotNeedAzure(t *testing.T) {
	for _, executable := range []string{"git", "rg"} {
		if _, err := exec.LookPath(executable); err != nil {
			t.Skip(executable + " is needed for the CLI integration test")
		}
	}
	t.Setenv("AZURE_OPENAI_API_KEY", "")
	t.Setenv("AZURE_OPENAI_AUTH_TOKEN", "")
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(t.TempDir(), "private"))
	config := filepath.Join(t.TempDir(), "config.json")
	invoke := func(args ...string) []byte {
		t.Helper()
		var out, log bytes.Buffer
		full := append([]string{"--config", config}, args...)
		if code := execute(full, &out, &log); code != 0 {
			t.Fatalf("%v failed (%d): %s\n%s", args, code, log.String(), out.String())
		}
		return out.Bytes()
	}
	var state model.State
	if err := json.Unmarshal(invoke("demo", "--json"), &state); err != nil {
		t.Fatal(err)
	}
	demoDir := state.Config.Root
	if strings.HasPrefix(filepath.Base(demoDir), demopreset.ProjectName+"-") {
		t.Cleanup(func() { _ = os.RemoveAll(demoDir) })
	}
	if len(state.Tasks) != demopreset.TargetFileCount || len(state.Rules) != demopreset.CommonRuleCount+demopreset.IndividualRuleCount {
		t.Fatalf("demo did not load the bundled source and rules: %d tasks, %d rules", len(state.Tasks), len(state.Rules))
	}
	combinedFound := false
	for _, task := range state.Tasks {
		if task.Status != "pending" || task.Attempts != 0 || len(task.History) != 0 {
			t.Fatalf("demo must create real pending work, not fabricated outcomes: %+v", task)
		}
		if task.File == "src/workflows/dispatch-cycle.js" {
			combinedFound = true
			for _, rule := range state.Rules {
				// Common rules are shared across all targets; JSONL stores only
				// the individual rules matched for each file.
				if rule.Always {
					if rule.CandidateCount != len(state.Tasks) {
						t.Fatalf("common demo rule %s does not cover every target", rule.ID)
					}
					continue
				}
				if !slices.Contains(task.Rules, rule.ID) {
					t.Fatalf("combined demo workflow was not mapped to rule %s", rule.ID)
				}
			}
		}
	}
	if !combinedFound {
		t.Fatal("the combined demo workflow was not extracted")
	}
	firstFile := state.Tasks[0].File
	if err := json.Unmarshal(invoke("scan", "--json"), &state); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(invoke("retry", "--file", firstFile, "--json"), &state); err != nil {
		t.Fatal(err)
	}
	if state.Tasks[0].Status != "pending" || state.Tasks[0].Note == "" {
		t.Fatal("retry did not persist its selection")
	}
	if err := json.Unmarshal(invoke("status", "--json"), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != demopreset.TargetFileCount || state.Usage != (model.Usage{}) {
		t.Fatal("persisted status is incorrect or unexpectedly used Azure")
	}
	var result struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(invoke("report", "--json"), &result); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(result.Path); err != nil {
		t.Fatal("report was not saved:", err)
	}
}
