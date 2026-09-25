package engine

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

func batchRuleDiagnostic(t *testing.T, err error, id string) {
	t.Helper()
	var diagnostic *catalog.DiagnosticError
	if !errors.As(err, &diagnostic) || diagnostic.RuleID != id || diagnostic.Section != "" {
		t.Fatalf("error did not identify the affected rule %s: %v", id, err)
	}
}

func TestSaveRulesKeepsLiveConnectionClearAfterPersonalConnectionDeletion(t *testing.T) {
	s, _, _ := rulePackageService(t)
	id := rulePackageSelectConnection(t, s)
	if _, err := s.DeleteLLMConnection(id); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	if before.SelectedLLMConnectionID != "" {
		t.Fatal("deleted connection remained selected")
	}
	saved, err := s.SaveRules([]model.RuleEdit{ruleEdit("R001", "common", "")})
	if err != nil || saved.SelectedLLMConnectionID != "" || saved.Config.LLMConnectionID != "" || saved.Config.CredentialSet {
		t.Fatalf("batch save blocked or restored a deleted personal connection: %v", err)
	}
}

func TestSaveRulesAddsAndEditsInOneVersionWithoutChangingWorkspaceData(t *testing.T) {
	s, source, _ := rulePackageService(t)
	connectionID := rulePackageSelectConnection(t, s)
	common, individual := ruleEdit("R001", "common", ""), ruleEdit("R019", "individual", "Legacy")
	initial, err := s.SaveRules([]model.RuleEdit{common, individual})
	if err != nil || len(initial.Rules) != 2 {
		t.Fatalf("initial batch failed: %v", err)
	}
	helper := filepath.Join(initial.Config.RulesPath, "R001", "examples", "helper.txt")
	writeTest(t, helper, []byte("keep helper resource"))
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	queueBytes, err := os.ReadFile(before.Config.QueuePath)
	if err != nil {
		t.Fatal(err)
	}
	packageRoot := filepath.Dir(filepath.Dir(before.Config.RulesPath))
	versions, err := os.ReadDir(packageRoot)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.OpenRule(common.ID)
	if err != nil {
		t.Fatal(err)
	}
	common.ExpectedRevision, common.Notes = first.Revision, "edited notes"
	last, err := s.OpenRule(individual.ID)
	if err != nil {
		t.Fatal(err)
	}
	individual.ExpectedRevision, individual.After = last.Revision, "updated implementation"
	retainedLease := s.ruleLease
	added := ruleEdit("R200", "new rule", "Modern")
	saved, err := s.SaveRules([]model.RuleEdit{common, individual, added})
	if err != nil {
		t.Fatal(err)
	}
	newVersions, err := os.ReadDir(packageRoot)
	if err != nil || len(newVersions) != len(versions)+1 {
		t.Fatal("batch created more than one package version")
	}
	if len(saved.Rules) != 3 || saved.Rules[0].Notes != common.Notes || saved.Rules[1].After != individual.After || saved.Rules[2].ID != added.ID {
		t.Fatal("batch did not publish every draft together")
	}
	if saved.Config.Root != before.Config.Root || saved.Config.QueuePath != before.Config.QueuePath || saved.SelectedLLMConnectionID != connectionID || !saved.Config.CredentialSet || !reflect.DeepEqual(saved.Tasks, before.Tasks) || !reflect.DeepEqual(rulepack.FromConfig(saved.Config), rulepack.FromConfig(before.Config)) {
		t.Fatal("batch changed workspace, queue, tasks, personal connection, or processing settings")
	}
	currentQueue, err := os.ReadFile(saved.Config.QueuePath)
	if err != nil || !bytes.Equal(queueBytes, currentQueue) {
		t.Fatal("batch rewrote the existing queue")
	}
	asset, err := os.ReadFile(filepath.Join(saved.Config.RulesPath, "R001", "examples", "helper.txt"))
	if err != nil || string(asset) != "keep helper resource" {
		t.Fatal("batch lost auxiliary resources")
	}
	if s.ruleLease != retainedLease || s.ruleLeaseID != individual.ID {
		t.Fatal("batch released the active editor's lease")
	}
	for _, id := range []string{common.ID, added.ID} {
		lockPath, err := s.ruleLockPath(saved.ActiveWorkspaceID, id)
		if err != nil {
			t.Fatal(err)
		}
		if owner, err := store.PeekWorkspace(lockPath); err != nil || owner != nil {
			t.Fatalf("batch retained a temporary lease for %s: %v", id, err)
		}
	}
	oldData, err := os.ReadFile(filepath.Join(before.Config.RulesPath, common.ID, "rule.json"))
	if err != nil {
		t.Fatal(err)
	}
	old, err := ruleformat.Decode(oldData)
	if err != nil || old.Notes == common.Notes {
		t.Fatal("batch modified the previous package version")
	}
	assertRulePackageSourceUntouched(t, source)
}

func TestSaveRulesRejectsEntireBatchForValidationRevisionOrDuplicateFailure(t *testing.T) {
	s, source, _ := rulePackageService(t)
	original := ruleEdit("R001", "original", "")
	before, err := s.CreateRule(original)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.OpenRule(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	update := original
	update.ExpectedRevision, update.Notes = opened.Revision, "must not be partially saved"
	invalid := ruleEdit("R019", "invalid regex", "[invalid")
	invalidName := ruleEdit("R020", "", "")
	missingRevision := update
	missingRevision.ExpectedRevision = ""
	stale := update
	stale.ExpectedRevision = "outdated"
	deleted := ruleEdit("R099", "deleted elsewhere", "")
	deleted.ExpectedRevision = opened.Revision
	caseDuplicate := ruleEdit("r200", "case duplicate", "")
	setting := workspaceSettingPath(t, s, before.ActiveWorkspaceID)
	settings, err := os.ReadFile(setting)
	if err != nil {
		t.Fatal(err)
	}
	versionsPath := filepath.Dir(filepath.Dir(before.Config.RulesPath))
	versions, err := os.ReadDir(versionsPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name  string
		edits []model.RuleEdit
		id    string
	}{
		{"later invalid regex", []model.RuleEdit{update, invalid}, invalid.ID},
		{"later invalid name", []model.RuleEdit{update, invalidName}, invalidName.ID},
		{"missing revision", []model.RuleEdit{ruleEdit("R200", "addition", ""), missingRevision}, original.ID},
		{"stale revision", []model.RuleEdit{ruleEdit("R200", "addition", ""), stale}, original.ID},
		{"missing existing rule", []model.RuleEdit{update, deleted}, deleted.ID},
		{"duplicate IDs", []model.RuleEdit{update, ruleEdit("R200", "first copy", ""), caseDuplicate}, caseDuplicate.ID},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, err := s.SaveRules(scenario.edits)
			batchRuleDiagnostic(t, err, scenario.id)
			if state := s.Snapshot(); !reflect.DeepEqual(state.Config, before.Config) || !reflect.DeepEqual(state.Rules, before.Rules) || s.ruleLease == nil || s.ruleLeaseID != original.ID {
				t.Fatal("failed batch changed saved rules or released the active editor")
			}
			current, err := os.ReadFile(setting)
			if err != nil || !bytes.Equal(settings, current) {
				t.Fatal("failed batch changed saved settings")
			}
			if entries, err := os.ReadDir(versionsPath); err != nil || len(entries) != len(versions) {
				t.Fatal("failed batch retained an incomplete package")
			}
		})
	}
	assertRulePackageSourceUntouched(t, source)
}

func TestSaveRulesReadsCurrentDiskRevisionAndPreservesCanonicalID(t *testing.T) {
	s, _, _ := rulePackageService(t)
	original := ruleEdit("R019", "original", "Legacy")
	created, err := s.CreateRule(original)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.OpenRule(original.ID)
	if err != nil {
		t.Fatal(err)
	}
	current := editDefinition(original)
	current.Notes = "disk content changed after the draft was opened"
	writeTest(t, filepath.Join(created.Config.RulesPath, original.ID, "rule.json"), ruleDefinitionBytes(t, current))
	stale := original
	stale.ExpectedRevision = opened.Revision
	_, err = s.SaveRules([]model.RuleEdit{stale, ruleEdit("R200", "new", "")})
	batchRuleDiagnostic(t, err, original.ID)
	if s.Snapshot().Config.RulesPath != created.Config.RulesPath {
		t.Fatal("stale cached state replaced a newer disk definition")
	}
	stale.ID, stale.ExpectedRevision, stale.Notes = "r019", ruleformat.Revision(current), "reconciled change"
	saved, err := s.SaveRules([]model.RuleEdit{stale})
	if err != nil || len(saved.Rules) != 1 || saved.Rules[0].ID != "R019" || saved.Rules[0].Notes != stale.Notes {
		t.Fatalf("reconciled batch did not preserve the existing canonical ID: %v", err)
	}
	entries, err := os.ReadDir(saved.Config.RulesPath)
	if err != nil || len(entries) != 1 || entries[0].Name() != original.ID {
		t.Fatal("case-variant draft created a second rule directory")
	}
}

func TestSaveRulesHonorsBusyRuleAndWorkspaceGuards(t *testing.T) {
	s, _, _ := rulePackageService(t)
	initial := ruleEdit("R001", "active editor", "")
	before, err := s.CreateRule(initial)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := s.OpenRule(initial.ID)
	if err != nil {
		t.Fatal(err)
	}
	initial.ExpectedRevision, initial.Notes = opened.Revision, "do not partially save"
	newFirst, newBusy := ruleEdit("R100", "temporary lease", ""), ruleEdit("R200", "other editor", "")
	busyPath, err := s.ruleLockPath(before.ActiveWorkspaceID, newBusy.ID)
	if err != nil {
		t.Fatal(err)
	}
	otherLease, _, err := store.AcquireWorkspace(busyPath, store.WorkspaceOwner{Owner: "other editor", Host: "other host"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = otherLease.Release() })
	_, err = s.SaveRules([]model.RuleEdit{initial, newFirst, newBusy})
	batchRuleDiagnostic(t, err, newBusy.ID)
	if state := s.Snapshot(); !reflect.DeepEqual(state.Config, before.Config) || len(state.Rules) != 1 || s.ruleLease == nil || s.ruleLeaseID != initial.ID {
		t.Fatal("busy later rule partially committed the batch or released the open editor")
	}
	firstPath, err := s.ruleLockPath(before.ActiveWorkspaceID, newFirst.ID)
	if err != nil {
		t.Fatal(err)
	}
	if owner, err := store.PeekWorkspace(firstPath); err != nil || owner != nil {
		t.Fatalf("failed batch retained an earlier temporary lease: %v", err)
	}
	peer := New(s.configPath)
	t.Cleanup(peer.Close)
	if _, err := peer.SaveRules([]model.RuleEdit{newFirst}); err == nil {
		t.Fatal("read-only workspace accepted a batch")
	}
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	_, runningErr := s.SaveRules([]model.RuleEdit{newFirst})
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	if runningErr == nil {
		t.Fatal("running workspace accepted a batch")
	}
}
