package engine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
	"onebyone/internal/store"
)

func initTargetTestGit(t *testing.T, root string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required for target-folder validation")
	}
	gitTest(t, root, "init", "-q")
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "--allow-empty", "-qm", "initial")
}

func TestTargetFolderValidationReadsCleanCommittedSourceAndLinkedWorktrees(t *testing.T) {
	s, root := workspaceTestService(t)
	sub := filepath.Join(root, "src")
	writeTest(t, filepath.Join(sub, "tracked.txt"), []byte("unchanged source"))
	writeTest(t, filepath.Join(root, ".gitignore"), []byte("ignored.txt\n"))
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "tracked source")
	writeTest(t, filepath.Join(root, "ignored.txt"), []byte("ignored output is allowed"))
	before := workspaceTree(t, root)
	for _, selected := range []string{root, sub, filepath.Join(sub, ".")} {
		canonical, err := s.ValidateTargetFolder(selected)
		if err != nil || !sameRoot(canonical, selected) || !filepath.IsAbs(canonical) {
			t.Fatalf("Git worktree directory rejected: %s / %v", canonical, err)
		}
	}
	if _, err := s.CreateWorkspace("committed", sub); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceTree(t, root, before)
	linked := filepath.Join(t.TempDir(), "linked")
	gitTest(t, root, "worktree", "add", "-q", "-b", "validation", linked)
	linkedBefore := workspaceTree(t, linked)
	if _, err := s.ValidateTargetFolder(filepath.Join(linked, "src")); err != nil {
		t.Fatalf("linked Git worktree rejected: %v", err)
	}
	assertWorkspaceTree(t, linked, linkedBefore)
}

func TestTargetFolderRejectsMissingInitialCommitBeforeSaving(t *testing.T) {
	s, root := workspaceTestService(t)
	initial, err := s.CreateWorkspace("current", root)
	if err != nil {
		t.Fatal(err)
	}
	unborn := t.TempDir()
	gitTest(t, unborn, "init", "-q")
	writeTest(t, filepath.Join(unborn, "source.txt"), []byte("not committed"))
	fresh := workspaceNewService(t, filepath.Join(t.TempDir(), "app-settings.json"))
	before := workspaceTree(t, unborn)
	for name, operation := range map[string]func() error{
		"validate": func() error { _, err := s.ValidateTargetFolder(unborn); return err },
		"create":   func() error { _, err := s.CreateWorkspace("unborn", unborn); return err },
		"change":   func() error { _, err := s.ChangeTargetFolder(unborn); return err },
		"save new": func() error { c := DefaultConfig(); c.Root = unborn; _, err := fresh.SaveConfig(c); return err },
		"run":      func() error { c := initial.Config; c.Root = unborn; return s.prepareWorktree(context.Background(), c) },
	} {
		if err := operation(); err == nil || !strings.Contains(err.Error(), "初回コミット") {
			t.Fatalf("%s accepted an unborn repository: %v", name, err)
		}
	}
	if !reflect.DeepEqual(initial, s.Snapshot()) {
		t.Fatal("readiness rejection changed workspace state")
	}
	assertWorkspaceTree(t, unborn, before)
}

func TestExistingTargetWithoutHeadStillRestoresAndSavesUnrelatedSettings(t *testing.T) {
	s, root := workspaceTestService(t)
	initial, err := s.CreateWorkspace("existing", root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	gitTest(t, root, "symbolic-ref", "HEAD", "refs/heads/unborn")
	before := workspaceTree(t, root)
	reloaded := workspaceNewService(t, s.configPath)
	state := reloaded.Snapshot()
	if state.ActiveWorkspaceID != initial.ActiveWorkspaceID || state.ReadOnly || !strings.Contains(state.LastError, "初回コミット") {
		t.Fatalf("existing workspace could not restore with a readiness issue: %s", state.LastError)
	}
	c := state.Config
	c.MaxAttempts = 2
	saved, err := reloaded.SaveConfig(c)
	if err != nil || saved.Config.MaxAttempts != 2 {
		t.Fatalf("missing HEAD prevented unrelated settings save: %v", err)
	}
	issues := workspaceIssues(t, saved, initial.ActiveWorkspaceID)
	if len(issues) != 1 || issues[0].ID != "target" || !strings.Contains(issues[0].Message, "初回コミット") {
		t.Fatalf("saving settings hid the missing HEAD diagnostic: %#v", issues)
	}
	assertWorkspaceTree(t, root, before)
}

func TestTargetFolderReadinessRejectsEverySourceChangeIncludingOutsideSelectedSubfolder(t *testing.T) {
	for _, kind := range []string{"unstaged", "staged", "deleted", "renamed", "untracked", "outside subfolder"} {
		t.Run(kind, func(t *testing.T) {
			s, root := workspaceTestService(t)
			writeTest(t, filepath.Join(root, "A.txt"), []byte("original"))
			writeTest(t, filepath.Join(root, "src", "B.txt"), []byte("original"))
			gitTest(t, root, "add", ".")
			gitTest(t, root, "commit", "-qm", "source")
			initial, err := s.CreateWorkspace("current", root)
			if err != nil {
				t.Fatal(err)
			}
			selected := root
			switch kind {
			case "unstaged", "staged", "outside subfolder":
				writeTest(t, filepath.Join(root, "A.txt"), []byte("changed"))
				if kind == "staged" {
					gitTest(t, root, "add", "A.txt")
				} else if kind == "outside subfolder" {
					selected = filepath.Join(root, "src")
				}
			case "deleted":
				if err := os.Remove(filepath.Join(root, "A.txt")); err != nil {
					t.Fatal(err)
				}
			case "renamed":
				gitTest(t, root, "mv", "A.txt", "C.txt")
			case "untracked":
				writeTest(t, filepath.Join(root, "new.txt"), []byte("untracked"))
			}
			before := workspaceTree(t, root)
			_, validationErr := s.ValidateTargetFolder(selected)
			if validationErr == nil || !strings.Contains(validationErr.Error(), "未コミット") {
				t.Fatalf("source change was accepted: %v", validationErr)
			}
			for name, operation := range map[string]func() error{
				"create": func() error { _, err := s.CreateWorkspace("dirty", selected); return err },
				"change": func() error { _, err := s.ChangeTargetFolder(selected); return err },
				"run": func() error {
					c := initial.Config
					c.Root = selected
					return s.prepareWorktree(context.Background(), c)
				},
			} {
				if err := operation(); err == nil || err.Error() != validationErr.Error() {
					t.Fatalf("%s readiness differs from selection: %v", name, err)
				}
			}
			if !reflect.DeepEqual(initial, s.Snapshot()) {
				t.Fatal("readiness rejection changed workspace state")
			}
			c := initial.Config
			c.MaxAttempts = 2
			saved, err := s.SaveConfig(c)
			if err != nil || saved.Config.MaxAttempts != 2 || len(workspaceIssues(t, saved, initial.ActiveWorkspaceID)) != 1 {
				t.Fatalf("dirty source prevented unrelated settings save or hid diagnostic: %v", err)
			}
			assertWorkspaceTree(t, root, before)
		})
	}
}

func TestTargetFolderRejectsNonGitBareAndUnsafeLocationsWithoutSaving(t *testing.T) {
	s, root := workspaceTestService(t)
	plain := t.TempDir()
	writeTest(t, filepath.Join(plain, "keep.txt"), []byte("unchanged"))
	before := workspaceTree(t, plain)
	if _, err := s.ValidateTargetFolder(plain); err == nil || !strings.Contains(err.Error(), "Gitで管理") {
		t.Fatalf("non-Git directory did not receive the field error: %v", err)
	}
	if _, err := s.CreateWorkspace("invalid", plain); err == nil || s.Snapshot().ActiveWorkspaceID != "" {
		t.Fatal("non-Git directory created a workspace")
	}
	c := DefaultConfig()
	c.Root = plain
	if _, err := s.SaveConfig(c); err == nil || s.Snapshot().ActiveWorkspaceID != "" {
		t.Fatal("SaveConfig bypassed target validation")
	}
	assertWorkspaceTree(t, plain, before)
	initial, err := s.CreateWorkspace("valid", root)
	if err != nil {
		t.Fatal(err)
	}
	settingBefore := readTest(t, workspaceSettingPath(t, s, initial.ActiveWorkspaceID))
	if _, err = s.ChangeTargetFolder(plain); err == nil || !reflect.DeepEqual(initial, s.Snapshot()) {
		t.Fatal("rejected target change changed state")
	}
	if readTest(t, workspaceSettingPath(t, s, initial.ActiveWorkspaceID)) != settingBefore {
		t.Fatal("rejected target change wrote settings")
	}
	bare := t.TempDir()
	gitTest(t, bare, "init", "--bare", "-q")
	if _, err := s.ValidateTargetFolder(bare); err == nil || !strings.Contains(err.Error(), "Gitで管理") {
		t.Fatalf("bare repository accepted: %v", err)
	}
	managed := filepath.Dir(workspaceSettingPath(t, s, initial.ActiveWorkspaceID))
	for _, selected := range []string{managed, filepath.Dir(s.configPath), filepath.Dir(root), filepath.Join(filepath.Dir(s.configPath), "workspace-locks", initial.ActiveWorkspaceID)} {
		if _, err := s.ValidateTargetFolder(selected); err == nil {
			t.Fatalf("target overlapped local settings: %s", selected)
		}
	}
	if err := os.MkdirAll(initial.Config.QueuePath+".worktree", 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ChangeTargetFolder(initial.Config.QueuePath + ".worktree"); err == nil {
		t.Fatal("managed worktree accepted as source")
	}
	link := filepath.Join(t.TempDir(), "source-link")
	if err := os.Symlink(root, link); err == nil {
		if _, err := s.ValidateTargetFolder(link); err == nil {
			t.Fatal("symlink target accepted")
		}
	}
}

func TestTargetFolderReportsMissingGitSeparately(t *testing.T) {
	s, root := workspaceTestService(t)
	t.Setenv("PATH", t.TempDir())
	if _, err := s.ValidateTargetFolder(root); err == nil || !strings.Contains(err.Error(), "Gitが見つかりません") {
		t.Fatalf("missing executable was mistaken for a non-Git folder: %v", err)
	}
}

func TestTargetFolderChangePreservesSettingsArtifactsAndWorkspaceLease(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.mu.Lock()
	oldQueue := s.state.Config.QueuePath
	s.state.Tasks[0].Status = "done"
	s.state.Tasks[0].History = []model.Attempt{{ID: uid(), Number: 1, Outcome: "done", Usage: model.Usage{InputTokens: 42}}}
	s.meta.Worktree, s.meta.Branch = oldQueue+".worktree", "onebyone/retained"
	s.state.Worktree, s.state.Branch = s.meta.Worktree, s.meta.Branch
	s.recountLocked()
	s.mu.Unlock()
	writeTest(t, filepath.Join(oldQueue+".worktree", "keep.txt"), []byte("old correction"))
	writeTest(t, filepath.Join(oldQueue+".artifacts", "keep.diff"), []byte("old diff"))
	writeTest(t, filepath.Join(filepath.Dir(oldQueue), "reports", "keep.txt"), []byte("old report"))
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	before := s.Snapshot()
	lease := s.workspaceLease
	oldOutput := workspaceTree(t, filepath.Dir(oldQueue))
	oldSource := workspaceTree(t, before.Config.Root)
	nextRoot := t.TempDir()
	initTargetTestGit(t, nextRoot)
	nextSource := workspaceTree(t, nextRoot)
	after, err := s.ChangeTargetFolder(nextRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := before.Config
	want.Root, want.QueuePath = after.Config.Root, after.Config.QueuePath
	wantRules := append([]model.Rule(nil), before.Rules...)
	for i := range wantRules {
		wantRules[i].CandidateCount, wantRules[i].AppliedCount = 0, 0
	}
	if !reflect.DeepEqual(want, after.Config) || after.SelectedLLMConnectionID != before.SelectedLLMConnectionID || !reflect.DeepEqual(after.Rules, wantRules) {
		t.Fatal("target change lost rule or LLM settings")
	}
	if after.ActiveWorkspaceID != before.ActiveWorkspaceID || after.Workspaces[0].Name != before.Workspaces[0].Name || !sameRoot(after.Config.Root, nextRoot) || after.Config.QueuePath == oldQueue {
		t.Fatal("target change did not retain identity and separate output")
	}
	if len(after.Tasks) != 0 || after.Worktree != "" || after.Branch != "" || after.ScannedCount != 0 || after.ExcludedCount != 0 || after.Usage != (model.Usage{}) || len(after.Logs) != 0 || after.CurrentFile != "" || after.Phase != "idle" || !reflect.DeepEqual(s.meta, manifest{Version: 1}) {
		t.Fatal("target change retained stale execution state")
	}
	if s.workspaceLease != lease || s.leaseID != before.ActiveWorkspaceID || !sameRoot(s.leaseRoot, nextRoot) {
		t.Fatal("target change lost the workspace lease")
	}
	lockPath, err := s.workspaceLockPath(before.ActiveWorkspaceID, ".edit.lock")
	if err != nil {
		t.Fatal(err)
	}
	lock, owner, err := store.AcquireWorkspace(lockPath, store.WorkspaceOwner{})
	if lock != nil {
		_ = lock.Release()
	}
	if err != nil || lock != nil || owner == nil {
		t.Fatalf("target change released editing exclusion: %v", err)
	}
	if err := s.Start(1); err == nil || !strings.Contains(err.Error(), "対象ファイルがありません") {
		t.Fatalf("old tasks could start without a new scan: %v", err)
	}
	if _, err := s.SaveConfig(after.Config); err != nil {
		t.Fatalf("new root no longer has editing rights: %v", err)
	}
	again, err := s.ChangeTargetFolder(nextRoot)
	if err != nil || again.Config.QueuePath != after.Config.QueuePath {
		t.Fatal("selecting the same target discarded its output location")
	}
	assertWorkspaceTree(t, filepath.Dir(oldQueue), oldOutput)
	assertWorkspaceTree(t, before.Config.Root, oldSource)
	assertWorkspaceTree(t, nextRoot, nextSource)
	s.Close()
	reloaded := workspaceNewService(t, s.configPath).Snapshot()
	if reloaded.LastError != "" || reloaded.ReadOnly || reloaded.ActiveWorkspaceID != after.ActiveWorkspaceID || reloaded.Config.QueuePath != after.Config.QueuePath || !sameRoot(reloaded.Config.Root, nextRoot) || len(reloaded.Tasks) != 0 || reloaded.SelectedLLMConnectionID != after.SelectedLLMConnectionID {
		t.Fatalf("new target did not survive reopening: %s", reloaded.LastError)
	}
}

func TestTargetFolderChangeRequiresIdleEditableWorkspaceAndExplicitOperation(t *testing.T) {
	s, root := workspaceTestService(t)
	if _, err := s.ChangeTargetFolder(root); err == nil {
		t.Fatal("target changed before workspace creation")
	}
	initial, err := s.CreateWorkspace("active", root)
	if err != nil {
		t.Fatal(err)
	}
	nextRoot := t.TempDir()
	initTargetTestGit(t, nextRoot)
	c := initial.Config
	c.Root = nextRoot
	if _, err := s.SaveConfig(c); err == nil || !strings.Contains(err.Error(), "変更操作") {
		t.Fatalf("SaveConfig bypassed fresh-output handling: %v", err)
	}
	s.mu.Lock()
	s.state.Running = true
	s.mu.Unlock()
	_, err = s.ChangeTargetFolder(nextRoot)
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
	if err == nil || s.Snapshot().Config.Root != initial.Config.Root {
		t.Fatal("running workspace accepted a target change")
	}
	reader := workspaceNewService(t, s.configPath)
	if !reader.Snapshot().ReadOnly {
		t.Fatal("second service did not become a reader")
	}
	if _, err := reader.ChangeTargetFolder(nextRoot); err == nil || reader.Snapshot().Config.Root != initial.Config.Root {
		t.Fatal("reader changed the target")
	}
}

func TestSavedTargetIssuesAppearOnReloadAndClearAfterRepair(t *testing.T) {
	s, root := workspaceTestService(t)
	initial, err := s.CreateWorkspace("saved target", root)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := os.Rename(filepath.Join(root, ".git"), filepath.Join(root, "old-git-metadata")); err != nil {
		t.Fatal(err)
	}
	reloaded := workspaceNewService(t, s.configPath)
	issues := workspaceIssues(t, reloaded.Snapshot(), initial.ActiveWorkspaceID)
	if len(issues) != 1 || issues[0].ID != "target" || issues[0].Page != "target" || issues[0].Section != "target" || !strings.Contains(issues[0].Message, "Gitで管理") {
		t.Fatalf("saved non-Git target lacks field diagnostic: %#v", issues)
	}
	if err := os.Rename(filepath.Join(root, "old-git-metadata"), filepath.Join(root, ".git")); err != nil {
		t.Fatal(err)
	}
	reloaded.mu.Lock()
	reloaded.state.Running = true
	reloaded.mu.Unlock()
	polled, err := reloaded.GetState()
	reloaded.mu.Lock()
	reloaded.state.Running = false
	reloaded.mu.Unlock()
	if err != nil || len(workspaceIssues(t, polled, initial.ActiveWorkspaceID)) != 1 {
		t.Fatal("running polling refreshed target diagnostics")
	}
	repaired, err := reloaded.GetState()
	if err != nil || len(workspaceIssues(t, repaired, initial.ActiveWorkspaceID)) != 0 || repaired.LastError != "" {
		t.Fatalf("repaired Git target retained its issue: %v", err)
	}
}
