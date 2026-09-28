package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/engine"
	"onebyone/internal/model"
)

func TestCLIConcurrencyFlagValidation(t *testing.T) {
	for _, value := range []string{"1", "2", "10"} {
		var out, logs bytes.Buffer
		o, help, err := parseOptions([]string{"run", "--concurrency", value}, &out, &logs)
		if err != nil || help || !o.set["concurrency"] || o.concurrency < 1 || o.concurrency > 10 {
			t.Fatalf("valid concurrency %s rejected: %v", value, err)
		}
	}
	for _, args := range [][]string{
		{"run", "--concurrency", "0"}, {"run", "--concurrency", "-1"}, {"run", "--concurrency", "11"},
		{"run", "--concurrency", "1.5"}, {"scan", "--concurrency", "2"}, {"status", "--concurrency", "2"},
	} {
		var out, logs bytes.Buffer
		if _, _, err := parseOptions(args, &out, &logs); err == nil {
			t.Fatalf("invalid arguments accepted: %v", args)
		}
	}
	var out, logs bytes.Buffer
	o, _, err := parseOptions([]string{"run"}, &out, &logs)
	if err != nil || o.set["concurrency"] {
		t.Fatal("omitting concurrency must preserve saved settings")
	}
}

func TestCLIConcurrencyPersistsWithConnectionSelectionAndAllowsDefaultReset(t *testing.T) {
	s, root, base := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	saved, err := s.SaveLLMConnection(model.LLMConnection{
		Name: "Concurrency fixture", Provider: "openai", Endpoint: "https://api.openai.com/v1/",
		Deployment: "fixture-model", AuthMode: "api_key",
	})
	if err != nil {
		t.Fatal(err)
	}
	connectionID := saved.LLMConnections[0].ID
	var out, logs bytes.Buffer
	o, _, err := parseOptions([]string{"run", "--connection", connectionID, "--concurrency", "4"}, &out, &logs)
	if err != nil {
		t.Fatal(err)
	}
	if err := prepareCLIWorkspace(s, o); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Config.Concurrency != 4 || s.Snapshot().SelectedLLMConnectionID != connectionID {
		t.Fatal("run failed to save both its connection selection and concurrency")
	}
	s.Close()
	reopened := engine.New(filepath.Join(base, "app", "settings.json"))
	defer reopened.Close()
	if reopened.Snapshot().Config.Concurrency != 4 {
		t.Fatal("run concurrency was not persisted to workspace settings")
	}
	if err := prepareCLIWorkspace(reopened, options{command: "run", set: map[string]bool{}}); err != nil || reopened.Snapshot().Config.Concurrency != 4 {
		t.Fatal("omitted run flag replaced the saved concurrency")
	}
	ctx := context.Background()
	if _, err := executeSettingsCommand(ctx, reopened, []string{"update", "--input", "-"}, strings.NewReader(`{"inputPricePerMillion":8}`)); err != nil {
		t.Fatal(err)
	}
	if reopened.Snapshot().Config.Concurrency != 4 {
		t.Fatal("unrelated partial settings update replaced concurrency")
	}
	shown, err := executeSettingsCommand(ctx, reopened, []string{"show"}, nil)
	if err != nil || string(shown.(map[string]json.RawMessage)["concurrency"]) != "4" {
		t.Fatalf("settings show omitted concurrency: %v", err)
	}
	if _, err := executeSettingsCommand(ctx, reopened, []string{"update", "--input", "-"}, strings.NewReader(`{"concurrency":0}`)); err != nil {
		t.Fatal(err)
	}
	cfg := reopened.Snapshot().Config
	if cfg.Concurrency != 0 || cfg.EffectiveConcurrency() != 2 || cfg.InputPricePerMillion != 8 {
		t.Fatal("default reset did not preserve the unset value or unrelated settings")
	}
	shown, err = executeSettingsCommand(ctx, reopened, []string{"show"}, nil)
	if err != nil || string(shown.(map[string]json.RawMessage)["concurrency"]) != "0" {
		t.Fatalf("default concurrency must be shown as reusable 0, not null: %v", err)
	}
	for _, body := range []string{`{"concurrency":-1}`, `{"concurrency":11}`, `{"concurrency":1.5}`, `{"concurrency":null}`} {
		if _, err := executeSettingsCommand(ctx, reopened, []string{"update", "--input", "-"}, strings.NewReader(body)); err == nil {
			t.Fatalf("invalid concurrency settings accepted: %s", body)
		}
		if reopened.Snapshot().Config.Concurrency != 0 {
			t.Fatal("invalid settings changed saved concurrency")
		}
	}
}
