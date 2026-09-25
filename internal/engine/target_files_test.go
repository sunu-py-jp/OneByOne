package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func targetBrowserWorkspace(t *testing.T, s *Service, root string) model.State {
	t.Helper()
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "--allow-empty", "-qm", "browser fixture")
	state, err := s.CreateWorkspace("browser", root)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func targetBrowserNames(files []model.TargetFile) []string {
	names := make([]string, len(files))
	for i, file := range files {
		names[i] = file.File
	}
	return names
}

func TestTargetFilesBrowseOriginalDirtyRootWithoutRulesOrQueue(t *testing.T) {
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, "src", "nested", "alpha.js"), []byte("committed source\n"))
	writeTest(t, filepath.Join(root, "removed.txt"), []byte("removed later"))
	writeTest(t, filepath.Join(root, ".gitignore"), []byte("cache/\n*.log\n"))
	state := targetBrowserWorkspace(t, s, root)
	writeTest(t, filepath.Join(root, "src", "nested", "alpha.js"), []byte("original source, edited by user\n"))
	writeTest(t, filepath.Join(root, "new file.txt"), []byte("untracked"))
	writeTest(t, filepath.Join(root, "cache", "hidden.txt"), []byte("ignored"))
	writeTest(t, filepath.Join(root, "debug.log"), []byte("ignored"))
	if err := os.Remove(filepath.Join(root, "removed.txt")); err != nil {
		t.Fatal(err)
	}
	worktree := t.TempDir()
	writeTest(t, filepath.Join(worktree, "src", "nested", "alpha.js"), []byte("AI worktree must not be read"))
	s.mu.Lock()
	s.state.Worktree = worktree
	s.meta.Worktree = worktree
	s.state.Config.IncludeGlobs = []string{"*.not-a-source-extension"}
	s.state.Config.ExcludeGlobs = []string{"**"}
	s.state.Running = true // Reading is safe during execution and in read-only mode.
	s.state.ReadOnly = true
	s.mu.Unlock()
	before := workspaceTree(t, root)
	listed, err := s.ListTargetFiles(state.ActiveWorkspaceID, state.Config.Root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".gitignore", "new file.txt", "src/nested/alpha.js"}
	if !reflect.DeepEqual(targetBrowserNames(listed.Files), want) || listed.Truncated || listed.Limit != targetFileListLimit {
		t.Fatalf("unexpected original-source listing: %#v", listed)
	}
	content, err := s.ReadTargetFile(state.ActiveWorkspaceID, state.Config.Root, "src/nested/alpha.js")
	if err != nil || content.Content != "original source, edited by user\n" || content.UnavailableReason != "" {
		t.Fatalf("wrong source content: %#v / %v", content, err)
	}
	if content.WorkspaceID != listed.WorkspaceID || content.Root != listed.Root || content.Size != int64(len(content.Content)) {
		t.Fatalf("wrong source identity: %#v", content)
	}
	assertWorkspaceTree(t, root, before)
	if _, err := os.Stat(state.Config.QueuePath); !os.IsNotExist(err) {
		t.Fatalf("browsing created a queue: %v", err)
	}
	// Avoid leaving the fixture in a simulated running state for Close.
	s.mu.Lock()
	s.state.Running = false
	s.mu.Unlock()
}

func TestTargetFilesSelectedSubfolderHasRelativePathsAndInheritedIgnores(t *testing.T) {
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, ".gitignore"), []byte("*.generated\n"))
	writeTest(t, filepath.Join(root, "outside.txt"), []byte("outside selected folder"))
	writeTest(t, filepath.Join(root, "src", "nested", "file.ts"), []byte("inside"))
	gitTest(t, root, "add", ".")
	gitTest(t, root, "commit", "-qm", "subfolder fixtures")
	state, err := s.CreateWorkspace("subfolder", filepath.Join(root, "src"))
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(root, "src", "skip.generated"), []byte("ignored"))
	listed, err := s.ListTargetFiles(state.ActiveWorkspaceID, state.Config.Root)
	if err != nil || !reflect.DeepEqual(targetBrowserNames(listed.Files), []string{"nested/file.ts"}) {
		t.Fatalf("listing escaped selected subfolder: %#v / %v", listed, err)
	}
}

func TestTargetFilesRejectTraversalSymlinksAndWorkspaceMismatch(t *testing.T) {
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, "src", "file.txt"), []byte("safe"))
	state := targetBrowserWorkspace(t, s, root)
	outside := t.TempDir()
	writeTest(t, filepath.Join(outside, "secret.txt"), []byte("outside"))
	for _, link := range []struct{ name, target string }{
		{"outside.txt", filepath.Join(outside, "secret.txt")},
		{"inside.txt", filepath.Join(root, "src", "file.txt")},
		{"linked", outside},
	} {
		if err := os.Symlink(link.target, filepath.Join(root, link.name)); err != nil {
			t.Skipf("symbolic links unavailable: %v", err)
		}
	}
	listed, err := s.ListTargetFiles(state.ActiveWorkspaceID, state.Config.Root)
	if err != nil || !reflect.DeepEqual(targetBrowserNames(listed.Files), []string{"src/file.txt"}) {
		t.Fatalf("links appeared in listing: %#v / %v", listed, err)
	}
	for _, file := range []string{"../secret.txt", filepath.Join(outside, "secret.txt"), "src/../../secret.txt", "src\\..\\secret.txt", ".git/config", "src/../.git/config", "outside.txt", "inside.txt", "linked/secret.txt", "src", "src/./file.txt", ""} {
		if content, err := s.ReadTargetFile(state.ActiveWorkspaceID, state.Config.Root, file); err == nil || content.Content != "" {
			t.Errorf("unsafe file %q accepted: %#v / %v", file, content, err)
		}
	}
	for _, req := range []struct{ workspaceID, root string }{
		{"", state.Config.Root}, {"other-workspace", state.Config.Root},
		{state.ActiveWorkspaceID, ""}, {state.ActiveWorkspaceID, outside},
		{state.ActiveWorkspaceID, filepath.Join(root, "src")},
	} {
		if _, err := s.ListTargetFiles(req.workspaceID, req.root); err == nil {
			t.Errorf("mismatched list request accepted: %#v", req)
		}
		if _, err := s.ReadTargetFile(req.workspaceID, req.root, "src/file.txt"); err == nil {
			t.Errorf("mismatched read request accepted: %#v", req)
		}
	}
	s.mu.Lock()
	s.state.ActiveWorkspaceID = "different-workspace"
	s.mu.Unlock()
	if _, err := s.ReadTargetFile(state.ActiveWorkspaceID, state.Config.Root, "src/file.txt"); err == nil {
		t.Fatal("stale workspace request accepted")
	}
}

func TestTargetFilePreviewBoundsAndEncoding(t *testing.T) {
	s, root := workspaceTestService(t)
	fixtures := []struct {
		file            string
		data            []byte
		content, reason string
	}{
		{"empty.txt", nil, "", ""},
		{"text.txt", []byte("\xef\xbb\xbf日本語\r\nsecond\n"), "日本語\r\nsecond\n", ""},
		{"binary.dat", []byte{'x', 0, 'y'}, "", "バイナリ"},
		{"shiftjis.txt", []byte{0x82, 0xa0}, "", "UTF-8以外"},
		{"large.txt", bytes.Repeat([]byte{'a'}, targetFileContentLimit+1), "", "1 MiB"},
		{"limit.txt", bytes.Repeat([]byte{'b'}, targetFileContentLimit), strings.Repeat("b", targetFileContentLimit), ""},
	}
	for _, fixture := range fixtures {
		writeTest(t, filepath.Join(root, fixture.file), fixture.data)
	}
	state := targetBrowserWorkspace(t, s, root)
	for _, fixture := range fixtures {
		content, err := s.ReadTargetFile(state.ActiveWorkspaceID, state.Config.Root, fixture.file)
		if err != nil || content.Content != fixture.content || content.Size != int64(len(fixture.data)) {
			t.Errorf("%s: wrong preview (size=%d content length=%d): %v", fixture.file, content.Size, len(content.Content), err)
		}
		if fixture.reason == "" && content.UnavailableReason != "" || fixture.reason != "" && !strings.Contains(content.UnavailableReason, fixture.reason) {
			t.Errorf("%s: wrong unavailable reason %q", fixture.file, content.UnavailableReason)
		}
	}
}

func TestTargetFileListExplicitLimitAndNullDelimitedNames(t *testing.T) {
	s, root := workspaceTestService(t)
	for _, file := range []string{"c.txt", "a.txt", "nested/b.txt", "space name.txt"} {
		writeTest(t, filepath.Join(root, file), []byte(file))
	}
	state := targetBrowserWorkspace(t, s, root)
	files, truncated, err := listTargetFiles(state.Config.Root, 2)
	if err != nil || !truncated || !reflect.DeepEqual(targetBrowserNames(files), []string{"a.txt", "c.txt"}) {
		t.Fatalf("limit did not produce explicit partial listing: %#v %t %v", files, truncated, err)
	}
	files, truncated, err = listTargetFiles(state.Config.Root, 4)
	if err != nil || truncated || len(files) != 4 {
		t.Fatalf("exact limit incorrectly truncated: %#v %t %v", files, truncated, err)
	}
}

func TestTargetFileListMissingGitUsesInstallationDiagnostic(t *testing.T) {
	s, root := workspaceTestService(t)
	writeTest(t, filepath.Join(root, "file.txt"), []byte("preview remains read-only"))
	state := targetBrowserWorkspace(t, s, root)
	t.Setenv("PATH", t.TempDir())
	if _, err := s.ListTargetFiles(state.ActiveWorkspaceID, state.Config.Root); err == nil || !strings.Contains(err.Error(), "Gitが見つかりません") {
		t.Fatalf("missing Git did not use the installation diagnostic: %v", err)
	}
	content, err := s.ReadTargetFile(state.ActiveWorkspaceID, state.Config.Root, "file.txt")
	if err != nil || content.Content != "preview remains read-only" {
		t.Fatalf("reading an existing file unnecessarily depends on Git: %#v / %v", content, err)
	}
}
