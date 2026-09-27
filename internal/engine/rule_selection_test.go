package engine

import (
	"context"
	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRuleSelectionSaveRestartAndStaleQueueGuard(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.ExcludedRuleIDs = []string{"R001", " R001 ", "unknown"}
	state, err := s.SaveConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(state.Config.ExcludedRuleIDs, []string{"R001"}) {
		t.Fatal("selection was not normalized", state.Config.ExcludedRuleIDs)
	}
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		t.Error("stale queue reached LLM")
		return model.Proposal{}, nil
	}
	if err = s.Start(0); err == nil || !strings.Contains(err.Error(), "対象抽出") {
		t.Fatal("selection change did not block old queue", err)
	}
	settings := s.configPath
	s.Close()
	s = New(settings)
	t.Cleanup(s.Close)
	if !reflect.DeepEqual(s.Snapshot().Config.ExcludedRuleIDs, []string{"R001"}) {
		t.Fatal("old session overwrote current selection")
	}
	state, err = s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Rules) != 2 || !reflect.DeepEqual(state.Tasks[0].Rules, []string{"R019"}) {
		t.Fatal("wrong selected extraction", state)
	}
	cfg = state.Config
	cfg.ExcludedRuleIDs = []string{"R001", "R019"}
	if _, err = s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	state, err = s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 0 || len(state.Rules) != 2 {
		t.Fatal("zero selection should clear unused queue while retaining rules")
	}
	if err = s.Start(0); err == nil {
		t.Fatal("zero rule selection started")
	}
	cfg.ExcludedRuleIDs = nil
	if _, err = s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	state, err = s.Scan()
	if err != nil || len(state.Tasks) != 1 {
		t.Fatal("reselection did not restore target", err)
	}
}

func TestRuleDeselectionKeepsHistoryAndRestoresCheckboxes(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	state := runTest(t, s, 1)
	if state.Tasks[0].Status != "done" {
		t.Fatal("fixture not completed", state.Tasks)
	}
	if _, err := s.SetTaskSelection([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	cfg = state.Config
	cfg.ExcludedRuleIDs = []string{"R001", "R019"}
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	state, err := s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Tasks) != 2 || state.Tasks[0].Status != "done" || len(state.Tasks[0].History) != 1 {
		t.Fatal("deselection lost history", state.Tasks)
	}
	for _, task := range state.Tasks {
		if !task.Excluded || len(task.Rules) != 0 || task.ExcludedBeforeScope == nil {
			t.Fatal("deselected task remains runnable", task)
		}
	}
	cfg.ExcludedRuleIDs = nil
	if _, err = s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	state, err = s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	if state.Tasks[0].Excluded || !state.Tasks[1].Excluded || state.Tasks[0].ExcludedBeforeScope != nil || state.Tasks[0].Status != "done" {
		t.Fatal("restored scope lost checkbox choices/history", state.Tasks)
	}
}

func TestRuleReplacementResetsSelectionAndMergeSelectsNewRules(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.ExcludedRuleIDs = []string{"R001", "R019"}
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	definition, err := ruleformat.Encode(model.RuleDefinition{ID: "R101", Name: "New rule", Description: "Another rule", Body: "Preserve behavior"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "rules.json")
	if err = rulepack.Write(path, &rulepack.Package{Rules: []rulepack.Entry{{ID: "R101", Markdown: string(definition)}}}); err != nil {
		t.Fatal(err)
	}
	state, err := s.ImportRulePackage(path, "merge")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Rules) != 3 || !reflect.DeepEqual(state.Config.ExcludedRuleIDs, []string{"R001", "R019"}) {
		t.Fatal("merge changed existing selection", state.Config.ExcludedRuleIDs)
	}
	state, err = s.ImportRulePackage(path, "replace")
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Rules) != 1 || len(state.Config.ExcludedRuleIDs) != 0 {
		t.Fatal("replacement retained stale selection", state.Config.ExcludedRuleIDs)
	}
}
