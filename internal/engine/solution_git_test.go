package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func solutionGitFixture(t *testing.T, attributes string) (string, string) {
	t.Helper()
	repo := t.TempDir()
	writeTest(t, filepath.Join(repo, "src", "Main.vb"), []byte("Class OldType\nEnd Class\n"))
	writeTest(t, filepath.Join(repo, ".gitattributes"), []byte(attributes))
	gitTest(t, repo, "init", "-q")
	gitTest(t, repo, "add", ".")
	gitTest(t, repo, "commit", "-qm", "initial")
	return repo, gitTest(t, repo, "rev-parse", "HEAD")
}

func TestPrepareSolutionInputsAllowsCRLFCheckoutAndBuildsBlobBytes(t *testing.T) {
	repo, head := solutionGitFixture(t, "*.vb text eol=crlf\n")
	scratch := filepath.Join(t.TempDir(), "scratch")
	gitTest(t, repo, "worktree", "add", "--detach", scratch, head)
	t.Cleanup(func() { gitTest(t, repo, "worktree", "remove", "--force", scratch) })
	path := filepath.Join(scratch, "src", "Main.vb")
	checkout, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(checkout, []byte("\r\n")) {
		t.Fatalf("fixture did not create a CRLF checkout: %q", checkout)
	}
	original, texts, err := prepareSolutionInputs(context.Background(), filepath.Join(scratch, "src"), scratch, head, "src", []string{"Main.vb"})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("Class OldType\nEnd Class\n")
	if !bytes.Equal(original["Main.vb"], want) || texts["Main.vb"] != string(want) {
		t.Fatalf("unexpected immutable inputs: %#v", texts)
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, want) {
		t.Fatalf("scratch was not pinned to blob: %q %v", actual, err)
	}
}

func TestStageSolutionCandidatePreservesReviewedBytesWithoutEOLFilters(t *testing.T) {
	repo, head := solutionGitFixture(t, "*.vb text eol=crlf\n")
	candidate := []byte("Class NewType\r\nEnd Class\r\n")
	tree, err := stageSolutionCandidate(context.Background(), repo, head, "src", []string{"Main.vb"}, map[string][]byte{"Main.vb": candidate})
	if err != nil {
		t.Fatal(err)
	}
	actual, err := git(context.Background(), repo, "cat-file", "blob", tree+":src/Main.vb")
	if err != nil || !bytes.Equal([]byte(actual), candidate) {
		t.Fatalf("staged bytes changed: %q %v", actual, err)
	}
	mode, err := solutionBlobMode(context.Background(), repo, head, "src/Main.vb")
	if err != nil || mode != "100644" {
		t.Fatalf("mode: %s %v", mode, err)
	}
	if got := gitTest(t, repo, "rev-parse", "HEAD"); got != head {
		t.Fatal("staging moved HEAD")
	}
}

func TestSolutionGitRejectsCleanFilterWithoutExecutingIt(t *testing.T) {
	repo, head := solutionGitFixture(t, "")
	marker := filepath.Join(t.TempDir(), "filter-ran")
	t.Setenv("ONEBYONE_TEST_FILTER_MARKER", marker)
	gitTest(t, repo, "config", "filter.poison.clean", `echo invoked > "$ONEBYONE_TEST_FILTER_MARKER"; cat`)
	writeTest(t, filepath.Join(repo, ".git", "info", "attributes"), []byte("*.vb filter=poison\n"))
	_, err := stageSolutionCandidate(context.Background(), repo, head, "src", []string{"Main.vb"}, map[string][]byte{"Main.vb": []byte("changed\n")})
	if err == nil || !strings.Contains(err.Error(), "filter属性") {
		t.Fatalf("custom filter not rejected: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("Git clean filter was executed")
	}
	if _, _, err := prepareSolutionInputs(context.Background(), filepath.Join(repo, "src"), repo, head, "src", []string{"Main.vb"}); err == nil || !strings.Contains(err.Error(), "filter属性") {
		t.Fatalf("filtered input accepted: %v", err)
	}
}

func TestPrepareSolutionInputsRejectsCheckoutContentChanges(t *testing.T) {
	repo, head := solutionGitFixture(t, "")
	path := filepath.Join(repo, "src", "Main.vb")
	writeTest(t, path, []byte("Class Unexpected\nEnd Class\n"))
	if _, _, err := prepareSolutionInputs(context.Background(), filepath.Join(repo, "src"), repo, head, "src", []string{"Main.vb"}); err == nil {
		t.Fatal("modified checkout accepted")
	}
	if got := readTest(t, path); !strings.Contains(got, "Unexpected") {
		t.Fatal("refused checkout was modified")
	}
}
