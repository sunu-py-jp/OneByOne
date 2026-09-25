package privateconfig

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

func TestConnectionRegistryConcurrentProcessesPreserveAllChanges(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	const workers = 6
	commands := make([]*exec.Cmd, workers)
	outputs := make([]bytes.Buffer, workers)
	for i := range workers {
		commands[i] = exec.Command(binary, "-test.run=^TestConnectionRegistryProcessHelper$")
		commands[i].Env = append(os.Environ(), "ONEBYONE_CONNECTION_REGISTRY_TEST_DIR="+dir, "ONEBYONE_CONNECTION_REGISTRY_TEST_ID="+fmt.Sprint(i))
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("process %d failed: %v: %s", i, err, outputs[i].String())
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.LoadConnections()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Connections) != workers*8 {
		t.Fatal("a concurrent transaction overwrote another process's entries")
	}
	for _, connection := range r.Connections {
		if !reflect.DeepEqual(connection.LLMSettings, fakeSettings()) {
			t.Fatal("connection transaction was not preserved")
		}
	}
	encoded, err := os.ReadFile(s.Path(registryKey))
	if err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{fakeSettings().Credential, fakeSettings().Endpoint, "private-friendly-connection", "private-workspace-"} {
		if bytes.Contains(encoded, []byte(private)) {
			t.Fatal("personal registry contains plaintext metadata or a credential")
		}
	}
}

func TestConnectionRegistryProcessHelper(t *testing.T) {
	dir := os.Getenv("ONEBYONE_CONNECTION_REGISTRY_TEST_DIR")
	if dir == "" {
		t.Skip("subprocess-only fixture")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		id := fmt.Sprintf("%s-%d", os.Getenv("ONEBYONE_CONNECTION_REGISTRY_TEST_ID"), i)
		_, err = s.UpdateConnections(func(r *Registry) error {
			r.Connections = append(r.Connections, Connection{ID: id, Name: "private-friendly-connection-" + id, LLMSettings: fakeSettings()})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestConnectionRegistryFailedTransactionDoesNotWrite(t *testing.T) {
	s := openTestStore(t)
	if _, err := s.UpdateConnections(func(r *Registry) error {
		r.Connections = []Connection{{ID: "one", Name: "private", LLMSettings: fakeSettings()}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Path(registryKey))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("fixture rejected change")
	if _, err = s.UpdateConnections(func(r *Registry) error {
		r.Connections[0].Credential = "must-not-save"
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("transaction error = %v", err)
	}
	after, err := os.ReadFile(s.Path(registryKey))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("rejected transaction changed the ciphertext")
	}
}

func TestConnectionRegistryDiscardsOnlyInvalidDocumentAndAllowsRegistration(t *testing.T) {
	for _, kind := range []string{"old-fields", "version", "missing-version", "missing-id", "duplicate-id", "bad-envelope", "bad-ciphertext", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			s := openTestStore(t)
			invalid := any(Registry{Version: registryVersion, Connections: []Connection{{ID: "one"}}})
			switch kind {
			case "old-fields":
				invalid = map[string]any{"version": 1, "connections": []Connection{{ID: "old", LLMSettings: fakeSettings()}}, "selections": map[string]string{"workspace": "old"}, "migrated": map[string]bool{"workspace": true}}
			case "version":
				invalid = Registry{Version: 99}
			case "missing-version":
				invalid = map[string]any{"connections": []Connection{}}
			case "missing-id":
				invalid = Registry{Version: registryVersion, Connections: []Connection{{Name: "missing id"}}}
			case "duplicate-id":
				invalid = Registry{Version: registryVersion, Connections: []Connection{{ID: "same"}, {ID: "same"}}}
			}
			if err := s.saveValue(registryKey, invalid); err != nil {
				t.Fatal(err)
			}
			var broken []byte
			switch kind {
			case "bad-envelope":
				broken = []byte(`{not-json`)
			case "bad-ciphertext":
				broken = []byte(`{"version":1,"ciphertext":"aW52YWxpZA=="}`)
			case "oversize":
				broken = bytes.Repeat([]byte("X"), maxProfileBytes+1)
			}
			if broken != nil {
				if err := os.WriteFile(s.Path(registryKey), broken, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.Save("unrelated-profile", fakeSettings()); err != nil {
				t.Fatal(err)
			}
			workspacePath := filepath.Join(filepath.Dir(s.dir), "workspaces", "workspace-id", "setting.json")
			if err := os.MkdirAll(filepath.Dir(workspacePath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(workspacePath, []byte(`{"config":{"root":"fixture"}}`), 0600); err != nil {
				t.Fatal(err)
			}
			preserved := make(map[string][]byte)
			for _, path := range []string{filepath.Join(s.dir, keyFile), s.Path("unrelated-profile"), workspacePath} {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				preserved[path] = data
			}
			current, err := s.LoadConnections()
			if err != nil || current.Version != registryVersion || len(current.Connections) != 0 {
				t.Fatalf("invalid settings did not reset to an empty connection list: %v", err)
			}
			if _, err := os.Lstat(s.Path(registryKey)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid registry file was not discarded")
			}
			for path, before := range preserved {
				after, err := os.ReadFile(path)
				if err != nil || !bytes.Equal(before, after) {
					t.Fatalf("discarding settings modified an unrelated file: %s", filepath.Base(path))
				}
			}
			if _, err := s.UpdateConnections(func(r *Registry) error {
				r.Connections = append(r.Connections, Connection{ID: "new", Name: "new registration", LLMSettings: fakeSettings()})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(s.dir)
			if err != nil {
				t.Fatal(err)
			}
			current, err = reopened.LoadConnections()
			if err != nil || len(current.Connections) != 1 || current.Connections[0].ID != "new" || !reflect.DeepEqual(current.Connections[0].LLMSettings, fakeSettings()) {
				t.Fatalf("registration after discarding settings could not be restored: %v", err)
			}
		})
	}
}

func TestConnectionRegistryUpdateRecoversInvalidDocumentWithoutNestedLock(t *testing.T) {
	s := openTestStore(t)
	if err := os.WriteFile(s.Path(registryKey), []byte(`not-json`), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := s.UpdateConnections(func(r *Registry) error {
			if len(r.Connections) != 0 || r.Version != registryVersion {
				return errors.New("invalid registry reached update callback")
			}
			r.Connections = []Connection{{ID: "new", LLMSettings: fakeSettings()}}
			return nil
		})
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("registry recovery deadlocked while updating")
	}
}

func TestConnectionRegistryReaderWaitsForWriterAndKeepsRepairedDocument(t *testing.T) {
	s := openTestStore(t)
	if err := os.WriteFile(s.Path(registryKey), []byte(`not-json`), 0600); err != nil {
		t.Fatal(err)
	}
	unlock, err := s.lockConnections()
	if err != nil {
		t.Fatal(err)
	}
	locked := true
	defer func() {
		if locked {
			unlock()
		}
	}()
	type result struct {
		registry Registry
		err      error
	}
	done := make(chan result, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		r, err := s.LoadConnections()
		done <- result{r, err}
	}()
	<-started
	select {
	case <-done:
		t.Fatal("reader bypassed an active connection transaction")
	case <-time.After(50 * time.Millisecond):
	}
	if err := s.saveValue(registryKey, Registry{Version: registryVersion, Connections: []Connection{{ID: "repaired", LLMSettings: fakeSettings()}}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(s.Path(registryKey))
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	locked = false
	select {
	case result := <-done:
		if result.err != nil || len(result.registry.Connections) != 1 || result.registry.Connections[0].ID != "repaired" {
			t.Fatalf("reader discarded a document repaired while it waited for the lock: %v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader did not resume when the transaction released its lock")
	}
	after, err := os.ReadFile(s.Path(registryKey))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("reading a valid registry changed or removed its ciphertext")
	}
}

func TestConnectionRegistryPermissionErrorDoesNotDiscardOrUpdate(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("requires unprivileged Unix mode permissions")
	}
	s := openTestStore(t)
	if err := s.saveValue(registryKey, Registry{Version: registryVersion, Connections: []Connection{{ID: "existing"}}}); err != nil {
		t.Fatal(err)
	}
	path := s.Path(registryKey)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	if _, err := s.LoadConnections(); !errors.Is(err, os.ErrPermission) {
		t.Fatalf("read permission failure was swallowed or treated as invalid contents: %v", err)
	}
	called := false
	if _, err := s.UpdateConnections(func(*Registry) error { called = true; return nil }); !errors.Is(err, os.ErrPermission) || called {
		t.Fatalf("update ran after a read permission failure: %v", err)
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("permission failure discarded or changed the registry")
	}
}

func TestConnectionRegistryUnsafePathsArePreservedAndRejected(t *testing.T) {
	for _, kind := range []string{"symlink", "directory"} {
		t.Run(kind, func(t *testing.T) {
			s := openTestStore(t)
			path := s.Path(registryKey)
			outside := filepath.Join(t.TempDir(), "untouched.json")
			data := []byte("unrelated-content")
			if err := os.WriteFile(outside, data, 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "symlink" {
				if err := os.Symlink(outside, path); err != nil {
					t.Skipf("symlinks unavailable: %v", err)
				}
			} else if err := os.Mkdir(path, 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := s.LoadConnections(); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("unsafe registry path accepted: %v", err)
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatal("unsafe registry path was removed")
			}
			after, err := os.ReadFile(outside)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("registry recovery touched a symlink destination")
			}
		})
	}
}

func TestConnectionRegistryChangedSnapshotCannotBeRemoved(t *testing.T) {
	s := openTestStore(t)
	path := s.Path(registryKey)
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	profiles, err := s.openProfiles()
	if err != nil {
		t.Fatal(err)
	}
	defer profiles.Close()
	_, snapshot, err := readBoundedSnapshot(profiles, profileName(registryKey), maxProfileBytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.saveValue(registryKey, Registry{Version: registryVersion, Connections: []Connection{{ID: "replaced"}}}); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := removeSnapshot(profiles, profileName(registryKey), snapshot); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("replaced inode was eligible for deletion: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("a stale snapshot removed a replacement document")
	}
}
