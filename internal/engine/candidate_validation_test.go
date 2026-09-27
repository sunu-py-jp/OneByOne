package engine

import (
	"context"
	"encoding/json"
	"errors"
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
	return model.CandidateRequest{PlanRevision: 1, BaseHash: in.InputHash, AddressedItemIDs: []string{"P01"}, Edits: []model.Edit{{OldText: oldText, NewText: newText, ItemIDs: []string{"P01"}, Attributions: []model.EditAttribution{}}}}
}

func assertCandidateClean(t *testing.T, in candidateValidationInput) {
	t.Helper()
	if got := readTest(t, filepath.Join(in.Source, in.File)); got != string(in.Before) {
		t.Fatalf("candidate validation touched the worktree: %q", got)
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

func TestCandidateValidationLeavesSemanticResidueForIndependentReview(t *testing.T) {
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
	partial, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
	if err != nil || !partial.Passed || len(partial.Diagnostics) != 0 {
		t.Fatalf("valid edits should pass structural checks independently of semantic residue: %+v, %v", partial, err)
	}
	assertCandidateClean(t, in)
	request := candidateRequest(in, "Legacy.Save()", "Modern.Save()")
	request.Edits = append(request.Edits, model.Edit{OldText: "Legacy.Load()", NewText: "Modern.Load()", ItemIDs: []string{"P01"}, Attributions: []model.EditAttribution{}})
	passed, err := validateCandidate(context.Background(), in, request)
	if err != nil || !passed.Passed || len(passed.Diagnostics) != 0 || passed.CandidateID == partial.CandidateID || journalCalls != 2 {
		t.Fatalf("corrected full proposal failed: %+v, %v; journals %d", passed, err, journalCalls)
	}
	assertCandidateClean(t, in)
	for _, result := range []model.CandidateValidation{partial, passed} {
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

func TestCandidateValidationIgnoresAndPreservesUnadoptedWorktreeState(t *testing.T) {
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
			if err != nil || !result.Passed {
				t.Fatalf("worktree state leaked into immutable candidate validation: %+v, %v", result, err)
			}
			if got := readTest(t, path); got != "user work\n" {
				t.Fatalf("unexpected changes were destroyed: %q", got)
			}
		})
	}
}

func TestCandidateValidationParallelSnapshotsDoNotInterfereWithAnotherCommit(t *testing.T) {
	a := candidateFixture(t, "Legacy.Save()\n")
	b := a
	b.File, b.Relative = "src/B.txt", "src/B.txt"
	b.Before, b.InputHash = []byte("untouched\n"), digest([]byte("untouched\n"))
	b.Artifact = filepath.Join(filepath.Dir(a.Artifact), "second-attempt")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	ready, release := make(chan struct{}, 2), make(chan struct{})
	defer close(release)
	journal := func(model.CandidateValidation) error {
		ready <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	a.Journal, b.Journal = journal, journal
	type completion struct {
		input  candidateValidationInput
		result model.CandidateValidation
		err    error
	}
	completed := make(chan completion, 2)
	for _, in := range []candidateValidationInput{a, b} {
		go func(in candidateValidationInput) {
			request := candidateRequest(in, strings.TrimSpace(string(in.Before)), "updated "+in.File)
			result, err := validateCandidate(ctx, in, request)
			completed <- completion{in, result, err}
		}(in)
	}
	for range 2 {
		select {
		case <-ready:
		case result := <-completed:
			t.Fatalf("validation failed before its snapshot was ready: %+v", result)
		case <-ctx.Done():
			t.Fatal("parallel candidate validation did not reach both journals")
		}
	}
	assertCandidateClean(t, a)
	if got := readTest(t, filepath.Join(a.Source, b.File)); got != string(b.Before) {
		t.Fatalf("parallel candidate was published into worktree: %q", got)
	}
	// Simulate serialized adoption of a different file while both candidates
	// still reference the earlier immutable commit. Neither may reset it.
	writeTest(t, filepath.Join(a.Worktree, "another.txt"), []byte("another adopted file\n"))
	gitTest(t, a.Worktree, "add", "another.txt")
	gitTest(t, a.Worktree, "commit", "-qm", "another file completed")
	adoptedHead := gitTest(t, a.Worktree, "rev-parse", "HEAD")
	// Each waiter consumes a value; the deferred close also releases failures.
	for range 2 {
		release <- struct{}{}
	}
	ids := map[string]bool{}
	for range 2 {
		got := <-completed
		if got.err != nil || !got.result.Passed || ids[got.result.CandidateID] {
			t.Fatalf("parallel snapshots interfered or used duplicate candidate IDs: %+v", got)
		}
		ids[got.result.CandidateID] = true
		prefix := got.input.Artifact + ".candidate-" + got.result.CandidateID
		if content := readTest(t, prefix+".after"); content != "updated "+got.input.File+"\n" {
			t.Fatalf("candidate received another file's contents: %q", content)
		}
		if diff := readTest(t, prefix+".diff"); !strings.Contains(diff, "+++ b/"+got.input.Relative+"\n") || !strings.Contains(diff, "+updated "+got.input.File) {
			t.Fatalf("candidate diff received another file's context: %q", diff)
		}
	}
	if head := gitTest(t, a.Worktree, "rev-parse", "HEAD"); head != adoptedHead || head == a.Head {
		t.Fatalf("candidate validation rolled back another file's adopted commit: %s", head)
	}
	if status := gitTest(t, a.Worktree, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("parallel validation polluted the shared worktree: %s", status)
	}
	for _, in := range []candidateValidationInput{a, b} {
		if got := readTest(t, filepath.Join(in.Source, in.File)); got != string(in.Before) {
			t.Fatalf("candidate changed %s: %q", in.File, got)
		}
	}
	entries, err := os.ReadDir(filepath.Dir(a.Artifact))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".onebyone-candidate-diff-") {
			t.Fatalf("private diff temporary directory was not removed: %s", entry.Name())
		}
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

func TestCandidateValidationCanceledBeforePublicationPreservesSource(t *testing.T) {
	in := candidateFixture(t, "Legacy.Save()\n")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	in.Journal = func(model.CandidateValidation) error { cancel(); return nil }
	result, err := validateCandidate(ctx, in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
	if err == nil || result.Passed {
		t.Fatalf("canceled validation was accepted: %+v, %v", result, err)
	}
	assertCandidateClean(t, in)
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
