package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/diagnostics"
	"onebyone/internal/model"
)

func importTestDiagnostics(t *testing.T, s *Service, file, message string) model.State {
	t.Helper()
	path := filepath.Join(t.TempDir(), "compiler-errors.jsonl")
	data, err := diagnostics.Encode([]model.SourceDiagnostic{{File: file, Line: 7, Code: "E001", Message: message}})
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, path, data)
	st, err := s.ImportDiagnostics(path)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func TestDiagnosticsImportUsesImmutablePrivateCopyAndAtomicReplacement(t *testing.T) {
	s, root := workspaceTestService(t)
	if _, err := s.CreateWorkspace("diagnostics", root); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "errors.jsonl")
	first := []byte(`{"file":"src/A.cs","message":"initial error"}` + "\n")
	writeTest(t, external, first)
	st, err := s.ImportDiagnostics(external)
	if err != nil {
		t.Fatal(err)
	}
	imported := st.Config.DiagnosticsPath
	if imported == external || st.Diagnostics == nil || *st.Diagnostics != (model.DiagnosticSummary{Name: "errors.jsonl", Count: 1, FileCount: 1}) {
		t.Fatalf("import did not publish an independent summary: %+v", st.Diagnostics)
	}
	if !strings.HasPrefix(imported, filepath.Join(filepath.Dir(workspaceSettingPath(t, s, st.ActiveWorkspaceID)), "diagnostics")+string(filepath.Separator)) {
		t.Fatal("diagnostics were not stored inside their workspace")
	}
	if mode, err := os.Stat(imported); err != nil || runtime.GOOS != "windows" && mode.Mode().Perm()&0077 != 0 {
		t.Fatalf("private diagnostic file permissions: %v %v", mode, err)
	}
	writeTest(t, external, []byte(`{"file":"src/A.cs","message":"changed outside app"}`))
	items, err := diagnostics.Read(imported)
	if err != nil || items[0].Message != "initial error" {
		t.Fatalf("external changes affected imported evidence: %#v %v", items, err)
	}
	beforeSetting := readTest(t, workspaceSettingPath(t, s, st.ActiveWorkspaceID))
	writeTest(t, external, []byte(`{"file":"src/A.cs","message":"replacement","line":0}`))
	if _, err = s.ImportDiagnostics(external); err == nil {
		t.Fatal("invalid import was accepted")
	}
	if s.Snapshot().Config.DiagnosticsPath != imported || readTest(t, workspaceSettingPath(t, s, st.ActiveWorkspaceID)) != beforeSetting {
		t.Fatal("failed import changed current configuration")
	}
	writeTest(t, external, []byte(`{"file":"src/B.cs","message":"replacement"}`))
	replaced, err := s.ImportDiagnostics(external)
	if err != nil || replaced.Config.DiagnosticsPath == imported {
		t.Fatalf("replacement did not allocate a new version: %v", err)
	}
	if old, err := diagnostics.Read(imported); err != nil || old[0].Message != "initial error" {
		t.Fatal("replacement mutated the previous version")
	}
	if readTest(t, external) != `{"file":"src/B.cs","message":"replacement"}` {
		t.Fatal("import rewrote the supplied file")
	}
}

func TestDiagnosticsWorkspaceIsolationDuplicateClearAndTargetChange(t *testing.T) {
	s, root := workspaceTestService(t)
	if _, err := s.CreateWorkspace("one", root); err != nil {
		t.Fatal(err)
	}
	one := importTestDiagnostics(t, s, "A.cs", "workspace one")
	cfg := one.Config
	cfg.DiagnosticsOnly = true
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	duplicate, err := s.DuplicateWorkspace("copy")
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Config.DiagnosticsPath == one.Config.DiagnosticsPath || duplicate.Diagnostics == nil || duplicate.Diagnostics.Name != one.Diagnostics.Name || !duplicate.Config.DiagnosticsOnly {
		t.Fatal("workspace duplicate did not copy diagnostic data and settings")
	}
	duplicatePath := duplicate.Config.DiagnosticsPath
	cfg = duplicate.Config
	cfg.DiagnosticsPath = one.Config.DiagnosticsPath
	if st, err := s.SaveConfig(cfg); err != nil || st.Config.DiagnosticsPath != duplicatePath {
		t.Fatalf("settings form redirected the app-owned import: %v", err)
	}
	if cleared, err := s.ClearDiagnostics(); err != nil || cleared.Diagnostics != nil || cleared.Config.DiagnosticsPath != "" || cleared.Config.DiagnosticsOnly {
		t.Fatalf("clear did not release diagnostic scope: %+v %v", cleared.Diagnostics, err)
	}
	if _, err := os.Stat(duplicatePath); err != nil {
		t.Fatal("clear deleted earlier evidence")
	}
	cfg = s.Snapshot().Config
	cfg.DiagnosticsOnly = true
	if _, err = s.SaveConfig(cfg); err == nil {
		t.Fatal("diagnostics-only mode accepted without loaded diagnostics")
	}
	restored, err := s.SelectWorkspace(one.ActiveWorkspaceID)
	if err != nil || restored.Config.DiagnosticsPath != one.Config.DiagnosticsPath || restored.Diagnostics == nil || !restored.Config.DiagnosticsOnly {
		t.Fatalf("switch changed the other workspace's diagnostics: %+v %v", restored.Diagnostics, err)
	}
	otherRoot := t.TempDir()
	initTargetTestGit(t, otherRoot)
	changed, err := s.ChangeTargetFolder(otherRoot)
	if err != nil || changed.Config.DiagnosticsPath != "" || changed.Config.DiagnosticsOnly || changed.Diagnostics != nil {
		t.Fatalf("target change carried unrelated diagnostics: %+v %v", changed.Diagnostics, err)
	}
	if _, err = os.Stat(one.Config.DiagnosticsPath); err != nil {
		t.Fatal("target change deleted evidence from earlier target")
	}
}

func TestDiagnosticsExtractionAndAttemptEvidenceSurviveReplacementAndReload(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	st := importTestDiagnostics(t, s, "A.txt", "first evidence")
	cfg := st.Config
	cfg.DiagnosticsOnly = true
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(0); err == nil || !strings.Contains(err.Error(), "対象抽出") {
		t.Fatalf("changed diagnostics did not invalidate extracted scope: %v", err)
	}
	st, err := s.Scan()
	if err != nil || len(st.Tasks) != 1 || len(st.Tasks[0].SourceDiagnostics) != 1 || st.Tasks[0].SourceDiagnostics[0].Message != "first evidence" {
		t.Fatalf("extract imported evidence: %+v %v", st.Tasks, err)
	}
	var input []model.SourceDiagnostic
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		input = copySourceDiagnostics(in.SourceDiagnostics)
		return model.Proposal{Outcome: "needs_human", Note: "Review the referenced API before editing."}, nil
	}
	st = runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" || len(st.Tasks[0].History) != 1 {
		t.Fatalf("attempt failed: %+v / %s", st.Tasks, st.LastError)
	}
	original := st.Tasks[0]
	if !reflect.DeepEqual(input, original.SourceDiagnostics) || !reflect.DeepEqual(original.History[0].SourceDiagnostics, original.SourceDiagnostics) {
		t.Fatalf("attempt and agent did not receive frozen evidence: input=%+v attempt=%+v", input, original.History[0])
	}
	replacement := importTestDiagnostics(t, s, "A.txt", "updated evidence")
	// The saved queue still names the previous version at this point.
	if loaded, err := s.LoadQueue(st.Config.QueuePath); err != nil || loaded.Config.DiagnosticsPath != replacement.Config.DiagnosticsPath || !loaded.Config.DiagnosticsOnly {
		t.Fatalf("queue load rolled back the latest diagnostic import or mode: %v", err)
	}
	st, err = s.Scan()
	if err != nil || st.Tasks[0].SourceDiagnostics[0].Message != "updated evidence" || !reflect.DeepEqual(st.Tasks[0].History, original.History) || st.Tasks[0].Status != original.Status {
		t.Fatalf("rescan changed historical evidence or automatically retried: %+v %v", st.Tasks, err)
	}
	importTestDiagnostics(t, s, "B.txt", "other file evidence")
	st, err = s.Scan()
	if err != nil {
		t.Fatal(err)
	}
	var previous model.Task
	for _, task := range st.Tasks {
		if task.File == "A.txt" {
			previous = task
		}
	}
	if !previous.Excluded || len(previous.SourceDiagnostics) != 0 || !reflect.DeepEqual(previous.History, original.History) {
		t.Fatalf("removed scope retained current evidence or lost history: %+v", previous)
	}
	s.Close()
	reopened := workspaceNewService(t, s.configPath)
	reloaded := reopened.Snapshot()
	if reloaded.Diagnostics == nil || reloaded.Diagnostics.Name != "compiler-errors.jsonl" || !reflect.DeepEqual(reloaded.Tasks, st.Tasks) {
		t.Fatalf("workspace reload did not retain diagnostic snapshots: %+v", reloaded)
	}
	if _, err := reopened.ClearDiagnostics(); err != nil {
		t.Fatal(err)
	}
	st, err = reopened.Scan()
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range st.Tasks {
		if len(task.SourceDiagnostics) != 0 {
			t.Fatal("normal extraction retained removed diagnostics")
		}
		if task.File == "A.txt" && !reflect.DeepEqual(task.History, original.History) {
			t.Fatal("clear and rescan rewrote earlier attempt evidence")
		}
	}
}

func TestMissingDiagnosticsRemainRepairableAndDoNotBlockOtherWorkspaces(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	one := importTestDiagnostics(t, s, "A.cs", "error")
	other, err := s.CreateWorkspace("independent", cfg.Root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(one.Config.DiagnosticsPath); err != nil {
		t.Fatal(err)
	}
	if st, err := s.SaveConfig(other.Config); err != nil || st.Diagnostics != nil || st.Config.DiagnosticsPath != "" {
		t.Fatalf("unrelated workspace was affected by the missing import: %v", err)
	}
	if st, err := s.SelectWorkspace(one.ActiveWorkspaceID); err != nil || st.ActiveWorkspaceID != one.ActiveWorkspaceID || st.LastError == "" {
		t.Fatalf("missing diagnostic copy was not reported on its own workspace: %+v %v", st, err)
	} else {
		if len(st.Rules) == 0 {
			t.Fatal("diagnostic failure hid the rules and blocked access to import settings")
		}
		found := false
		for _, workspace := range st.Workspaces {
			if workspace.ID != one.ActiveWorkspaceID {
				continue
			}
			for _, issue := range workspace.Issues {
				if issue.ID == "diagnostics" && issue.Page == "run" {
					found = true
				}
				if issue.Page == "rules" {
					t.Fatal("diagnostic failure was reported as a rule setup error")
				}
			}
		}
		if !found {
			t.Fatal("diagnostic issue did not link to execution settings")
		}
	}
	for _, operation := range []func() error{
		func() error { _, err := s.Scan(); return err },
		func() error { return s.Start(0) },
	} {
		if err := operation(); err == nil {
			t.Fatal("execution accepted a missing diagnostic source")
		}
		for _, workspace := range s.Snapshot().Workspaces {
			if workspace.ID == one.ActiveWorkspaceID {
				for _, issue := range workspace.Issues {
					if issue.Page == "rules" {
						t.Fatal("execution classified a diagnostic-source failure as a rule error")
					}
				}
			}
		}
	}
	if st, err := s.ClearDiagnostics(); err != nil || st.Config.DiagnosticsPath != "" {
		t.Fatalf("missing diagnostic import could not be cleared: %v", err)
	}
}

func TestDiagnosticInputInsideTargetStaysOutOfSourceScope(t *testing.T) {
	const report = `{"file":"A.txt","message":"error"}`
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "reports/errors.jsonl": report})
	input := filepath.Join(cfg.Root, "reports", "errors.jsonl")
	st, err := s.ImportDiagnostics(input)
	if err != nil || st.Config.DiagnosticsSourceFile != "reports/errors.jsonl" {
		t.Fatalf("input exclusion was not retained: %s %v", st.Config.DiagnosticsSourceFile, err)
	}
	form := st.Config
	form.DiagnosticsSourceFile = "A.txt"
	if st, err = s.SaveConfig(form); err != nil || st.Config.DiagnosticsSourceFile != "reports/errors.jsonl" {
		t.Fatalf("form save redirected the input exclusion: %v", err)
	}
	st, err = s.Scan()
	if err != nil || len(st.Tasks) != 1 || st.Tasks[0].File != "A.txt" {
		t.Fatalf("report was selected as source: %+v %v", st.Tasks, err)
	}
	if readTest(t, input) != report {
		t.Fatal("import changed the source-side report")
	}
	duplicate, err := s.DuplicateWorkspace("copy")
	if err != nil || duplicate.Config.DiagnosticsSourceFile != "reports/errors.jsonl" {
		t.Fatalf("duplicate forgot the report exclusion: %v", err)
	}
	if loaded, err := s.SelectWorkspace(st.ActiveWorkspaceID); err != nil || loaded.Config.DiagnosticsSourceFile != "reports/errors.jsonl" {
		t.Fatalf("workspace reload forgot the report exclusion: %v", err)
	}
	if cleared, err := s.ClearDiagnostics(); err != nil || cleared.Config.DiagnosticsSourceFile != "" {
		t.Fatalf("clear retained the old report exclusion: %v", err)
	}
}

func TestCopyTaskDeepCopiesDiagnosticRuleIDs(t *testing.T) {
	d := []model.SourceDiagnostic{{File: "A.cs", Message: "original", RuleIDs: []string{"R1"}}}
	original := model.Task{SourceDiagnostics: d, History: []model.Attempt{{SourceDiagnostics: d}}}
	cloned := copyTask(original)
	cloned.SourceDiagnostics[0].Message = "changed"
	cloned.SourceDiagnostics[0].RuleIDs[0] = "R2"
	cloned.History[0].SourceDiagnostics[0].RuleIDs[0] = "R3"
	if original.SourceDiagnostics[0].Message != "original" || original.SourceDiagnostics[0].RuleIDs[0] != "R1" || original.History[0].SourceDiagnostics[0].RuleIDs[0] != "R1" {
		t.Fatal("copy aliases mutable diagnostic data")
	}
}
