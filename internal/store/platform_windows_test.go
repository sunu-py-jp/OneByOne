//go:build windows

package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestSnapshotReaderAllowsReplacementAndRetainsOldContents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := WriteFile(path, []byte("old snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := openSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if err := WriteFile(path, []byte("new snapshot"), 0600); err != nil {
		t.Fatal("live snapshot reader blocked replacement", err)
	}
	old, err := io.ReadAll(reader)
	if err != nil || string(old) != "old snapshot" {
		t.Fatalf("existing reader lost its snapshot: %q %v", old, err)
	}
	current, err := ReadFile(path)
	if err != nil || string(current) != "new snapshot" {
		t.Fatalf("new reader did not see replacement: %q %v", current, err)
	}
}

func TestSnapshotReplacementWaitsForTransientExternalReader(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := WriteFile(path, []byte("old snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	// An ordinary Windows file handle does not share delete access, matching
	// an external editor/indexer that briefly prevents atomic replacement.
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	done := make(chan error, 1)
	go func() { done <- WriteFile(path, []byte("new snapshot"), 0600) }()
	select {
	case err := <-done:
		t.Fatalf("publication returned before reader released: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal("transient reader was not retried", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("snapshot publication did not finish")
	}
	current, err := ReadFile(path)
	if err != nil || string(current) != "new snapshot" {
		t.Fatalf("wrong published snapshot: %q %v", current, err)
	}
}

func TestSnapshotPersistentSharingFailurePreservesDestination(t *testing.T) {
	path := filepath.Join(t.TempDir(), "snapshot.json")
	if err := WriteFile(path, []byte("existing snapshot"), 0600); err != nil {
		t.Fatal(err)
	}
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	err = WriteFile(path, []byte("must not publish"), 0600)
	if !errors.Is(err, syscall.ERROR_ACCESS_DENIED) && !errors.Is(err, syscall.Errno(32)) {
		t.Fatalf("persistent sharing conflict should be reported: %v", err)
	}
	current, err := ReadFile(path)
	if err != nil || string(current) != "existing snapshot" {
		t.Fatalf("failed publication damaged old snapshot: %q %v", current, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(filepath.Dir(path), ".onebyone-write-*"))
	if err != nil || len(leftovers) != 0 {
		t.Fatalf("failed publication leaked temporary snapshot: %v %v", leftovers, err)
	}
}

func TestSnapshotLongPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), strings.Repeat("a", 120), strings.Repeat("b", 120), "snapshot.json")
	for _, value := range []string{"first", "replacement"} {
		if err := WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		data, err := ReadFile(path)
		if err != nil || string(data) != value {
			t.Fatalf("long-path snapshot: %q %v", data, err)
		}
	}
}
