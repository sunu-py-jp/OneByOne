package store

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWorkspaceLeaseLiveOwnerAndIdempotentRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".edit.lock")
	want := WorkspaceOwner{Owner: "first editor", Host: "host-a", OpenedAt: "2026-09-12T00:00:00Z"}
	lease, busy, err := AcquireWorkspace(path, want)
	if err != nil || lease == nil || busy != nil {
		t.Fatalf("first acquire: %v %+v %v", lease, busy, err)
	}
	defer lease.Release()
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if owner, err := PeekWorkspace(path); err != nil || owner == nil || *owner != want {
		t.Fatalf("peek live holder: %+v %v", owner, err)
	}
	if other, owner, err := AcquireWorkspace(path, WorkspaceOwner{Owner: "second editor"}); err != nil || other != nil || owner == nil || *owner != want {
		if other != nil {
			_ = other.Release()
		}
		t.Fatalf("second acquire: %v %+v %v", other, owner, err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := lease.Release(); err != nil {
				t.Errorf("release: %v", err)
			}
		})
	}
	wg.Wait()
	if owner, err := PeekWorkspace(path); err != nil || owner != nil {
		t.Fatalf("released holder appears busy: %+v %v", owner, err)
	}
	after, err := os.Stat(path)
	if err != nil || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) {
		t.Fatalf("lock inode was removed, replaced or modified: %v", err)
	}
	newLease, busy, err := AcquireWorkspace(path, WorkspaceOwner{Owner: "second editor"})
	if err != nil || newLease == nil || busy != nil {
		t.Fatalf("acquire after release: %v %+v %v", newLease, busy, err)
	}
	_ = newLease.Release()
}

func TestWorkspaceLeaseCrossProcessAndProcessDeath(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".edit.lock")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	child := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestWorkspaceLeaseOwnerProcess$", "--", "onebyone-workspace-owner", path)
	stdin, err := child.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := child.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	child.Stderr = &stderr
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if child.ProcessState == nil {
			_ = child.Process.Kill()
			_ = child.Wait()
		}
	}()
	ready, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || ready != "ready\n" {
		t.Fatalf("child not ready: %q %v", ready, err)
	}
	for _, check := range []func() (*WorkspaceOwner, error){
		func() (*WorkspaceOwner, error) { return PeekWorkspace(path) },
		func() (*WorkspaceOwner, error) {
			lease, owner, err := AcquireWorkspace(path, WorkspaceOwner{Owner: "parent"})
			if lease != nil {
				_ = lease.Release()
				t.Error("parent acquired a live child's lock")
			}
			return owner, err
		},
	} {
		owner, err := check()
		if err != nil || owner == nil || owner.Owner != "child editor" || owner.Host != "child-host" {
			t.Fatalf("cross-process owner: %+v %v", owner, err)
		}
	}
	if err = child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	if owner, err := PeekWorkspace(path); err != nil || owner != nil {
		t.Fatalf("dead process retained ownership: %+v %v", owner, err)
	}
	lease, busy, err := AcquireWorkspace(path, WorkspaceOwner{Owner: "parent"})
	if err != nil || lease == nil || busy != nil {
		t.Fatalf("acquire after child died: %v %+v %v", lease, busy, err)
	}
	_ = lease.Release()
}

func TestWorkspaceLeaseOwnerProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "onebyone-workspace-owner" || i+1 >= len(os.Args) {
			continue
		}
		lease, busy, err := AcquireWorkspace(os.Args[i+1], WorkspaceOwner{Owner: "child editor", Host: "child-host"})
		if err != nil || lease == nil || busy != nil {
			fmt.Fprintf(os.Stderr, "child acquire: %+v %v\n", busy, err)
			os.Exit(2)
		}
		fmt.Fprintln(os.Stdout, "ready")
		_, _ = io.Copy(io.Discard, os.Stdin)
		// Deliberately exit without Release: the kernel owns crash recovery.
		os.Exit(0)
	}
}

func TestWorkspacePeekDoesNotWriteOrTrustStaleMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".edit.lock")
	if owner, err := PeekWorkspace(path); err != nil || owner != nil {
		t.Fatalf("empty workspace: %+v %v", owner, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("peek created files: %v", entries)
	}
	if err := os.WriteFile(path, []byte("do not truncate me"), 0444); err != nil {
		t.Fatal(err)
	}
	metadata := []byte(`{"owner":"dead editor","host":"old host"}`)
	if err := os.WriteFile(path+".owner.json", metadata, 0444); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	ownerBefore, _ := os.Stat(path + ".owner.json")
	for range 3 {
		if owner, err := PeekWorkspace(path); err != nil || owner != nil {
			t.Fatalf("stale metadata claimed ownership: %+v %v", owner, err)
		}
	}
	contents, _ := os.ReadFile(path)
	after, _ := os.Stat(path)
	ownerContents, _ := os.ReadFile(path + ".owner.json")
	ownerAfter, _ := os.Stat(path + ".owner.json")
	if string(contents) != "do not truncate me" || !os.SameFile(before, after) || !before.ModTime().Equal(after.ModTime()) || before.Mode() != after.Mode() {
		t.Fatal("peek changed the lock file")
	}
	if string(ownerContents) != string(metadata) || !os.SameFile(ownerBefore, ownerAfter) || !ownerBefore.ModTime().Equal(ownerAfter.ModTime()) || ownerBefore.Mode() != ownerAfter.Mode() {
		t.Fatal("peek changed owner metadata")
	}
}

func TestWorkspaceBusyOwnerMetadataIsBoundedAndBestEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".edit.lock")
	lease, _, err := AcquireWorkspace(path, WorkspaceOwner{Owner: "editor"})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	for _, data := range []string{"", "invalid-json", strings.Repeat("x", workspaceOwnerLimit+1)} {
		if err := os.WriteFile(path+".owner.json", []byte(data), 0644); err != nil {
			t.Fatal(err)
		}
		if owner, err := PeekWorkspace(path); err != nil || owner == nil || *owner != (WorkspaceOwner{}) {
			t.Fatalf("bad metadata lost busy status: %+v %v", owner, err)
		}
	}
	if err := os.Remove(path + ".owner.json"); err != nil {
		t.Fatal(err)
	}
	if other, owner, err := AcquireWorkspace(path, WorkspaceOwner{}); err != nil || other != nil || owner == nil {
		if other != nil {
			_ = other.Release()
		}
		t.Fatalf("missing metadata lost busy status: %v %+v %v", other, owner, err)
	}
}

func TestWorkspaceLockRejectsUnsafePaths(t *testing.T) {
	for _, target := range []string{"lock directory", "owner directory", "lock symlink", "owner symlink", "parent symlink", "parent traversal"} {
		t.Run(target, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, ".edit.lock")
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			switch target {
			case "lock directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "owner directory":
				if err := os.Mkdir(path+".owner.json", 0700); err != nil {
					t.Fatal(err)
				}
			case "lock symlink", "owner symlink":
				link := path
				if target == "owner symlink" {
					link += ".owner.json"
				}
				if err := os.Symlink(outside, link); err != nil {
					if runtime.GOOS == "windows" {
						t.Skip("symlink privilege unavailable")
					}
					t.Fatal(err)
				}
			case "parent symlink":
				link := filepath.Join(dir, "parent-link")
				if err := os.Symlink(filepath.Dir(outside), link); err != nil {
					if runtime.GOOS == "windows" {
						t.Skip("symlink privilege unavailable")
					}
					t.Fatal(err)
				}
				path = filepath.Join(link, ".edit.lock")
			case "parent traversal":
				path = dir + string(filepath.Separator) + "unused" + string(filepath.Separator) + ".." + string(filepath.Separator) + ".edit.lock"
			}
			if lease, busy, err := AcquireWorkspace(path, WorkspaceOwner{}); err == nil || lease != nil || busy != nil {
				if lease != nil {
					_ = lease.Release()
				}
				t.Fatalf("unsafe acquire accepted: %v %+v %v", lease, busy, err)
			}
			if owner, err := PeekWorkspace(path); err == nil || owner != nil {
				t.Fatalf("unsafe peek accepted: %+v %v", owner, err)
			}
			contents, err := os.ReadFile(outside)
			if err != nil || string(contents) != "untouched" {
				t.Fatalf("outside file changed: %q %v", contents, err)
			}
		})
	}
}

func TestWorkspaceMetadataPublicationFailureReleasesLease(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".edit.lock")
	if lease, busy, err := AcquireWorkspace(path, WorkspaceOwner{Owner: strings.Repeat("x", workspaceOwnerLimit)}); err == nil || lease != nil || busy != nil {
		if lease != nil {
			_ = lease.Release()
		}
		t.Fatalf("oversized metadata accepted: %v %+v %v", lease, busy, err)
	}
	lease, busy, err := AcquireWorkspace(path, WorkspaceOwner{Owner: "valid editor"})
	if err != nil || lease == nil || busy != nil {
		t.Fatalf("failed acquisition retained lease: %v %+v %v", lease, busy, err)
	}
	defer lease.Release()
	data, err := os.ReadFile(path + ".owner.json")
	var owner WorkspaceOwner
	if err != nil || json.Unmarshal(data, &owner) != nil || owner.Owner != "valid editor" || owner.OpenedAt == "" {
		t.Fatalf("invalid published owner: %q %v", data, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(path))
	if len(entries) != 2 {
		t.Fatalf("temporary file leak: %v", entries)
	}
}

func TestWorkspaceErrorsAreNotMistakenForBusy(t *testing.T) {
	for _, err := range []error{os.ErrPermission, os.ErrNotExist, errors.New("filesystem unavailable")} {
		if workspaceLockBusy(err) || workspaceLockBusy(fmt.Errorf("wrapped: %w", err)) {
			t.Fatalf("non-contention error was classified as another editor: %v", err)
		}
	}
	for _, path := range []string{"", filepath.Join(t.TempDir(), "missing-parent", ".edit.lock")} {
		if lease, owner, err := AcquireWorkspace(path, WorkspaceOwner{}); err == nil || lease != nil || owner != nil {
			if lease != nil {
				_ = lease.Release()
			}
			t.Fatalf("invalid path should fail closed: %v %+v %v", lease, owner, err)
		}
	}
}
