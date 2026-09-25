package engine

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"onebyone/internal/demopreset"
	"onebyone/internal/model"
)

func TestDemoProjectCreatesIndependentCommittedChildrenWithoutSelectingWorkspace(t *testing.T) {
	s, source := workspaceTestService(t)
	parent := t.TempDir()
	previous := filepath.Join(parent, demopreset.ProjectName)
	writeTest(t, filepath.Join(previous, "keep.txt"), []byte("keep earlier example"))
	previousTree, sourceTree := workspaceTree(t, previous), workspaceTree(t, source)
	initial := s.Snapshot()
	first, err := s.CreateDemoProject(parent)
	if err != nil {
		t.Fatal(err)
	}
	if !sameRoot(filepath.Dir(first), parent) || !strings.HasPrefix(filepath.Base(first), demopreset.ProjectName+"-") || !reflect.DeepEqual(initial, s.Snapshot()) {
		t.Fatal("demo creation changed workspace selection or used the chosen parent directly")
	}
	files, err := demopreset.ProjectFiles()
	if err != nil {
		t.Fatal(err)
	}
	tracked := zeroLines(gitTest(t, first, "ls-files", "-z"))
	if len(tracked) != demopreset.ProjectFileCount || len(files) != len(tracked) {
		t.Fatalf("demo source was omitted from the initial commit: %d files", len(tracked))
	}
	for path, body := range files {
		if readTest(t, filepath.Join(first, filepath.FromSlash(path))) != string(body) {
			t.Fatalf("demo file differs from bundled content: %s", path)
		}
	}
	if _, err := s.ValidateTargetFolder(first); err != nil {
		t.Fatalf("new demo is not ready for selection: %v", err)
	}
	if gitTest(t, first, "log", "-1", "--format=%an <%ae>") != "OneByOne <onebyone@localhost>" {
		t.Fatal("demo initial commit inherited another Git identity")
	}
	firstTree := workspaceTree(t, first)
	second, err := s.CreateDemoProject(parent)
	if err != nil || sameRoot(first, second) || !sameRoot(filepath.Dir(second), parent) {
		t.Fatalf("repeated demo creation did not allocate a new sibling: %v", err)
	}
	assertWorkspaceTree(t, first, firstTree)
	assertWorkspaceTree(t, previous, previousTree)
	assertWorkspaceTree(t, source, sourceTree)
	if _, err := s.CreateDemoProject(first); err == nil {
		t.Fatal("demo was nested inside an existing Git repository")
	}
	assertWorkspaceTree(t, first, firstTree)
}

func TestDemoProjectCreationKeepsReadOnlyWorkspaceAndRejectsManagedOrInvalidParents(t *testing.T) {
	s, source := workspaceTestService(t)
	created, err := s.CreateWorkspace("current", source)
	if err != nil {
		t.Fatal(err)
	}
	reader := workspaceNewService(t, s.configPath)
	initial := reader.Snapshot()
	if !initial.ReadOnly {
		t.Fatal("reader did not open the existing workspace read-only")
	}
	if _, err := reader.CreateDemoProject(t.TempDir()); err != nil || !reflect.DeepEqual(initial, reader.Snapshot()) {
		t.Fatalf("independent demo creation changed or depended on workspace edit rights: %v", err)
	}
	managed := filepath.Dir(workspaceSettingPath(t, s, created.ActiveWorkspaceID))
	managedTree := workspaceTree(t, managed)
	for _, parent := range []string{"", source, managed, filepath.Join(t.TempDir(), "missing")} {
		if _, err := s.CreateDemoProject(parent); err == nil {
			t.Fatalf("unsafe or invalid demo parent accepted: %s", parent)
		}
	}
	assertWorkspaceTree(t, managed, managedTree)
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	_, err = s.CreateDemoProject(t.TempDir())
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	if err == nil {
		t.Fatal("demo project creation was allowed while running")
	}
}

func TestDemoProjectGitFailureRemovesOnlyItsNewChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the failure executable uses a POSIX shell")
	}
	s, _ := workspaceTestService(t)
	parent := t.TempDir()
	previous := filepath.Join(parent, "previous")
	writeTest(t, filepath.Join(previous, "keep.txt"), []byte("keep this tree"))
	before := workspaceTree(t, previous)
	bin := t.TempDir()
	gitPath := filepath.Join(bin, "git")
	writeTest(t, gitPath, []byte("#!/bin/sh\nif [ \"$1\" = \"--version\" ]; then\n  echo 'git version 2.50.1'\n  exit 0\nfi\nexit 1\n"))
	if err := os.Chmod(gitPath, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	if _, err := s.CreateDemoProject(parent); err == nil {
		t.Fatal("failed Git initialization was reported as success")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 || entries[0].Name() != "previous" {
		t.Fatalf("failure retained a partial example or removed earlier data: %v / %v", entries, err)
	}
	assertWorkspaceTree(t, previous, before)
}

func TestDemoRuleImportPreservesSourceConnectionHistoryAndUsesExistingWriteGuards(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxAttempts, cfg.MaxTurns, cfg.MaxOutputTokens, cfg.MaxFileBytes, cfg.TimeoutSeconds = 1, 4, 2048, 65536, 180
	cfg.MaxCostUSD, cfg.InputPricePerMillion, cfg.CachedInputPricePerMillion, cfg.OutputPricePerMillion = 3, 4, 0.4, 12
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	sourceTree, queueTree := workspaceTree(t, cfg.Root), workspaceTree(t, filepath.Dir(cfg.QueuePath))
	if _, err := s.OpenRule("R001"); err != nil {
		t.Fatal(err)
	}
	imported, err := s.ImportDemoRules()
	if err != nil {
		t.Fatal(err)
	}
	if imported.Config.Root != before.Config.Root || imported.Config.QueuePath != before.Config.QueuePath || imported.SelectedLLMConnectionID != before.SelectedLLMConnectionID || imported.Config.CredentialSet != before.Config.CredentialSet || !reflect.DeepEqual(imported.Tasks, before.Tasks) {
		t.Fatal("demo rules replaced source selection, connection or prior history")
	}
	assertWorkspaceExecutionSettings(t, imported.Config, before.Config)
	common, individual := 0, 0
	for _, rule := range imported.Rules {
		if rule.Always {
			common++
		} else {
			individual++
		}
	}
	if common != demopreset.CommonRuleCount || individual != demopreset.IndividualRuleCount || s.ruleLease != nil {
		t.Fatal("demo rules had incorrect counts or retained the former rule edit lease")
	}
	if err := s.Start(1); err == nil || !strings.Contains(err.Error(), "対象抽出を再実行") {
		t.Fatalf("demo rule replacement did not require a new scan: %v", err)
	}
	reader := workspaceNewService(t, s.configPath)
	if _, err := reader.ImportDemoRules(); err == nil {
		t.Fatal("read-only workspace replaced its rule package")
	}
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	_, err = s.ImportDemoRules()
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	if err == nil {
		t.Fatal("running workspace replaced its rule package")
	}
	assertWorkspaceTree(t, cfg.Root, sourceTree)
	assertWorkspaceTree(t, filepath.Dir(cfg.QueuePath), queueTree)
}

func TestDemoWorkspacePublishesBundledRulesWithNewWorkspace(t *testing.T) {
	s, source := workspaceTestService(t)
	previous, err := s.CreateWorkspace("previous", source)
	if err != nil {
		t.Fatal(err)
	}
	demoRoot, err := s.CreateDemoProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before := workspaceTree(t, demoRoot)
	created, err := s.CreateDemoWorkspace("demo workspace", demoRoot)
	if err != nil || created.ActiveWorkspaceID == previous.ActiveWorkspaceID || created.Config.Root != demoRoot || len(created.Rules) != demopreset.CommonRuleCount+demopreset.IndividualRuleCount || len(created.Tasks) != 0 {
		t.Fatalf("new demo workspace was not fully configured before selection: %v", err)
	}
	if created.Config.RulesPath == "" || isAtOrWithin(demoRoot, created.Config.RulesPath) || created.Config.QueuePath == previous.Config.QueuePath || s.leaseID != created.ActiveWorkspaceID {
		t.Fatal("demo workspace did not isolate its managed assets and lease")
	}
	assertWorkspaceExecutionSettings(t, created.Config, model.Config{})
	loaded := workspaceNewService(t, s.configPath).Snapshot()
	if loaded.ActiveWorkspaceID != created.ActiveWorkspaceID || loaded.LastError != "" || len(loaded.Rules) != len(created.Rules) {
		t.Fatalf("another app observed an incomplete demo workspace: %s", loaded.LastError)
	}
	assertWorkspaceTree(t, demoRoot, before)
}

func TestDemoWorkspaceFailedPublicationRetainsPriorStateAndRemovesNewSettings(t *testing.T) {
	s, source := workspaceTestService(t)
	previous, err := s.CreateWorkspace("keep current", source)
	if err != nil {
		t.Fatal(err)
	}
	demoRoot, err := s.CreateDemoProject(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	victim := filepath.Join(t.TempDir(), "do-not-overwrite")
	writeTest(t, victim, []byte("unchanged"))
	if err := os.Remove(s.configPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, s.configPath); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	lease := s.workspaceLease
	if _, err := s.CreateDemoWorkspace("must roll back", demoRoot); err == nil {
		t.Fatal("invalid active settings destination unexpectedly succeeded")
	}
	if !reflect.DeepEqual(s.Snapshot(), previous) || s.workspaceLease != lease || s.leaseID != previous.ActiveWorkspaceID || readTest(t, victim) != "unchanged" {
		t.Fatal("failed demo publication replaced the previous selection or files")
	}
	workspaces, err := s.listLocalWorkspaces()
	if err != nil || len(workspaces) != 1 || workspaces[0].ID != previous.ActiveWorkspaceID {
		t.Fatalf("failed demo creation left a partially configured workspace: %v", err)
	}
}
