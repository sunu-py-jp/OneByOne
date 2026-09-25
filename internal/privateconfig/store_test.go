package privateconfig

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
)

func fakeSettings() LLMSettings {
	return LLMSettings{Provider: "azure", Endpoint: "https://fixture.example.invalid", Deployment: "fixture-model", AuthMode: "api_key", Credential: "FAKE-KEY-privateconfig-test-only"}
}

func openTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func TestRoundTripRestartAndNoPlaintext(t *testing.T) {
	s := openTestStore(t)
	want := fakeSettings()
	key := "a-workspace-id"
	if err := s.Save(key, want); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(s.Path(key))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{want.Provider, want.Endpoint, want.Deployment, want.AuthMode, want.Credential} {
		if bytes.Contains(first, []byte(secret)) {
			t.Fatal("profile contains a plaintext setting")
		}
	}
	restarted, err := Open(s.dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := restarted.Load(key)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("restart round trip failed: %v", err)
	}
	if err := restarted.Save(key, want); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(s.Path(key))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("repeat save reused ciphertext/nonce")
	}
	got, err = s.Load(key)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("repeat save did not round trip: %v", err)
	}
}

func TestProfilesAreIndependentAndNamesConfined(t *testing.T) {
	s := openTestStore(t)
	first := "../workspace/名前"
	second := "../workspace/other"
	if err := s.Save(first, fakeSettings()); err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(s.Path(first)) != filepath.Join(s.dir, profileDir) {
		t.Fatal("profile path escaped its directory")
	}
	if _, err := s.Load(second); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing profile = %v", err)
	}
	ciphertext, err := os.ReadFile(s.Path(first))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(second), ciphertext, 0600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(second)
	if !errors.Is(err, ErrInvalidSettings) || !reflect.DeepEqual(got, LLMSettings{}) {
		t.Fatal("ciphertext authenticated under the wrong workspace")
	}
}

func TestTamperingAndMalformedProfilesFailClosed(t *testing.T) {
	for _, kind := range []string{"ciphertext", "nonce", "version", "truncated", "unknown-field", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			s := openTestStore(t)
			if err := s.Save("workspace", fakeSettings()); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile(s.Path("workspace"))
			if err != nil {
				t.Fatal(err)
			}
			var envelope profileEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "ciphertext":
				envelope.Ciphertext[len(envelope.Ciphertext)-1] ^= 1
			case "nonce":
				envelope.Ciphertext[0] ^= 1
			case "version":
				envelope.Version++
			case "truncated":
				envelope.Ciphertext = envelope.Ciphertext[:5]
			}
			data, _ = json.Marshal(envelope)
			if kind == "unknown-field" {
				data = []byte(`{"version":1,"ciphertext":"","credential":"FAKE-unexpected"}`)
			}
			if kind == "oversize" {
				data = bytes.Repeat([]byte("X"), maxProfileBytes+1)
			}
			if err := os.WriteFile(s.Path("workspace"), data, 0600); err != nil {
				t.Fatal(err)
			}
			got, err := s.Load("workspace")
			if !errors.Is(err, ErrInvalidSettings) || !reflect.DeepEqual(got, LLMSettings{}) {
				t.Fatalf("malformed profile accepted: %v", err)
			}
			if strings.Contains(err.Error(), "FAKE-") {
				t.Fatal("error leaked secret data")
			}
		})
	}
}

func TestInvalidOrLostKeysAreNotRegenerated(t *testing.T) {
	for _, kind := range []string{"lost", "corrupt", "length", "version", "protection"} {
		t.Run(kind, func(t *testing.T) {
			s := openTestStore(t)
			if err := s.Save("workspace", fakeSettings()); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(s.dir, keyFile)
			if kind == "lost" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			} else {
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var envelope keyEnvelope
				if err := json.Unmarshal(data, &envelope); err != nil {
					t.Fatal(err)
				}
				switch kind {
				case "length":
					envelope.Key = []byte("invalid-short-key")
				case "version":
					envelope.Version++
				case "protection":
					envelope.Protection = "unsupported"
				}
				data, _ = json.Marshal(envelope)
				if kind == "corrupt" {
					data = []byte("{not-json")
				}
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Open(s.dir); !errors.Is(err, ErrInvalidStore) {
				t.Fatalf("invalid key accepted: %v", err)
			}
			if kind == "lost" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("lost key was silently regenerated")
				}
			}
		})
	}
}

func TestWrongInstallationKeyFails(t *testing.T) {
	first, second := openTestStore(t), openTestStore(t)
	if err := first.Save("workspace", fakeSettings()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(first.Path("workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second.Path("workspace"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got, err := second.Load("workspace"); !errors.Is(err, ErrInvalidSettings) || !reflect.DeepEqual(got, LLMSettings{}) {
		t.Fatal("different installation decrypted the profile")
	}
}

func TestPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows protects the master key with DPAPI and inherits the user's app-data ACL")
	}
	s := openTestStore(t)
	if err := s.Save("workspace", fakeSettings()); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{s.dir, filepath.Join(s.dir, profileDir), filepath.Join(s.dir, keyFile), filepath.Join(s.dir, ".init.lock"), s.Path("workspace")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0600)
		if info.IsDir() {
			want = 0700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("permissions %o, want %o", info.Mode().Perm(), want)
		}
	}
}

func TestSymlinksRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks may require Windows Developer Mode")
	}
	for _, target := range []string{"directory", "profiles", "key", "lock", "profile"} {
		t.Run(target, func(t *testing.T) {
			s := openTestStore(t)
			if err := s.Save("workspace", fakeSettings()); err != nil {
				t.Fatal(err)
			}
			path := s.dir
			switch target {
			case "profiles":
				path = filepath.Join(s.dir, profileDir)
			case "key":
				path = filepath.Join(s.dir, keyFile)
			case "lock":
				path = filepath.Join(s.dir, ".init.lock")
			case "profile":
				path = s.Path("workspace")
			}
			actual := path + "-actual"
			if err := os.Rename(path, actual); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(actual, path); err != nil {
				t.Fatal(err)
			}
			if target == "profile" {
				if _, err := s.Load("workspace"); err == nil {
					t.Fatal("read followed profile symlink")
				}
				if err := s.Save("workspace", fakeSettings()); err == nil {
					t.Fatal("write accepted profile symlink")
				}
			} else if _, err := Open(s.dir); err == nil {
				t.Fatal("Open accepted symlink")
			}
		})
	}
}

func TestConcurrentInitialization(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	const count = 20
	errors := make(chan error, count)
	var workers sync.WaitGroup
	for i := range count {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			s, err := Open(dir)
			if err == nil {
				err = s.Save(fmt.Sprint(i), fakeSettings())
			}
			errors <- err
		}(i)
	}
	workers.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range count {
		if got, err := s.Load(fmt.Sprint(i)); err != nil || !reflect.DeepEqual(got, fakeSettings()) {
			t.Fatalf("concurrent profile %d cannot be decrypted: %v", i, err)
		}
	}
}

func TestConcurrentInitializationAcrossProcesses(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	commands := make([]*exec.Cmd, 6)
	outputs := make([]bytes.Buffer, len(commands))
	for i := range commands {
		commands[i] = exec.Command(binary, "-test.run=^TestPrivateStoreProcessHelper$")
		commands[i].Env = append(os.Environ(), "ONEBYONE_PRIVATECONFIG_TEST_DIR="+dir, "ONEBYONE_PRIVATECONFIG_TEST_ID="+fmt.Sprint(i))
		commands[i].Stdout, commands[i].Stderr = &outputs[i], &outputs[i]
		if err := commands[i].Start(); err != nil {
			t.Fatal(err)
		}
	}
	for i, command := range commands {
		if err := command.Wait(); err != nil {
			t.Fatalf("process %d failed: %v, %s", i, err, outputs[i].String())
		}
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range commands {
		if got, err := s.Load(fmt.Sprint(i)); err != nil || !reflect.DeepEqual(got, fakeSettings()) {
			t.Fatalf("process profile %d cannot be decrypted: %v", i, err)
		}
	}
}

func TestPrivateStoreProcessHelper(t *testing.T) {
	dir := os.Getenv("ONEBYONE_PRIVATECONFIG_TEST_DIR")
	if dir == "" {
		t.Skip("subprocess-only fixture")
	}
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(os.Getenv("ONEBYONE_PRIVATECONFIG_TEST_ID"), fakeSettings()); err != nil {
		t.Fatal(err)
	}
}

func TestEmptyWorkspaceAndOversizedSettingsRejected(t *testing.T) {
	s := openTestStore(t)
	for _, key := range []string{"", "   ", "bad\x00key", strings.Repeat("a", (16<<10)+1)} {
		if err := s.Save(key, fakeSettings()); !errors.Is(err, ErrInvalidWorkspace) {
			t.Fatal("invalid workspace accepted by Save")
		}
		if _, err := s.Load(key); !errors.Is(err, ErrInvalidWorkspace) {
			t.Fatal("invalid workspace accepted by Load")
		}
	}
	settings := fakeSettings()
	settings.Credential = strings.Repeat("a", maxProfileBytes)
	if err := s.Save("workspace", settings); !errors.Is(err, ErrInvalidSettings) {
		t.Fatal("oversized settings accepted")
	}
	if _, err := s.Load("workspace"); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected save left a profile")
	}
}
