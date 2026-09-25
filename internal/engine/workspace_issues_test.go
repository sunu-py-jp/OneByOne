package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func workspaceIssues(t *testing.T, state model.State, id string) []model.WorkspaceIssue {
	t.Helper()
	for _, workspace := range state.Workspaces {
		if workspace.ID == id {
			return workspace.Issues
		}
	}
	t.Fatalf("workspace %s missing from state", id)
	return nil
}

func TestWorkspaceIssuesIdentifyInactiveInvalidRulesAndSurviveSelection(t *testing.T) {
	s, root := workspaceTestService(t)
	if _, err := s.CreateWorkspace("legacy definition", root); err != nil {
		t.Fatal(err)
	}
	first, err := s.CreateRule(ruleEdit("R001", "first", ""))
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.DuplicateWorkspace("broken JSON")
	if err != nil {
		t.Fatal(err)
	}
	clean, err := s.CreateWorkspace("healthy", root)
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(first.Config.RulesPath, "R001", "name.txt")
	writeTest(t, legacyPath, []byte("old title remains untouched"))
	writeTest(t, filepath.Join(second.Config.RulesPath, "R001", "rule.json"), []byte("invalid JSON"))
	s.Close()
	restored := workspaceNewService(t, s.configPath)
	assertIssues := func(st model.State) {
		t.Helper()
		for _, id := range []string{first.ActiveWorkspaceID, second.ActiveWorkspaceID} {
			issues := workspaceIssues(t, st, id)
			if len(issues) != 1 || issues[0].ID != "catalog" || issues[0].Page != "rules" || issues[0].RuleID != "R001" {
				t.Fatalf("invalid rule attributed incorrectly: %#v", issues)
			}
		}
		if len(workspaceIssues(t, st, clean.ActiveWorkspaceID)) != 0 {
			t.Fatal("healthy workspace inherited another workspace's issue")
		}
		if !strings.Contains(workspaceIssues(t, st, first.ActiveWorkspaceID)[0].Message, "旧形式のname.txt") {
			t.Fatal("legacy rule diagnostic message was lost")
		}
	}
	assertIssues(restored.Snapshot())
	for _, id := range []string{first.ActiveWorkspaceID, second.ActiveWorkspaceID, clean.ActiveWorkspaceID} {
		selected, err := restored.SelectWorkspace(id)
		if err != nil {
			t.Fatal(err)
		}
		assertIssues(selected)
		if id != clean.ActiveWorkspaceID && len(selected.Rules) != 0 {
			t.Fatal("invalid catalog was published as executable rules")
		}
	}
	if readTest(t, legacyPath) != "old title remains untouched" {
		t.Fatal("diagnosing an old rule modified its assets")
	}
}

func TestWorkspaceIssuesRefreshAfterFixAndNeverValidateRunningPolls(t *testing.T) {
	s, root := workspaceTestService(t)
	if _, err := s.CreateWorkspace("repair", root); err != nil {
		t.Fatal(err)
	}
	initial, err := s.CreateRule(ruleEdit("R001", "repairable", ""))
	if err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(initial.Config.RulesPath, "R001", "name.txt")
	writeTest(t, legacyPath, []byte("old"))
	// Snapshot and polling while running must serve their memory-only view.
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	st, err := s.GetState()
	if err != nil || len(workspaceIssues(t, st, initial.ActiveWorkspaceID)) != 0 || len(st.Rules) != 1 {
		t.Fatalf("running GetState inspected edited catalog: %v", err)
	}
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	st, err = s.GetState()
	if err != nil || len(workspaceIssues(t, st, initial.ActiveWorkspaceID)) != 1 || len(st.Rules) != 0 {
		t.Fatalf("idle refresh did not diagnose invalid catalog: %v", err)
	}
	if err := os.Remove(legacyPath); err != nil {
		t.Fatal(err)
	}
	if len(workspaceIssues(t, s.Snapshot(), initial.ActiveWorkspaceID)) != 1 {
		t.Fatal("Snapshot re-read the repaired catalog")
	}
	st, err = s.GetState()
	if err != nil || len(workspaceIssues(t, st, initial.ActiveWorkspaceID)) != 0 || len(st.Rules) != 1 || st.LastError != "" {
		t.Fatalf("repair did not clear the diagnostic and restore the rule: %v", err)
	}
}

func TestWorkspaceRulePackageImportClearsIssueWithoutEditingOldAssets(t *testing.T) {
	s, _, base := rulePackageService(t)
	initial, err := s.CreateRule(ruleEdit("R001", "replacement", ""))
	if err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "replacement.oborules")
	if _, err = s.ExportRulePackage(archive); err != nil {
		t.Fatal(err)
	}
	legacyPath := filepath.Join(initial.Config.RulesPath, "R001", "name.txt")
	writeTest(t, legacyPath, []byte("old"))
	broken, err := s.SelectWorkspace(initial.ActiveWorkspaceID)
	if err != nil || len(workspaceIssues(t, broken, initial.ActiveWorkspaceID)) != 1 || broken.LastError == "" {
		t.Fatalf("fixture did not report old-format failure: %v", err)
	}
	fixed, err := s.ImportRulePackage(archive, "replace")
	if err != nil || len(workspaceIssues(t, fixed, initial.ActiveWorkspaceID)) != 0 || len(fixed.Rules) != 1 || fixed.LastError != "" {
		t.Fatalf("import did not clear the diagnosed catalog failure: %v", err)
	}
	if readTest(t, legacyPath) != "old" {
		t.Fatal("repair changed the previous rule package")
	}
}

func TestWorkspaceQueueAndCatalogIssuesAreIndependent(t *testing.T) {
	s, root := workspaceTestService(t)
	if _, err := s.CreateWorkspace("two failures", root); err != nil {
		t.Fatal(err)
	}
	initial, err := s.CreateRule(ruleEdit("R001", "rule", ""))
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, initial.Config.QueuePath, []byte("invalid queue JSON"))
	writeTest(t, filepath.Join(initial.Config.RulesPath, "R001", "name.txt"), []byte("old"))
	st, err := s.SelectWorkspace(initial.ActiveWorkspaceID)
	issues := workspaceIssues(t, st, initial.ActiveWorkspaceID)
	if err != nil || len(issues) != 2 || issues[0].ID != "catalog" || issues[1].ID != "queue" || issues[1].Page != "results" {
		t.Fatalf("queue failure hid the rule failure: %#v / %v", issues, err)
	}
	if err = store.SaveQueue(initial.Config.QueuePath, nil); err != nil {
		t.Fatal(err)
	}
	writeTest(t, initial.Config.QueuePath+".session.json", []byte("invalid session JSON"))
	st, err = s.GetState()
	issues = workspaceIssues(t, st, initial.ActiveWorkspaceID)
	if err != nil || len(issues) != 2 || issues[1].Page != "results" {
		t.Fatalf("session restoration error lacks results destination: %#v / %v", issues, err)
	}
}

func TestWorkspaceRunPreparationIssuesHaveActionableDestinations(t *testing.T) {
	for _, test := range []struct {
		name        string
		breakSource bool
		id          string
		page        string
		ruleID      string
	}{
		{name: "source changed after scan", breakSource: true, id: "run", page: "results"},
		{name: "invalid rule after scan", id: "catalog", page: "rules", ruleID: "R019"},
	} {
		t.Run(test.name, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
			if test.breakSource {
				writeTest(t, filepath.Join(cfg.Root, "A.txt"), []byte("Legacy.Save() // changed\n"))
			} else {
				writeTest(t, filepath.Join(cfg.RulesPath, "R019", "rule.json"), []byte("invalid rule JSON"))
			}
			st := runTest(t, s, 1)
			issues := workspaceIssues(t, st, st.ActiveWorkspaceID)
			if len(issues) != 1 || issues[0].ID != test.id || issues[0].Page != test.page || issues[0].RuleID != test.ruleID || issues[0].File != "" {
				t.Fatalf("preparation failure has wrong destination: %#v", issues)
			}
			if st.Running || st.CurrentFile != "" || st.LastError == "" || st.Tasks[0].Attempts != 0 {
				t.Fatalf("preparation failure unexpectedly processed a file: %#v", st)
			}
		})
	}
}

func TestWorkspaceFailedTasksHaveRedactedFileDestinations(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	secret := s.state.Config.Credential
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		return model.Proposal{}, errors.New("provider failed " + secret)
	}
	if err := s.Start(1); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	issues := workspaceIssues(t, st, st.ActiveWorkspaceID)
	if len(issues) != 1 || issues[0].ID != "task:A.txt" || issues[0].Page != "results" || issues[0].File != "A.txt" {
		t.Fatalf("failed file lacks a specific result destination: %#v", issues)
	}
	if strings.Contains(issues[0].Message, secret) || !strings.Contains(issues[0].Message, "[redacted]") {
		t.Fatal("workspace issue exposed a provider credential")
	}
	otherRoot := t.TempDir()
	initTargetTestGit(t, otherRoot)
	if _, err := s.CreateWorkspace("other", otherRoot); err != nil {
		t.Fatal(err)
	}
	if len(workspaceIssues(t, s.Snapshot(), st.ActiveWorkspaceID)) != 1 {
		t.Fatal("switching away forgot the failed task")
	}
	if _, err := s.SelectWorkspace(st.ActiveWorkspaceID); err != nil {
		t.Fatal(err)
	}
	retried, err := s.RetryTasks([]string{"A.txt"})
	if err != nil || len(workspaceIssues(t, retried, st.ActiveWorkspaceID)) != 0 {
		t.Fatalf("retry retained a resolved failed-task issue: %v", err)
	}
}
