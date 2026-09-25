package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func TestOneByOneSourceChangesBlockRun(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "OneByOne/source.txt": "unchanged\n"})
	writeTest(t, filepath.Join(cfg.Root, "OneByOne", "source.txt"), []byte("unsaved work\n"))
	called := false
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		called = true
		return model.Proposal{}, nil
	}
	st := runTest(t, s, 0)
	if st.LastError == "" || called {
		t.Fatalf("uncommitted source reached AI: %v %s", called, st.LastError)
	}
}

func TestUncommittedSourceChangesBlockRun(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n", "src/other.txt": "unchanged\n"})
	writeTest(t, filepath.Join(cfg.Root, "OneByOne", "azure-settings.json"), []byte("{}"))
	writeTest(t, filepath.Join(cfg.Root, "src", "other.txt"), []byte("unsaved work\n"))
	called := false
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		called = true
		return model.Proposal{}, nil
	}
	st := runTest(t, s, 0)
	if st.LastError == "" || called {
		t.Fatalf("uncommitted source reached AI: called=%v error=%q", called, st.LastError)
	}
}

func TestGitChangesIncludeEveryDirectoryAndBothRenamePaths(t *testing.T) {
	for _, tc := range []struct {
		status string
		dirty  bool
	}{
		{"?? OneByOne/azure-settings.json\x00", true},
		{" M src/oNeByOnE/target-settings.json\x00", true},
		{"R  OneByOne/new.json\x00OneByOne/old.json\x00", true},
		{"R  src/source.txt\x00OneByOne/old.json\x00", true},
		{"R  OneByOne/new.json\x00src/source.txt\x00", true},
		{"C  OneByOne/new.json\x00src/source.txt\x00", true},
		{"?? OneByOne\x00", true},
		{" M OneByOne.go\x00", true},
		{"?? OneByOne/azure-settings.json\x00 M src/file.txt\x00", true},
	} {
		got, err := hasSourceChanges(tc.status)
		if err != nil || got != tc.dirty {
			t.Errorf("status %q => %v, %v; want %v", tc.status, got, err, tc.dirty)
		}
	}
	if _, err := hasSourceChanges("R  OneByOne/new.json\x00"); err == nil {
		t.Error("truncated rename was accepted")
	}
}

func TestOneByOneSourcesCanEnterQueueAndContext(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "OneByOne/source.txt": "Legacy.Save()\n"})
	path := "OneByOne/source.txt"
	queue := filepath.Join(filepath.Dir(cfg.Root), "manual.jsonl")
	if err := store.SaveQueue(queue, []model.Task{{File: path, Status: "pending"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.LoadQueue(queue); err != nil {
		t.Fatal(err)
	}
	if body, err := readContext(cfg.Root, path, 1, 1, 1024); err != nil || !strings.Contains(body, "Legacy.Save()") {
		t.Fatalf("source inaccessible: %q %v", body, err)
	}
}

func TestManagedWorktreeStillDetectsConfigurationSideEffects(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "OneByOne/tracked.json": "Legacy.Save()\n"})
	if err := s.prepareWorktree(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	// Git can detect a rename here. Scope validation must still report the
	// deleted configuration path as well as the one allowed destination.
	if err := os.Remove(filepath.Join(st.Worktree, "A.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(st.Worktree, "OneByOne", "tracked.json"), filepath.Join(st.Worktree, "A.txt")); err != nil {
		t.Fatal(err)
	}
	check, err := scopeCheck(context.Background(), st.Worktree, "A.txt")
	if err != nil || check.Status != "failed" || !strings.Contains(check.Detail, "OneByOne/tracked.json") {
		t.Fatalf("configuration side effect was hidden: %+v %v", check, err)
	}
}
