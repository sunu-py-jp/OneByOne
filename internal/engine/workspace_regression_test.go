package engine

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func TestWorkspaceRestoreDoesNotExecuteConfiguredRGOverride(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this executable marker fixture uses a POSIX shell")
	}
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	t.Cleanup(s.Close)
	marker := filepath.Join(t.TempDir(), "shared-command-executed")
	executable := filepath.Join(t.TempDir(), "untrusted-rg")
	quotedMarker := "'" + strings.ReplaceAll(marker, "'", "'\"'\"'") + "'"
	writeTest(t, executable, []byte("#!/bin/sh\nprintf 'executed\\n' > "+quotedMarker+"\nexit 1\n"))
	if err := os.Chmod(executable, 0700); err != nil {
		t.Fatal(err)
	}
	c := s.Snapshot().Config
	c.RGPath = executable
	created, err := s.SaveConfig(c)
	if err != nil {
		t.Fatal(err)
	}
	s.Close()

	// Local workspaces restore on startup. Selecting one from another app
	// instance must follow the same passive restoration path.
	restored := workspaceNewService(t, s.configPath)
	assertPassiveRestore := func(st model.State) {
		t.Helper()
		if st.LastError != "" || st.ActiveWorkspaceID != created.ActiveWorkspaceID || len(st.Rules) != 2 || len(st.Tasks) != 1 {
			t.Fatalf("workspace did not actually restore its rules and queue: %s", st.LastError)
		}
		if st.Config.RGPath != executable {
			t.Fatal("restoration silently changed the saved executable preference")
		}
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatal("merely restoring a workspace executed its configured RG override")
		}
	}
	assertPassiveRestore(restored.Snapshot())
	restored.Close()
	fresh := workspaceNewService(t, s.configPath)
	opened, err := fresh.SelectWorkspace(created.ActiveWorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	assertPassiveRestore(opened)
}

func TestWorkspaceCloseRetainsLeaseUntilCancellationCleanupCompletes(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	t.Cleanup(s.Close)
	workspaceID := s.Snapshot().ActiveWorkspaceID
	started := make(chan struct{})
	cancelled := make(chan struct{})
	releaseCleanup := make(chan struct{})
	closed := make(chan struct{})
	var releaseOnce sync.Once
	// Release the deliberately stalled proposal before any registered service
	// cleanup runs, including when an assertion or timeout terminates this test.
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseCleanup) }) })
	s.propose = func(ctx context.Context, _ agent.Input) (model.Proposal, error) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-releaseCleanup
		return model.Proposal{}, ctx.Err()
	}
	if err := s.Start(1); err != nil {
		t.Fatal(err)
	}
	waitSignal := func(signal <-chan struct{}, message string) {
		t.Helper()
		select {
		case <-signal:
		case <-time.After(15 * time.Second):
			t.Fatal(message)
		}
	}
	waitSignal(started, "proposal did not start")
	go func() { s.Close(); close(closed) }()
	waitSignal(cancelled, "Close did not cancel the active proposal")
	select {
	case <-closed:
		t.Fatal("Close returned while proposal cancellation cleanup was still running")
	default:
	}

	other := workspaceNewService(t, s.configPath)
	opened, err := other.SelectWorkspace(workspaceID)
	if err != nil || !opened.ReadOnly || opened.WorkspaceLock == nil {
		t.Fatalf("another instance acquired the workspace during cancellation cleanup: %v", err)
	}
	if _, err := other.SaveConfig(opened.Config); err == nil {
		t.Fatal("another instance wrote settings before cancellation cleanup completed")
	}

	releaseOnce.Do(func() { close(releaseCleanup) })
	waitSignal(closed, "Close did not finish after proposal cleanup was released")
	selected, err := other.SelectWorkspace(workspaceID)
	if err != nil || selected.ReadOnly || selected.WorkspaceLock != nil {
		t.Fatalf("completed shutdown retained the workspace edit lock: %v", err)
	}
	if _, err := other.RenameWorkspace("new owner after cleanup"); err != nil {
		t.Fatalf("new owner could not edit after shutdown: %v", err)
	}
	if err := s.Start(1); err == nil {
		t.Fatal("a closed service accepted a new run")
	}
}
