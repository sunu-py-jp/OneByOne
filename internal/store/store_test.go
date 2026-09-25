package store

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"onebyone/internal/model"
)

func TestQueueRoundTripAndMalformedTail(t *testing.T) {
	file := filepath.Join(t.TempDir(), "session", "queue.jsonl")
	tasks := []model.Task{{File: "src/A.txt", Rules: []string{"R019"}, Status: "done", Attempts: 1, History: []model.Attempt{{ID: "id", Outcome: "done", Note: "日本語\nmultiple lines", Usage: model.Usage{InputTokens: 12}, Commit: "abc"}}}, {File: "src/B.txt", Status: "pending"}}
	if err := SaveQueue(file, tasks); err != nil {
		t.Fatal(err)
	}
	got, err := LoadQueue(file)
	if err != nil || len(got) != 2 || got[0].History[0].Note != tasks[0].History[0].Note || got[0].History[0].Usage.InputTokens != 12 {
		t.Fatalf("roundtrip: %+v, %v", got, err)
	}
	if got[1].Rules == nil || got[1].History == nil || got[1].RulesApplied == nil {
		t.Error("optional arrays not normalized")
	}
	f, err := os.OpenFile(file, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = f.WriteString(`{"file":"truncated`)
	_ = f.Close()
	if partial, err := LoadQueue(file); err == nil || partial != nil {
		t.Fatalf("malformed final row accepted as partial success: %+v, %v", partial, err)
	}
}

func TestQueueRejectsInvalidIdentityAndState(t *testing.T) {
	for name, data := range map[string]string{
		"duplicate":         "{\"file\":\"src/A.txt\"}\n{\"file\":\"src/a.txt\"}\n",
		"empty name":        `{"file":""}`,
		"unknown state":     `{"file":"A.txt","status":"success"}`,
		"negative attempts": `{"file":"A.txt","attempts":-1}`,
		"invalid JSON":      `not json`,
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "queue.jsonl")
			if err := os.WriteFile(file, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadQueue(file); err == nil {
				t.Fatalf("accepted %q", data)
			}
		})
	}
	file := filepath.Join(t.TempDir(), "queue.jsonl")
	if err := os.WriteFile(file, []byte("\ufeff{\"file\":\"A.txt\"}\r\n\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if q, err := LoadQueue(file); err != nil || len(q) != 1 || q[0].Status != "pending" {
		t.Fatalf("BOM/CRLF queue rejected: %+v %v", q, err)
	}
}

func TestAtomicSnapshotReadersNeverSeePartialWrites(t *testing.T) {
	t.Run("snapshot readers", func(t *testing.T) { checkConcurrentSnapshotReaders(t, ReadFile) })
	t.Run("ordinary readers", checkOrdinarySnapshotReaderBursts)
}

func checkOrdinarySnapshotReaderBursts(t *testing.T) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "snapshot")
	a, b := bytes.Repeat([]byte("a"), 200000), bytes.Repeat([]byte("b"), 300000)
	if err := WriteFile(file, a, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		// All four ordinary readers have live handles before publication starts.
		// Their finite burst is followed by a quiet window until publication
		// completes. Continuously reopening non-delete-sharing Windows handles
		// can legitimately prevent any bounded writer from making progress.
		readers := make([]*os.File, 0, 4)
		for range 4 {
			reader, err := os.Open(file)
			if err != nil {
				for _, opened := range readers {
					_ = opened.Close()
				}
				t.Fatal(err)
			}
			readers = append(readers, reader)
		}
		start := make(chan struct{})
		var wg sync.WaitGroup
		for _, reader := range readers {
			wg.Go(func() {
				defer reader.Close()
				<-start
				data, err := io.ReadAll(reader)
				if err != nil {
					t.Errorf("snapshot read failed: %v", err)
				} else if !bytes.Equal(data, a) && !bytes.Equal(data, b) {
					t.Errorf("ordinary reader saw partial snapshot (%d bytes)", len(data))
				}
			})
		}
		data := a
		if i%2 == 0 {
			data = b
		}
		published := make(chan error, 1)
		go func() { published <- WriteFile(file, data, 0600) }()
		close(start)
		wg.Wait()
		if err := <-published; err != nil {
			t.Fatal("publication failed after ordinary readers closed", err)
		}
		current, err := os.ReadFile(file)
		if err != nil || !bytes.Equal(current, data) {
			t.Fatalf("new reader did not see full published snapshot: %d bytes %v", len(current), err)
		}
	}
}

func checkConcurrentSnapshotReaders(t *testing.T, read func(string) ([]byte, error)) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "snapshot")
	a, b := bytes.Repeat([]byte("a"), 200000), bytes.Repeat([]byte("b"), 300000)
	if err := WriteFile(file, a, 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-done:
					return
				default:
				}
				data, err := read(file)
				if err != nil {
					t.Errorf("snapshot disappeared: %v", err)
					return
				}
				if !bytes.Equal(data, a) && !bytes.Equal(data, b) {
					t.Errorf("reader saw partial snapshot (%d bytes)", len(data))
					return
				}
			}
		}()
	}
	for i := 0; i < 20; i++ {
		data := a
		if i%2 == 0 {
			data = b
		}
		if err := WriteFile(file, data, 0600); err != nil {
			t.Error(err)
			break
		}
	}
	close(done)
	wg.Wait()
	files, err := filepath.Glob(filepath.Join(filepath.Dir(file), ".onebyone-write-*"))
	if err != nil || len(files) != 0 {
		t.Errorf("temporary snapshot leak: %v, %v", files, err)
	}
}

func TestLockRejectsLiveWriterAndReleases(t *testing.T) {
	file := filepath.Join(t.TempDir(), "queue.lock")
	unlock, err := Lock(file)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := Lock(file); err == nil {
		second()
		t.Error("two writers obtained same queue lock")
	}
	unlock()
	third, err := Lock(file)
	if err != nil {
		t.Fatal("lock not released", err)
	}
	third()
}

func TestLockRecoversAfterOwningProcessExit(t *testing.T) {
	file := filepath.Join(t.TempDir(), "queue.lock")
	// The child acquires the real OS lock and exits without calling unlock.
	child := exec.Command(os.Args[0], "-test.run=^TestLockOwnerProcess$", "--", "onebyone-lock-owner", file)
	if output, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child lock owner: %s %v", output, err)
	}
	unlock, err := Lock(file)
	if err != nil {
		t.Fatal("dead writer was not recovered", err)
	}
	// Windows locks exclude even another handle in the owning process from
	// reading the locked range. Release before checking diagnostic metadata.
	unlock()
	data, err := os.ReadFile(file)
	if err != nil || !strings.HasPrefix(string(data), fmt.Sprintf("%d\n", os.Getpid())) {
		t.Fatalf("wrong lock owner: %q, %v", data, err)
	}
	// Ownership belongs to the OS lock, not the contents of stale metadata.
	for _, invalid := range []string{"", "not-a-pid\n", "-1\n"} {
		if err := os.WriteFile(file, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		unlock, err := Lock(file)
		if err != nil {
			t.Errorf("unlocked stale metadata prevented recovery: %v", err)
		} else {
			unlock()
		}
	}
}

func TestLockOwnerProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "onebyone-lock-owner" || i+1 >= len(os.Args) {
			continue
		}
		if _, err := Lock(os.Args[i+1]); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func TestQueueRejectsArtifactPathTraversalAndDuplicateAttemptIDs(t *testing.T) {
	for name, data := range map[string]string{
		"parent traversal": `{"file":"A.txt","history":[{"id":"../../secret"}]}`,
		"absolute path":    `{"file":"A.txt","history":[{"id":"/tmp/secret"}]}`,
		"Windows alias":    `{"file":"A.txt","history":[{"id":"attempt. "}]}`,
		"duplicate IDs":    `{"file":"A.txt","history":[{"id":"same"},{"id":"same"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "queue.jsonl")
			if err := os.WriteFile(file, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			if tasks, err := LoadQueue(file); err == nil {
				t.Fatalf("unsafe history accepted: %+v", tasks)
			}
		})
	}
}

func TestFailedSnapshotPublicationPreservesExistingDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "existing-directory")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(path, []byte("not a directory"), 0600); err == nil {
		t.Error("replaced directory with result")
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("existing directory damaged: %v", err)
	}
}
