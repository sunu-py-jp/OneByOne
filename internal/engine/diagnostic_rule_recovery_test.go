package engine

import (
	"os"
	"testing"

	"onebyone/internal/model"
)

func TestRuleDraftCanBeSavedBeforeRepairingMissingDiagnosticImport(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	imported := importTestDiagnostics(t, s, "A.txt", "API missing")
	if err := os.Remove(imported.Config.DiagnosticsPath); err != nil {
		t.Fatal(err)
	}
	rule, err := s.OpenRule("R001")
	if err != nil {
		t.Fatal(err)
	}
	edit := model.RuleEdit{ID: rule.Rule.ID, Name: rule.Rule.Title, Description: rule.Rule.Summary, Body: rule.Rule.Body + "\nPreserve unrelated behavior.", PathPattern: rule.Rule.PathPattern, ContentPattern: rule.Rule.ContentPattern, DiagnosticPattern: rule.Rule.DiagnosticPattern, ExpectedRevision: rule.Revision}
	state, err := s.SaveRules([]model.RuleEdit{edit})
	if err != nil {
		t.Fatalf("valid rule draft blocked diagnostic recovery: %v", err)
	}
	if state.Config.DiagnosticsPath != imported.Config.DiagnosticsPath {
		t.Fatal("rule save silently discarded diagnostic import")
	}
	found := false
	for _, workspace := range state.Workspaces {
		if workspace.ID == state.ActiveWorkspaceID {
			for _, issue := range workspace.Issues {
				if issue.ID == "diagnostics" && issue.Page == "run" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("missing diagnostic issue hidden by rule save")
	}
	if _, err := s.Scan(); err == nil {
		t.Fatal("broken diagnostics allowed actual extraction")
	}
	if _, err := s.ClearDiagnostics(); err != nil {
		t.Fatalf("could not repair after draft save: %v", err)
	}
}
