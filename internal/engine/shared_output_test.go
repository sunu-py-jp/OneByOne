package engine

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"onebyone/internal/model"
)

func sharedOutputFixture(t *testing.T, sourceMode os.FileMode) model.Config {
	t.Helper()
	base := t.TempDir()
	root := filepath.Join(base, "source")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, sourceMode); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Root = root
	cfg.QueuePath = filepath.Join(base, "output", "workspace", "queue.jsonl")
	return cfg
}

func assertOutputMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != want {
		t.Fatalf("%s mode = %04o, want %04o", path, info.Mode().Perm(), want)
	}
}

func TestSharedOutputNewModesFollowSourcePolicy(t *testing.T) {
	for _, test := range []struct {
		name  string
		dirs  os.FileMode
		files os.FileMode
	}{{"private", 0700, 0600}, {"group-shared", 0775, 0664}, {"read-shared", 0755, 0644}} {
		t.Run(test.name, func(t *testing.T) {
			cfg := sharedOutputFixture(t, test.dirs)
			if err := prepareSharedOutput(cfg); err != nil {
				t.Fatal(err)
			}
			for _, dir := range []string{filepath.Dir(cfg.QueuePath), cfg.QueuePath + ".artifacts", filepath.Join(filepath.Dir(cfg.QueuePath), "reports")} {
				assertOutputMode(t, dir, test.dirs)
			}
			for _, path := range []string{cfg.QueuePath, cfg.QueuePath + ".session.json", filepath.Join(cfg.QueuePath+".artifacts", "attempt.diff"), filepath.Join(filepath.Dir(cfg.QueuePath), "reports", "result.json")} {
				if err := writeSharedArtifact(cfg, path, []byte("content")); err != nil {
					t.Fatal(err)
				}
				assertOutputMode(t, path, test.files)
				got, err := os.ReadFile(path)
				if err != nil || string(got) != "content" {
					t.Fatalf("snapshot roundtrip: %q %v", got, err)
				}
			}
		})
	}
}

func TestSharedOutputPreservesExistingPrivatePolicies(t *testing.T) {
	cfg := sharedOutputFixture(t, 0775)
	dir := filepath.Dir(cfg.QueuePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.QueuePath, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := prepareSharedOutput(cfg); err != nil {
		t.Fatal(err)
	}
	if err := writeSharedArtifact(cfg, cfg.QueuePath, []byte("new")); err != nil {
		t.Fatal(err)
	}
	assertOutputMode(t, dir, 0700)
	assertOutputMode(t, cfg.QueuePath, 0600)
	got, err := os.ReadFile(cfg.QueuePath)
	if err != nil || string(got) != "new" {
		t.Fatalf("replacement failed: %q %v", got, err)
	}
}

func TestSharedOutputArtifactsRespectIndividualSourceFilePolicy(t *testing.T) {
	cfg := sharedOutputFixture(t, 0775)
	source := filepath.Join(cfg.Root, "private.txt")
	if err := os.WriteFile(source, []byte("private source"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{".before", ".after", ".diff"} {
		path := filepath.Join(cfg.QueuePath+".artifacts", "attempt"+suffix)
		if err := writeSharedArtifact(cfg, path, []byte("private source"), source); err != nil {
			t.Fatal(err)
		}
		assertOutputMode(t, path, 0600)
	}
	if runtime.GOOS == "windows" {
		return // Windows ACLs, rather than POSIX mode bits, control disclosure.
	}
	path := filepath.Join(cfg.QueuePath+".artifacts", "public-existing.diff")
	if err := os.WriteFile(path, []byte("keep old"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeSharedArtifact(cfg, path, []byte("private replacement"), source); err == nil {
		t.Fatal("published private source into an explicitly public destination")
	}
	got, _ := os.ReadFile(path)
	if string(got) != "keep old" {
		t.Fatal("rejected publication changed existing content")
	}
	assertOutputMode(t, path, 0644)
}

func TestSharedOutputRejectsSymlinksAndNonregularDestinations(t *testing.T) {
	for _, kind := range []string{"parent symlink", "destination symlink", "destination directory"} {
		t.Run(kind, func(t *testing.T) {
			cfg := sharedOutputFixture(t, 0700)
			outside := filepath.Join(t.TempDir(), "outside")
			if err := os.WriteFile(outside, []byte("untouched"), 0600); err != nil {
				t.Fatal(err)
			}
			if kind == "parent symlink" {
				parent := filepath.Dir(filepath.Dir(cfg.QueuePath))
				if err := os.Symlink(filepath.Dir(outside), parent); err != nil {
					if runtime.GOOS == "windows" {
						t.Skip("symlink privileges unavailable")
					}
					t.Fatal(err)
				}
				if err := prepareSharedOutput(cfg); err == nil {
					t.Fatal("prepared a symlink output parent")
				}
			} else {
				if err := prepareSharedOutput(cfg); err != nil {
					t.Fatal(err)
				}
				if kind == "destination symlink" {
					if err := os.Symlink(outside, cfg.QueuePath); err != nil {
						if runtime.GOOS == "windows" {
							t.Skip("symlink privileges unavailable")
						}
						t.Fatal(err)
					}
				} else if err := os.Mkdir(cfg.QueuePath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := writeSharedArtifact(cfg, cfg.QueuePath, []byte("overwrite")); err == nil {
				t.Fatal("accepted unsafe output")
			}
			got, err := os.ReadFile(outside)
			if err != nil || string(got) != "untouched" {
				t.Fatalf("outside content changed: %q %v", got, err)
			}
		})
	}
}

func TestSharedOutputRejectsMissingSettingsAndCleansTemporaryFiles(t *testing.T) {
	cfg := sharedOutputFixture(t, 0700)
	missing := cfg
	missing.QueuePath = ""
	if err := prepareSharedOutput(missing); err == nil {
		t.Fatal("missing queue path accepted")
	}
	missing = cfg
	missing.Root = filepath.Join(cfg.Root, "missing")
	if err := writeSharedArtifact(missing, cfg.QueuePath, nil); err == nil {
		t.Fatal("missing source policy accepted")
	}
	for _, content := range []string{"first", "second"} {
		if err := writeSharedArtifact(cfg, cfg.QueuePath, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(cfg.QueuePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != filepath.Base(cfg.QueuePath) {
		t.Fatalf("temporary snapshot leak: %v", entries)
	}
}
