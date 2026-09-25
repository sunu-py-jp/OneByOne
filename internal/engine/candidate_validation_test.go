package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"onebyone/internal/model"
)

func candidateFixture(t *testing.T, content string) candidateValidationInput {
	t.Helper()
	s, cfg := fixture(t, map[string]string{"src/A.txt": content, "src/B.txt": "untouched\n"})
	if err := s.prepareWorktree(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	worktree, source, relative, cat := s.state.Worktree, s.sourceDirLocked(), s.meta.SourceRelative, s.cat
	s.mu.Unlock()
	file := "src/A.txt"
	return candidateValidationInput{
		Config: cfg, Catalog: cat, Worktree: worktree, Source: source, File: file,
		Relative: filepath.ToSlash(filepath.Join(relative, file)), Head: gitTest(t, worktree, "rev-parse", "HEAD"),
		Before: []byte(content), InputHash: digest([]byte(content)), Artifact: filepath.Join(cfg.QueuePath+".artifacts", "test-attempt"),
	}
}

func candidateRequest(in candidateValidationInput, oldText, newText string) model.CandidateRequest {
	return model.CandidateRequest{PlanRevision: 1, BaseHash: in.InputHash, AddressedItemIDs: []string{"P01"}, Edits: []model.Edit{{OldText: oldText, NewText: newText}}}
}

func assertCandidateClean(t *testing.T, in candidateValidationInput) {
	t.Helper()
	if got := readTest(t, filepath.Join(in.Source, in.File)); got != string(in.Before) {
		t.Fatalf("candidate was not rolled back: %q", got)
	}
	if got := readTest(t, filepath.Join(in.Config.Root, in.File)); got != string(in.Before) {
		t.Fatalf("user checkout was changed: %q", got)
	}
	if got := gitTest(t, in.Worktree, "rev-parse", "HEAD"); got != in.Head {
		t.Fatalf("validation committed a candidate: %q", got)
	}
	if got := gitTest(t, in.Worktree, "status", "--porcelain=v1", "--untracked-files=all"); got != "" {
		t.Fatalf("validation left a dirty worktree: %q", got)
	}
}

func TestCandidateValidationRejectsResidueThenAcceptsCorrectedOriginalEdits(t *testing.T) {
	in := candidateFixture(t, "Legacy.Save()\nLegacy.Load()\n")
	journalCalls := 0
	in.Journal = func(result model.CandidateValidation) error {
		journalCalls++
		if result.CandidateHash == "" || result.CandidateID == "" || result.Passed {
			t.Fatalf("invalid pre-write journal: %+v", result)
		}
		assertCandidateClean(t, in)
		return nil
	}
	failed, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
	if err != nil || failed.Passed || len(failed.Diagnostics) != 1 {
		t.Fatalf("residue was not a recoverable diagnostic: %+v, %v", failed, err)
	}
	diagnostic := failed.Diagnostics[0]
	if diagnostic.Check != "legacy_symbols" || diagnostic.Line != 2 || diagnostic.LineBasis != "candidate" || diagnostic.Excerpt != "Legacy.Load()" || diagnostic.ItemID != "" {
		t.Fatalf("wrong candidate coordinates or invented plan association: %+v", diagnostic)
	}
	assertCandidateClean(t, in)
	request := candidateRequest(in, "Legacy.Save()", "Modern.Save()")
	request.Edits = append(request.Edits, model.Edit{OldText: "Legacy.Load()", NewText: "Modern.Load()"})
	passed, err := validateCandidate(context.Background(), in, request)
	if err != nil || !passed.Passed || len(passed.Diagnostics) != 0 || passed.CandidateID == failed.CandidateID || journalCalls != 2 {
		t.Fatalf("corrected full proposal failed: %+v, %v; journals %d", passed, err, journalCalls)
	}
	assertCandidateClean(t, in)
	for _, result := range []model.CandidateValidation{failed, passed} {
		prefix := in.Artifact + ".candidate-" + result.CandidateID
		if got := readTest(t, prefix+".diff"); !strings.Contains(got, "+Modern.Save()") {
			t.Fatalf("candidate diff was not preserved: %q", got)
		}
		var stored model.CandidateValidation
		if err := json.Unmarshal([]byte(readTest(t, prefix+".validation.json")), &stored); err != nil || stored.Passed != result.Passed {
			t.Fatalf("candidate result was not persisted: %+v, %v", stored, err)
		}
		if digest([]byte(readTest(t, prefix+".after"))) != result.CandidateHash {
			t.Fatal("recorded candidate hash does not identify the preserved bytes")
		}
	}
}

func TestCandidateValidationKeepsBOMAndCRLF(t *testing.T) {
	in := candidateFixture(t, "\ufeffLegacy.Save()\r\nuntouched\r\n")
	result, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
	if err != nil || !result.Passed {
		t.Fatalf("valid BOM/CRLF candidate failed: %+v, %v", result, err)
	}
	if got := readTest(t, in.Artifact+".candidate-"+result.CandidateID+".after"); got != "\ufeffModern.Save()\r\nuntouched\r\n" {
		t.Fatalf("candidate changed encoding/newlines: %q", got)
	}
	assertCandidateClean(t, in)
}

func TestCandidateValidationInvalidEditsAndStaleRequestAreRecoverable(t *testing.T) {
	for _, kind := range []string{"absent", "empty", "overlap", "stale-hash", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			in := candidateFixture(t, "Legacy.Save()\n")
			request := candidateRequest(in, "Legacy.Save()", "Modern.Save()")
			switch kind {
			case "absent":
				request.Edits[0].OldText = "not found"
			case "empty":
				request.Edits = nil
			case "overlap":
				request.Edits = append(request.Edits, model.Edit{OldText: "Save", NewText: "Load"})
			case "stale-hash":
				request.BaseHash = digest([]byte("different base"))
			case "oversize":
				in.Config.MaxFileBytes = len(in.Before)
				request.Edits[0].NewText = strings.Repeat("x", 100)
			}
			journaled := false
			in.Journal = func(model.CandidateValidation) error { journaled = true; return nil }
			result, err := validateCandidate(context.Background(), in, request)
			if err != nil || result.Passed || len(result.Diagnostics) == 0 || journaled {
				t.Fatalf("bad edit entered mutation or stopped loop: %+v, %v, journaled %v", result, err, journaled)
			}
			assertCandidateClean(t, in)
		})
	}
}

func TestCandidateValidationPreservesUnexpectedDirtyState(t *testing.T) {
	for _, kind := range []string{"target", "other", "untracked", "new-head", "journal"} {
		t.Run(kind, func(t *testing.T) {
			in := candidateFixture(t, "Legacy.Save()\n")
			path := filepath.Join(in.Source, "src/B.txt")
			mutate := func() { writeTest(t, path, []byte("user work\n")) }
			switch kind {
			case "target":
				path = filepath.Join(in.Source, in.File)
				mutate()
			case "other":
				mutate()
			case "untracked":
				path = filepath.Join(in.Source, "src/added.txt")
				mutate()
			case "new-head":
				mutate()
				gitTest(t, in.Worktree, "add", ".")
				gitTest(t, in.Worktree, "commit", "-qm", "someone else's commit")
			case "journal":
				in.Journal = func(model.CandidateValidation) error { mutate(); return nil }
			}
			result, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
			if err == nil || result.Passed {
				t.Fatalf("unexpected change did not stop validation: %+v, %v", result, err)
			}
			if got := readTest(t, path); got != "user work\n" {
				t.Fatalf("unexpected changes were destroyed: %q", got)
			}
		})
	}
}

func TestCandidateValidationJournalFailureDoesNotWriteWorktree(t *testing.T) {
	in := candidateFixture(t, "Legacy.Save()\n")
	sentinel := errors.New("journal unavailable")
	in.Journal = func(model.CandidateValidation) error { return sentinel }
	result, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
	if !errors.Is(err, sentinel) || result.Passed {
		t.Fatalf("failed journal allowed validation: %+v, %v", result, err)
	}
	assertCandidateClean(t, in)
}

func TestCandidateValidationRejectsMismatchedCleanBase(t *testing.T) {
	in := candidateFixture(t, "Legacy.Save()\n")
	in.Before = []byte("Legacy.Load()\n")
	in.InputHash = digest(in.Before)
	result, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Load()", "Modern.Load()"))
	if err == nil || result.Passed {
		t.Fatalf("stale baseline was accepted: %+v, %v", result, err)
	}
	if got := readTest(t, filepath.Join(in.Source, in.File)); got != "Legacy.Save()\n" {
		t.Fatalf("stale baseline replaced source: %q", got)
	}
}

func candidateHelperCommand(mode string, args ...string) model.Command {
	return model.Command{Name: "candidate test", Executable: os.Args[0], Args: append([]string{"-test.run=^TestCandidateValidationHelperProcess$", "--", "candidate-validation-helper", mode}, args...)}
}

func TestCandidateValidationFailedCheckAndMutatingChecksRollback(t *testing.T) {
	for _, mode := range []string{"fail", "change-target", "change-other", "create-other"} {
		t.Run(mode, func(t *testing.T) {
			in := candidateFixture(t, "Legacy.Save()\n")
			in.Config.CheckCommands = []model.Command{candidateHelperCommand(mode)}
			result, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
			if err != nil || result.Passed || len(result.Diagnostics) == 0 {
				t.Fatalf("check failure was adopted: %+v, %v", result, err)
			}
			assertCandidateClean(t, in)
			if got := readTest(t, filepath.Join(in.Source, "src/B.txt")); got != "untouched\n" {
				t.Fatalf("other file not restored: %q", got)
			}
		})
	}
}

func TestCandidateValidationCanceledCheckStillRollsBack(t *testing.T) {
	in := candidateFixture(t, "Legacy.Save()\n")
	marker := filepath.Join(t.TempDir(), "started")
	in.Config.CheckCommands = []model.Command{candidateHelperCommand("wait", marker)}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := validateCandidate(ctx, in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatal("checker never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation was not propagated: %v", err)
	}
	assertCandidateClean(t, in)
}

func TestCandidateValidationUnexpectedCommitStopsWithoutResettingIt(t *testing.T) {
	in := candidateFixture(t, "Legacy.Save()\n")
	in.Config.CheckCommands = []model.Command{candidateHelperCommand("commit")}
	result, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
	if err == nil || result.Passed || !strings.Contains(err.Error(), "想定外のコミット") {
		t.Fatalf("unexpected commit did not stop the runner: %+v, %v", result, err)
	}
	if got := gitTest(t, in.Worktree, "rev-parse", "HEAD"); got == in.Head {
		t.Fatal("unknown commit was destructively rolled back")
	}
	if got := readTest(t, filepath.Join(in.Config.Root, in.File)); got != string(in.Before) {
		t.Fatalf("user source changed: %q", got)
	}
	var stored model.CandidateValidation
	if err := json.Unmarshal([]byte(readTest(t, in.Artifact+".candidate-"+result.CandidateID+".validation.json")), &stored); err != nil || stored.Passed {
		t.Fatalf("failed cleanup retained a passing validation: %+v, %v", stored, err)
	}
}

func TestCandidateDiagnosticsAreBoundedAndDoNotInventSourceCoordinates(t *testing.T) {
	detail := strings.Repeat("長い説明", 100) + "\n"
	checks := []model.Check{{Name: "tests", Status: "failed", Detail: strings.Repeat(detail, 50)}}
	diagnostics := candidateDiagnostics(checks, "A.txt")
	if len(diagnostics) != 20 {
		t.Fatalf("diagnostics not bounded: %d", len(diagnostics))
	}
	for _, diagnostic := range diagnostics {
		if len(diagnostic.Message) > 203 || !utf8.ValidString(diagnostic.Message) || diagnostic.Line != 0 || diagnostic.LineBasis != "" || diagnostic.ItemID != "" {
			t.Fatalf("unbounded or invented diagnostic: %+v", diagnostic)
		}
	}
}

func TestCandidateValidationHelperProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "candidate-validation-helper" || i+1 >= len(os.Args) {
			continue
		}
		var err error
		switch os.Args[i+1] {
		case "fail":
			fmt.Fprintln(os.Stderr, "expected assertion failed")
			os.Exit(2)
		case "change-target":
			err = os.WriteFile("src/A.txt", []byte("checker changed proposal\n"), 0600)
		case "change-other":
			err = os.WriteFile("src/B.txt", []byte("checker changed sibling\n"), 0600)
		case "create-other":
			err = os.WriteFile("src/generated.txt", []byte("checker created file\n"), 0600)
		case "commit":
			_, err = git(context.Background(), ".", "add", "src/A.txt")
			if err == nil {
				_, err = git(context.Background(), ".", "commit", "-qm", "unexpected checker commit")
			}
		case "wait":
			err = os.WriteFile(os.Args[i+2], []byte("started"), 0600)
			if err == nil {
				time.Sleep(30 * time.Second)
			}
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(3)
		}
		os.Exit(0)
	}
}
