package engine

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"onebyone/internal/store"
)

// Include source directory timestamps as well as bytes, modes, and symlinks:
// even an empty directory created and removed by the application is a mutation.
func workspaceTree(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			// Windows directory enumeration can retain the parent's cached
			// timestamp for this entry after its children changed. Walking
			// the directory then refreshes that cache, making a read-only
			// operation appear to have changed mtime in the next snapshot.
			// Query the directory handle for its current metadata instead.
			directory, err := os.Open(path)
			if err != nil {
				return err
			}
			info, err = directory.Stat()
			closeErr := directory.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fingerprint := fmt.Sprintf("mode=%v size=%d mtime=%s", info.Mode(), info.Size(), info.ModTime().Format("2006-01-02T15:04:05.999999999Z07:00"))
		if !entry.IsDir() {
			var data []byte
			var err error
			if entry.Type()&os.ModeSymlink != 0 {
				var destination string
				destination, err = os.Readlink(path)
				data = []byte(destination)
			} else if info.Mode().IsRegular() && info.Size() == 0 {
				// Windows byte-range locks also reject ReadFile on an empty
				// .edit.lock. Its recorded size already proves empty content;
				// do not unlock it or weaken checks for nonempty files.
				data = nil
			} else {
				data, err = os.ReadFile(path)
			}
			if err != nil {
				return err
			}
			digest := sha256.Sum256(data)
			fingerprint += " sha256=" + hex.EncodeToString(digest[:])
		}
		result[relative] = fingerprint
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func assertWorkspaceTree(t *testing.T, root string, before map[string]string) {
	t.Helper()
	after := workspaceTree(t, root)
	paths := make([]string, 0, len(before)+len(after))
	for path := range before {
		paths = append(paths, path)
	}
	for path := range after {
		if _, existed := before[path]; !existed {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	var differences []string
	for _, path := range paths {
		old, existed := before[path]
		current, exists := after[path]
		switch {
		case !existed:
			differences = append(differences, fmt.Sprintf("added %q: %s", path, current))
		case !exists:
			differences = append(differences, fmt.Sprintf("removed %q: %s", path, old))
		case old != current:
			differences = append(differences, fmt.Sprintf("changed %q:\n  before: %s\n  after:  %s", path, old, current))
		}
	}
	if len(differences) != 0 {
		t.Fatalf("operation changed files, permissions, sizes, or timestamps under %q:\n%s", root, strings.Join(differences, "\n"))
	}
}

func TestWorkspaceTreeSnapshotImmediatelyAfterCreatingEntriesIsStable(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	// Make the entry's old mtime unambiguously different from the child
	// creation time without sleeping or reducing timestamp precision.
	old := time.Unix(1700000000, 0)
	if err := os.Chtimes(nested, old, old); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(nested, "source.txt"), []byte("unchanged source\n"))
	before := workspaceTree(t, root)
	assertWorkspaceTree(t, root, before)
}

func TestWorkspaceSameApplicationPeerIsReadOnlyAndCannotWrite(t *testing.T) {
	first, root := workspaceTestService(t)
	sourceBefore := workspaceTree(t, root)
	created, err := first.CreateWorkspace("local workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	localDir := filepath.Dir(workspaceSettingPath(t, first, created.ActiveWorkspaceID))
	localBefore := workspaceTree(t, localDir)
	second := workspaceNewService(t, first.configPath)
	if initial := second.Snapshot(); len(initial.Workspaces) != 1 || !initial.ReadOnly || initial.WorkspaceLock == nil || initial.WorkspaceLock.Owner == "" {
		t.Fatal("startup did not restore the local workspace and its active owner")
	}
	opened, err := second.SelectWorkspace(created.ActiveWorkspaceID)
	if err != nil || !opened.ReadOnly || opened.WorkspaceLock == nil || opened.WorkspaceLock.Owner == "" {
		t.Fatalf("second process did not select read-only: %v", err)
	}
	assertWorkspaceTree(t, localDir, localBefore)
	operations := map[string]func() error{
		"save":       func() error { _, err := second.SaveConfig(opened.Config); return err },
		"rename":     func() error { _, err := second.RenameWorkspace("must not rename"); return err },
		"duplicate":  func() error { _, err := second.DuplicateWorkspace("must not create"); return err },
		"scan":       func() error { _, err := second.Scan(); return err },
		"start":      func() error { return second.Start(1) },
		"retry":      func() error { _, err := second.RetryTasks([]string{"A.txt"}); return err },
		"load queue": func() error { _, err := second.LoadQueue(created.Config.QueuePath); return err },
		"import package": func() error {
			_, err := second.ImportRulePackage(filepath.Join(t.TempDir(), "missing.oborules"), "replace")
			return err
		},
		"export report": func() error { _, err := second.ExportReport(""); return err },
	}
	for name, operation := range operations {
		t.Run(name, func(t *testing.T) {
			if err := operation(); err == nil || !strings.Contains(err.Error(), "閲覧専用") {
				t.Fatalf("operation was not blocked by the workspace lease: %v", err)
			}
			assertWorkspaceTree(t, localDir, localBefore)
			assertWorkspaceTree(t, root, sourceBefore)
		})
	}
	if got := second.Snapshot(); got.ActiveWorkspaceID != created.ActiveWorkspaceID || !got.ReadOnly || !sameRoot(got.Config.Root, root) {
		t.Fatal("blocked operation changed selection")
	}
}

func TestWorkspaceReleaseOrSwitchAllowsLaterLocalEditor(t *testing.T) {
	for _, release := range []string{"close", "switch"} {
		t.Run(release, func(t *testing.T) {
			first, root := workspaceTestService(t)
			before := workspaceTree(t, root)
			created, err := first.CreateWorkspace("first", root)
			if err != nil {
				t.Fatal(err)
			}
			second := workspaceNewService(t, first.configPath)
			if !second.Snapshot().ReadOnly {
				t.Fatal("second process did not detect the existing lease")
			}
			if release == "close" {
				first.Close()
			} else if _, err := first.CreateWorkspace("other", root); err != nil {
				t.Fatal(err)
			}
			reopened, err := second.SelectWorkspace(created.ActiveWorkspaceID)
			if err != nil || reopened.ReadOnly || reopened.WorkspaceLock != nil {
				t.Fatalf("released local workspace was not reacquired: %v", err)
			}
			if _, err := second.RenameWorkspace("new owner"); err != nil {
				t.Fatal(err)
			}
			if release == "switch" {
				got, err := first.SelectWorkspace(created.ActiveWorkspaceID)
				if err != nil || !got.ReadOnly {
					t.Fatal("former owner ignored the new editor's lease")
				}
			}
			assertWorkspaceTree(t, root, before)
		})
	}
}

func TestWorkspaceReadonlyPeerCanClearPersonalCredentialWithoutWorkspaceWrites(t *testing.T) {
	first, root := workspaceTestService(t)
	created, err := first.CreateWorkspace("workspace", root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := created.Config
	cfg.Endpoint, cfg.Deployment, cfg.Credential = "https://private.example.invalid", "model", "FAKE-key-to-clear"
	selected := workspaceSelectTestConnection(t, first, "personal connection", cfg)
	second := workspaceNewService(t, first.configPath)
	opened := second.Snapshot()
	if !opened.ReadOnly || !opened.Config.CredentialSet {
		t.Fatal("peer did not load the selected personal connection")
	}
	localDir := filepath.Dir(workspaceSettingPath(t, first, created.ActiveWorkspaceID))
	localBefore, sourceBefore := workspaceTree(t, localDir), workspaceTree(t, root)
	cleared, err := second.ClearLLMConnectionCredential(selected.SelectedLLMConnectionID)
	if err != nil || cleared.Config.CredentialSet || !cleared.ReadOnly || cleared.Config.Endpoint != cfg.Endpoint {
		t.Fatalf("personal credential clearing failed: %v", err)
	}
	assertWorkspaceTree(t, localDir, localBefore)
	assertWorkspaceTree(t, root, sourceBefore)
	second.Close()
	reloaded := workspaceNewService(t, first.configPath)
	if got := reloaded.Snapshot(); got.LastError != "" || got.Config.CredentialSet || !got.ReadOnly || got.Config.Endpoint != cfg.Endpoint {
		t.Fatalf("personal deletion did not persist: %s", got.LastError)
	}
}

func TestWorkspaceFailedApplicationSelectionWriteKeepsOldLease(t *testing.T) {
	s, root := workspaceTestService(t)
	first, err := s.CreateWorkspace("first", root)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.CreateWorkspace("second", root)
	if err != nil {
		t.Fatal(err)
	}
	firstLock, err := s.workspaceLockPath(first.ActiveWorkspaceID, ".edit.lock")
	if err != nil {
		t.Fatal(err)
	}
	appSettings := s.configPath
	original := readTest(t, appSettings)
	target := filepath.Join(t.TempDir(), "do-not-touch.json")
	writeTest(t, target, []byte(original))
	if err := os.Remove(appSettings); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, appSettings); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	selected, err := s.SelectWorkspace(first.ActiveWorkspaceID)
	if err == nil || selected.ActiveWorkspaceID != second.ActiveWorkspaceID || s.Snapshot().ActiveWorkspaceID != second.ActiveWorkspaceID || s.leaseID != second.ActiveWorkspaceID || s.workspaceLease == nil {
		t.Fatal("failed application selection write changed state or released prior lease")
	}
	if got := readTest(t, target); got != original {
		t.Fatal("application settings symlink target was changed")
	}
	probe, owner, err := store.AcquireWorkspace(firstLock, store.WorkspaceOwner{Owner: "probe", Host: "test", OpenedAt: now()})
	if err != nil || probe == nil || owner != nil {
		t.Fatalf("failed selection leaked a prospective lease: %v", err)
	}
	if err := probe.Release(); err != nil {
		t.Fatal(err)
	}
}
