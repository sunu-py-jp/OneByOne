package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/engine"
	"onebyone/internal/model"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

func workspaceRulesFixture(t *testing.T) (*engine.Service, string, string) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("ONEBYONE_PRIVATE_DIR", filepath.Join(base, "private"))
	root := filepath.Join(base, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	initCLITestRepository(t, root)
	s := engine.New(filepath.Join(base, "app", "settings.json"))
	t.Cleanup(s.Close)
	return s, root, base
}

func workspaceRulesCreate(t *testing.T, s *engine.Service, root string) model.State {
	t.Helper()
	result, err := executeWorkspaceCommand(context.Background(), s, []string{"create", "--name", "CLI workspace", "--root", root}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result.(model.State)
}

func workspaceRulesEdit() model.RuleEdit {
	return model.RuleEdit{ID: "R001", Name: "Preserve behavior", Overview: "Keep observable behavior while updating the storage API.", Before: "client.save(value)", After: "await client.write(value)", Notes: "Preserve call order.", HoldConditions: "An external caller must change.", Pattern: ""}
}

func workspaceRulesJSON(t *testing.T, value any) *bytes.Reader {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(data)
}

func TestCLIWorkspaceCommandsCreateSelectRenameDuplicateAndDeleteGuard(t *testing.T) {
	s, root, _ := workspaceRulesFixture(t)
	ctx := context.Background()
	first := workspaceRulesCreate(t, s, root)
	if first.ActiveWorkspaceID == "" || len(first.Workspaces) != 1 {
		t.Fatal("create did not select a local workspace")
	}
	if _, err := executeWorkspaceCommand(ctx, s, []string{"rename", "--name", "Renamed"}, nil); err != nil {
		t.Fatal(err)
	}
	result, err := executeWorkspaceCommand(ctx, s, []string{"duplicate", "--name", "Comparison"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second := result.(model.State)
	if second.ActiveWorkspaceID == first.ActiveWorkspaceID || len(second.Workspaces) != 2 {
		t.Fatal("duplicate did not create an independent workspace")
	}
	if _, err := executeWorkspaceCommand(ctx, s, []string{"delete", "--id", first.ActiveWorkspaceID}, nil); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("missing destructive confirmation was accepted: %v", err)
	}
	if s.Snapshot().ActiveWorkspaceID != second.ActiveWorkspaceID || len(s.Snapshot().Workspaces) != 2 {
		t.Fatal("delete guard changed the selected workspace")
	}
	if _, err := executeWorkspaceCommand(ctx, s, []string{"select", "--id", first.ActiveWorkspaceID}, nil); err != nil {
		t.Fatal(err)
	}
	if s.Snapshot().Workspaces[0].Name != "Renamed" && s.Snapshot().Workspaces[1].Name != "Renamed" {
		t.Fatal("rename was not persisted")
	}
	if _, err := executeWorkspaceCommand(ctx, s, []string{"delete", "--id", second.ActiveWorkspaceID, "--yes"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().Workspaces) != 1 || s.Snapshot().Workspaces[0].ID != first.ActiveWorkspaceID {
		t.Fatal("delete did not honor the explicit target ID")
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		t.Fatal("workspace deletion removed the source repository")
	}
}

func TestCLIWorkspaceRulesParseHelpAndCancellationHaveNoMutation(t *testing.T) {
	s, root, _ := workspaceRulesFixture(t)
	before := s.Snapshot()
	for _, args := range [][]string{
		{"create", "--name", "bad", "--root", root, "extra"},
		{"create", "--name", "bad", "--root", root, "--unknown"},
		{"list", "--name", "unexpected"},
		{"delete", "--id", "none", "--yes", "extra"},
	} {
		if _, err := executeWorkspaceCommand(context.Background(), s, args, nil); err == nil {
			t.Errorf("accepted malformed invocation: %v", args)
		}
	}
	if _, err := executeWorkspaceCommand(context.Background(), s, []string{"create", "--name", "no", "--root", root, "--help"}, nil); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("help did not stop before execution: %v", err)
	}
	if _, err := executeRulesCommand(context.Background(), s, []string{"create", "--input", "does-not-exist", "--help"}, nil); !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("rule help tried to read input: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := executeWorkspaceCommand(ctx, s, []string{"create", "--name", "no", "--root", root}, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled command was not rejected")
	}
	if !reflect.DeepEqual(before, s.Snapshot()) {
		t.Fatal("rejected command changed application state")
	}
}

func TestCLIWorkspaceRulesRouterPreflightDoesNotChangeGlobalSelection(t *testing.T) {
	s, root, base := workspaceRulesFixture(t)
	first := workspaceRulesCreate(t, s, root)
	if _, err := s.DuplicateWorkspace("Keep selected"); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(base, "app", "settings.json")
	s.Close()
	before, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{
		{"workspace", "delete", "--id", first.ActiveWorkspaceID},
		{"workspace", "rename", "--name", "no", "unexpected"},
		{"rules", "delete", "--id", "R001", "--yes", "--unknown"},
		{"rules", "create", "--input", "-", "unexpected"},
		{"rules", "import", "--input", "missing.oborules", "--mode", "invalid"},
		{"rules", "list", "--help=false"},
	} {
		var out, logs bytes.Buffer
		args := append(append([]string(nil), command...), "--config", config, "--workspace", first.ActiveWorkspaceID)
		code := executeContext(context.Background(), args, strings.NewReader(""), &out, &logs)
		if command[len(command)-1] == "--help=false" {
			if code != 0 {
				t.Fatalf("help was treated as execution error: %s", logs.String())
			}
		} else if code != 1 {
			t.Fatalf("malformed grouped command was accepted: %v, code=%d", command, code)
		}
		after, err := os.ReadFile(config)
		if err != nil || !bytes.Equal(after, before) {
			t.Fatalf("rejected/help command changed workspace selection: %v, error=%v", command, err)
		}
	}
}

func TestCLIRulesUpdateRequiresAndPreservesCallerRevision(t *testing.T) {
	s, root, _ := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	ctx := context.Background()
	edit := workspaceRulesEdit()
	if _, err := executeRulesCommand(ctx, s, []string{"create", "--input", "-"}, workspaceRulesJSON(t, edit)); err != nil {
		t.Fatal(err)
	}
	result, err := executeRulesCommand(ctx, s, []string{"show", "--id", edit.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	editor := result.(model.RuleEditor)
	if editor.ReadOnly || editor.Revision == "" || editor.Rule.Title != edit.Name {
		t.Fatal("show omitted the current rule or revision")
	}
	edit.Name = "First update"
	if _, err := executeRulesCommand(ctx, s, []string{"update", "--input", "-"}, workspaceRulesJSON(t, edit)); err == nil {
		t.Fatal("update accepted a missing revision")
	}
	edit.ExpectedRevision = editor.Revision
	if _, err := executeRulesCommand(ctx, s, []string{"update", "--input", "-"}, workspaceRulesJSON(t, edit)); err != nil {
		t.Fatal(err)
	}
	edit.Name = "Must not overwrite a newer edit"
	if _, err := executeRulesCommand(ctx, s, []string{"update", "--input", "-"}, workspaceRulesJSON(t, edit)); err == nil {
		t.Fatal("CLI substituted a fresh revision and accepted stale content")
	}
	if s.Snapshot().Rules[0].Title != "First update" {
		t.Fatal("stale update changed the rule")
	}
	if _, err := executeRulesCommand(ctx, s, []string{"delete", "--id", edit.ID}, nil); err == nil {
		t.Fatal("rule deletion did not require confirmation")
	}
	if _, err := executeRulesCommand(ctx, s, []string{"delete", "--id", edit.ID, "--yes"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().Rules) != 0 {
		t.Fatal("confirmed rule deletion did not remove the rule")
	}
}

func TestCLIRulesShowReleasesLeaseAndUpdateRespectsOtherEditor(t *testing.T) {
	s, root, base := workspaceRulesFixture(t)
	workspace := workspaceRulesCreate(t, s, root)
	ctx := context.Background()
	edit := workspaceRulesEdit()
	if _, err := executeRulesCommand(ctx, s, []string{"create", "--input", "-"}, workspaceRulesJSON(t, edit)); err != nil {
		t.Fatal(err)
	}
	shown, err := executeRulesCommand(ctx, s, []string{"show", "--id", edit.ID}, nil)
	if err != nil {
		t.Fatal(err)
	}
	edit.ExpectedRevision = shown.(model.RuleEditor).Revision
	lockPath := filepath.Join(base, "app", "workspace-locks", workspace.ActiveWorkspaceID, "rule-locks", "r001.lock")
	lease, owner, err := store.AcquireWorkspace(lockPath, store.WorkspaceOwner{Owner: "another editor", Host: "test-host"})
	if err != nil || owner != nil || lease == nil {
		t.Fatalf("show/create left an editing lease behind: %v, owner=%+v", err, owner)
	}
	defer lease.Release()
	shown, err = executeRulesCommand(ctx, s, []string{"show", "--id", edit.ID}, nil)
	if err != nil || !shown.(model.RuleEditor).ReadOnly {
		t.Fatalf("show failed to report the other editor: %v", err)
	}
	edit.Name = "Blocked edit"
	if _, err := executeRulesCommand(ctx, s, []string{"update", "--input", "-"}, workspaceRulesJSON(t, edit)); err == nil || !strings.Contains(err.Error(), "閲覧専用") {
		t.Fatalf("update ignored another editor's lock: %v", err)
	}
	if s.Snapshot().Rules[0].Title != "Preserve behavior" {
		t.Fatal("locked rule was modified")
	}
}

func TestCLIRulesImportRequiresModeAndMergesConflictingIDs(t *testing.T) {
	s, root, base := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	ctx := context.Background()
	edit := workspaceRulesEdit()
	if _, err := executeRulesCommand(ctx, s, []string{"create", "--input", "-"}, workspaceRulesJSON(t, edit)); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(base, "rules.oborules")
	result, err := executeRulesCommand(ctx, s, []string{"export", "--output", archive}, nil)
	if err != nil || result.(map[string]string)["path"] == "" {
		t.Fatalf("export failed: %v", err)
	}
	if _, err := rulepack.Read(archive); err != nil {
		t.Fatal("export did not produce a valid package:", err)
	}
	if _, err := executeRulesCommand(ctx, s, []string{"import", "--input", archive}, nil); err == nil || !strings.Contains(err.Error(), "--mode") {
		t.Fatalf("import silently replaced existing rules: %v", err)
	}
	if _, err := executeRulesCommand(ctx, s, []string{"import", "--input", archive, "--mode", "merge"}, nil); err != nil {
		t.Fatal(err)
	}
	rules := s.Snapshot().Rules
	if len(rules) != 2 || rules[0].ID != "R001" || rules[1].ID != "R001_2" {
		t.Fatalf("merge overwrote a conflicting rule ID: %+v", rules)
	}
	if _, err := executeRulesCommand(ctx, s, []string{"import", "--input", archive, "--mode", "replace"}, nil); err != nil {
		t.Fatal(err)
	}
	if len(s.Snapshot().Rules) != 1 {
		t.Fatal("explicit replace did not replace the rule collection")
	}
}

func TestCLIRulesDeleteCanRepairWorkspaceWithInvalidRule(t *testing.T) {
	s, root, base := workspaceRulesFixture(t)
	workspaceRulesCreate(t, s, root)
	ctx := context.Background()
	edit := workspaceRulesEdit()
	if _, err := executeRulesCommand(ctx, s, []string{"create", "--input", "-"}, workspaceRulesJSON(t, edit)); err != nil {
		t.Fatal(err)
	}
	badRule := filepath.Join(s.Snapshot().Config.RulesPath, edit.ID, "rule.json")
	if err := os.WriteFile(badRule, []byte(`{"version":999,"name":"invalid rule"}`), 0600); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened := engine.New(filepath.Join(base, "app", "settings.json"))
	defer reopened.Close()
	if reopened.Snapshot().LastError == "" {
		t.Fatal("invalid rule fixture did not produce a workspace diagnostic")
	}
	if _, err := executeRulesCommand(ctx, reopened, []string{"delete", "--id", edit.ID, "--yes"}, nil); err != nil {
		t.Fatalf("workspace diagnostic prevented deleting the invalid rule: %v", err)
	}
	if _, err := os.Stat(filepath.Join(reopened.Snapshot().Config.RulesPath, edit.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid rule remains in the active package: %v", err)
	}
}
