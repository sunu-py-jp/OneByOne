//go:build !windows

package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWorkspacePermissionErrorsFailClosed(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root bypasses file permissions")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, ".edit.lock")
	if err := os.WriteFile(path, nil, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if lease, owner, err := AcquireWorkspace(path, WorkspaceOwner{}); err == nil || lease != nil || owner != nil {
		if lease != nil {
			_ = lease.Release()
		}
		t.Fatalf("unreadable lock should fail closed: %v %+v %v", lease, owner, err)
	}
	if owner, err := PeekWorkspace(path); err == nil || owner != nil {
		t.Fatalf("unreadable peek should fail closed: %+v %v", owner, err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0700)
	if owner, err := PeekWorkspace(path); err != nil || owner != nil {
		t.Fatalf("read-only folder should support peek: %+v %v", owner, err)
	}
	if lease, owner, err := AcquireWorkspace(path, WorkspaceOwner{}); err == nil || lease != nil || owner != nil {
		if lease != nil {
			_ = lease.Release()
		}
		t.Fatalf("unwritable owner metadata should fail closed: %v %+v %v", lease, owner, err)
	}
	if owner, err := PeekWorkspace(path); err != nil || owner != nil {
		t.Fatalf("failed metadata write retained the lease: %+v %v", owner, err)
	}
}
