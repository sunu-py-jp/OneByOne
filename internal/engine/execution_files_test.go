package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestExecutionFilePreviewsNextInputInsteadOfOriginalOrRejectedProposal(t *testing.T) {
	s, repository := workspaceTestService(t)
	original := "export const version = 'before';\n"
	accepted := "export const version = 'accepted';\n"
	writeTest(t, filepath.Join(repository, "nested", "app", "main.ts"), []byte(original))
	gitTest(t, repository, "add", ".")
	gitTest(t, repository, "commit", "-qm", "execution preview fixture")
	state, err := s.CreateWorkspace("execution preview", filepath.Join(repository, "nested", "app"))
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.state.Tasks = []model.Task{{File: "main.ts", Status: "pending"}}
	s.mu.Unlock()
	initial, err := s.ReadExecutionFile(state.ActiveWorkspaceID, state.Config.Root, "main.ts")
	if err != nil || initial.Content != original {
		t.Fatalf("initial input: %#v / %v", initial, err)
	}

	worktree := filepath.Join(t.TempDir(), "worktree")
	gitTest(t, repository, "worktree", "add", "--detach", worktree, "HEAD")
	writeTest(t, filepath.Join(worktree, "nested", "app", "main.ts"), []byte(accepted))
	gitTest(t, worktree, "add", ".")
	gitTest(t, worktree, "commit", "-qm", "accepted file change")
	s.mu.Lock()
	s.state.Worktree, s.meta.Worktree = worktree, worktree
	s.meta.SourceRelative = filepath.Join("nested", "app")
	s.state.ReadOnly = true
	// The latest rejected proposal is not the current file and must not be previewed.
	s.state.Tasks[0].History = []model.Attempt{{ID: "rejected", Number: 1, Outcome: "failed"}}
	s.mu.Unlock()
	writeTest(t, state.Config.QueuePath+".artifacts/rejected.after", []byte("rejected proposal\n"))
	beforeRoot, beforeWorktree := workspaceTree(t, repository), workspaceTree(t, worktree)
	content, err := s.ReadExecutionFile(state.ActiveWorkspaceID, state.Config.Root, "main.ts")
	if err != nil || content.Content != accepted || content.UnavailableReason != "" {
		t.Fatalf("execution did not use current accepted worktree: %#v / %v", content, err)
	}
	if content.Root != state.Config.Root || content.WorkspaceID != state.ActiveWorkspaceID || content.File != "main.ts" {
		t.Fatalf("execution response lost selected target identity: %#v", content)
	}
	source, err := s.ReadTargetFile(state.ActiveWorkspaceID, state.Config.Root, "main.ts")
	if err != nil || source.Content != original {
		t.Fatalf("original folder browsing changed: %#v / %v", source, err)
	}
	assertWorkspaceTree(t, repository, beforeRoot)
	assertWorkspaceTree(t, worktree, beforeWorktree)
	if _, err := os.Stat(state.Config.QueuePath); !os.IsNotExist(err) {
		t.Fatalf("preview created or changed queue: %v", err)
	}
}

func TestExecutionFileRejectsWorkspaceRootPathsAndWorktreeSubfolderEscapes(t *testing.T) {
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, "safe.ts"), []byte("safe"))
	state := targetBrowserWorkspace(t, s, root)
	worktree := t.TempDir()
	writeTest(t, filepath.Join(worktree, "src", "safe.ts"), []byte("worktree"))
	writeTest(t, filepath.Join(worktree, "not-in-queue.ts"), []byte("not in queue"))
	s.mu.Lock()
	s.state.Worktree, s.meta.Worktree = worktree, worktree
	s.meta.SourceRelative = "src"
	s.state.Tasks = []model.Task{{File: "safe.ts"}}
	s.mu.Unlock()
	for _, request := range []struct{ workspaceID, root, file string }{
		{"", state.Config.Root, "safe.ts"}, {"other", state.Config.Root, "safe.ts"},
		{state.ActiveWorkspaceID, "", "safe.ts"}, {state.ActiveWorkspaceID, worktree, "safe.ts"},
		{state.ActiveWorkspaceID, state.Config.Root, "../safe.ts"},
		{state.ActiveWorkspaceID, state.Config.Root, "src/../../safe.ts"},
		{state.ActiveWorkspaceID, state.Config.Root, "src\\..\\safe.ts"},
		{state.ActiveWorkspaceID, state.Config.Root, filepath.Join(worktree, "src", "safe.ts")},
		{state.ActiveWorkspaceID, state.Config.Root, ".git/config"},
		{state.ActiveWorkspaceID, state.Config.Root, "not-in-queue.ts"},
	} {
		content, err := s.ReadExecutionFile(request.workspaceID, request.root, request.file)
		if err == nil || content.Content != "" {
			t.Errorf("unsafe execution request accepted: %#v / %#v / %v", request, content, err)
		}
	}
	for _, relative := range []string{"../", "../outside", filepath.Dir(worktree), "src/../src", ".git"} {
		s.mu.Lock()
		s.meta.SourceRelative = relative
		s.mu.Unlock()
		if content, err := s.ReadExecutionFile(state.ActiveWorkspaceID, state.Config.Root, "safe.ts"); err == nil || content.Content != "" {
			t.Errorf("unsafe worktree subfolder %q accepted: %#v / %v", relative, content, err)
		}
	}
	s.mu.Lock()
	s.meta.SourceRelative = "src"
	s.state.ActiveWorkspaceID = "changed"
	s.mu.Unlock()
	if _, err := s.ReadExecutionFile(state.ActiveWorkspaceID, state.Config.Root, "safe.ts"); err == nil {
		t.Fatal("stale workspace request accepted")
	}
	s.mu.Lock()
	s.state.ActiveWorkspaceID = state.ActiveWorkspaceID
	s.state.Config.Root = worktree
	s.mu.Unlock()
	if _, err := s.ReadExecutionFile(state.ActiveWorkspaceID, state.Config.Root, "safe.ts"); err == nil {
		t.Fatal("stale target request accepted")
	}
}

func TestExecutionFileRejectsLinkedWorktreeSubfolder(t *testing.T) {
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, "safe.ts"), []byte("original"))
	state := targetBrowserWorkspace(t, s, root)
	worktree, outside := t.TempDir(), t.TempDir()
	writeTest(t, filepath.Join(outside, "safe.ts"), []byte("outside selected worktree"))
	if err := os.Symlink(outside, filepath.Join(worktree, "linked")); err != nil {
		t.Skipf("symbolic links unavailable: %v", err)
	}
	s.mu.Lock()
	s.state.Worktree, s.meta.Worktree = worktree, worktree
	s.meta.SourceRelative = "linked"
	s.state.Tasks = []model.Task{{File: "safe.ts"}}
	s.mu.Unlock()
	content, err := s.ReadExecutionFile(state.ActiveWorkspaceID, state.Config.Root, "safe.ts")
	if err == nil || content.Content != "" || !strings.Contains(err.Error(), "シンボリックリンク") {
		t.Fatalf("worktree subfolder followed a symlink: %#v / %v", content, err)
	}
}
