package engine

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func TestUnspecifiedExecutionSettingsSurviveSaveRunAndRestart(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxAttempts, cfg.MaxTurns, cfg.TimeoutSeconds = 0, 0, 0
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceExecutionSettings(t, cfg, model.Config{})
	if detail, err := s.GetFileDetail("A.txt", -1); err != nil || detail.Before != "Legacy.Save()\n" {
		t.Fatalf("unset byte limit prevented preview: %+v, %v", detail, err)
	}
	called := false
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		called = true
		assertWorkspaceExecutionSettings(t, in.Config, model.Config{})
		if _, ok := ctx.Deadline(); ok {
			t.Error("unset timeout imposed a file deadline")
		}
		if text, err := in.ReadContext("A.txt", 1, 10); err != nil || !strings.Contains(text, "Legacy.Save()") {
			t.Errorf("unset byte limit prevented source context reads: %q, %v", text, err)
		}
		return successfulProposal(ctx, in)
	}
	state := runTest(t, s, 1)
	if !called || state.LastError != "" || len(state.Tasks) != 1 || state.Tasks[0].Status != "done" {
		t.Fatalf("unset execution settings blocked a normal run: called=%t, error=%s, tasks=%+v", called, state.LastError, state.Tasks)
	}
	assertWorkspaceExecutionSettings(t, state.Config, model.Config{})
	data, err := os.ReadFile(workspaceSettingPath(t, s, state.ActiveWorkspaceID))
	if err != nil {
		t.Fatal(err)
	}
	var stored struct {
		Config model.Config `json:"config"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceExecutionSettings(t, stored.Config, model.Config{})
	checkpoint, err := loadLatestRepairCheckpoint(cfg, state.Tasks[0].History)
	if err != nil || checkpoint == nil || checkpoint.SettingsHash != repairSettingsHash(state.Config) {
		t.Fatalf("execution replaced the raw settings fingerprint with resolved defaults: %v", err)
	}
	configPath := s.configPath
	s.Close()
	reloaded := New(configPath)
	t.Cleanup(reloaded.Close)
	if reloaded.Snapshot().LastError != "" {
		t.Fatal(reloaded.Snapshot().LastError)
	}
	assertWorkspaceExecutionSettings(t, reloaded.Snapshot().Config, model.Config{})
}

func TestUnspecifiedExecutionAttemptsContinuePastThreeUntilSuccess(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxAttempts, cfg.MaxTurns, cfg.TimeoutSeconds = 0, 0, 0
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		calls++
		if calls <= 4 {
			return model.Proposal{Outcome: "modified", Edits: []model.Edit{{OldText: "not present", NewText: "never applied"}}}, nil
		}
		return successfulProposal(ctx, in)
	}
	state := runTest(t, s, 1)
	if calls != 5 || state.Tasks[0].Status != "done" {
		t.Fatalf("unset attempts stopped before completion: calls=%d, task=%+v", calls, state.Tasks[0])
	}
	assertWorkspaceExecutionSettings(t, state.Config, model.Config{})
}

func TestUnspecifiedExecutionTimeCanStillBeStopped(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxAttempts, cfg.MaxTurns, cfg.TimeoutSeconds = 0, 0, 0
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	s.propose = func(ctx context.Context, _ agent.Input) (model.Proposal, error) {
		if _, bounded := ctx.Deadline(); bounded {
			t.Error("unlimited run imposed a deadline")
		}
		close(started)
		<-ctx.Done()
		return model.Proposal{}, ctx.Err()
	}
	if err := s.Start(1); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("unlimited run did not reach the proposer")
	}
	s.Stop()
	done := make(chan struct{})
	go func() { s.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("unlimited execution ignored manual stop")
	}
	if state := s.Snapshot(); state.Running || state.Tasks[0].Status != "needs_human" || state.Tasks[0].Attempts != 1 {
		t.Fatalf("cancelled unlimited execution retried or lost its hold result: %+v", state.Tasks)
	}
}

func TestNormalizeExecutionSettingsAllowsUnsetWithoutAcceptingInvalidValues(t *testing.T) {
	unset, err := normalizeConfig(DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	assertWorkspaceExecutionSettings(t, unset, model.Config{})
	for _, test := range []struct {
		name string
		set  func(*model.Config)
	}{
		{"attempts negative", func(c *model.Config) { c.MaxAttempts = -1 }},
		{"attempts too large", func(c *model.Config) { c.MaxAttempts = 4 }},
		{"turns negative", func(c *model.Config) { c.MaxTurns = -1 }},
		{"turns too large", func(c *model.Config) { c.MaxTurns = 33 }},
		{"output too small", func(c *model.Config) { c.MaxOutputTokens = 255 }},
		{"file too small", func(c *model.Config) { c.MaxFileBytes = 1023 }},
		{"timeout too short", func(c *model.Config) { c.TimeoutSeconds = 9 }},
		{"cost without rates", func(c *model.Config) { c.MaxCostUSD = 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := DefaultConfig()
			test.set(&cfg)
			if _, err := normalizeConfig(cfg); err == nil {
				t.Fatal("invalid explicitly specified value was accepted")
			}
		})
	}
}
