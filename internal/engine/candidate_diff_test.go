package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateSnapshotDiffPreservesTextAndUsesOnlyTargetPaths(t *testing.T) {
	for _, tc := range []struct{ name, path, before, after string }{
		{"unicode and CRLF", "src/請求.txt", "\ufefffirst\r\n旧API();\r\nlast\r\n", "\ufefffirst\r\n新API();\r\nlast\r\n"},
		{"no final newline", "src/file name.txt", "first\nold", "first\nnew"},
		{"header-like source", "src/file.txt", "-- before\nold\n", "++ after\nnew\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			diff, err := candidateSnapshotDiff(context.Background(), dir, tc.path, []byte(tc.before), []byte(tc.after))
			if err != nil || diff == "" || strings.Contains(diff, ".onebyone-candidate-diff-") || !strings.Contains(diff, "--- "+candidateDiffPath("a/"+tc.path)+"\n") || !strings.Contains(diff, "+++ "+candidateDiffPath("b/"+tc.path)+"\n") {
				t.Fatalf("incorrect immutable diff metadata: %q %v", diff, err)
			}
			// Applying the preserved diff in a throwaway directory must recreate
			// the candidate exactly, including newline/encoding and source lines
			// that resemble diff headers. The application's worktree is absent.
			copyDir := t.TempDir()
			writeTest(t, filepath.Join(copyDir, filepath.FromSlash(tc.path)), []byte(tc.before))
			diffPath := filepath.Join(dir, "candidate.diff")
			writeTest(t, diffPath, []byte(diff))
			gitTest(t, copyDir, "apply", "--", diffPath)
			if got := readTest(t, filepath.Join(copyDir, filepath.FromSlash(tc.path))); got != tc.after {
				t.Fatalf("diff did not reconstruct exact candidate bytes: %q", got)
			}
		})
	}
}

func TestCandidateSnapshotDiffRetainsLargeOutputAndCleansTemporaryFiles(t *testing.T) {
	dir := t.TempDir()
	diff, err := candidateSnapshotDiff(context.Background(), dir, "large.txt", []byte(strings.Repeat("a", 1100000)), []byte(strings.Repeat("b", 1100000)))
	if err != nil || len(diff) < 2200000 {
		t.Fatalf("large diff was truncated: %d bytes, %v", len(diff), err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed diff leaked temporary snapshots: %+v %v", entries, err)
	}
}

func TestCandidateSnapshotDiffUnchangedAndCanceledInputs(t *testing.T) {
	dir := t.TempDir()
	if diff, err := candidateSnapshotDiff(context.Background(), dir, "same.txt", []byte("same\n"), []byte("same\n")); err != nil || diff != "" {
		t.Fatalf("unchanged snapshots have a diff: %q %v", diff, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if diff, err := candidateSnapshotDiff(ctx, dir, "file.txt", []byte("old\n"), []byte("new\n")); !errors.Is(err, context.Canceled) || diff != "" {
		t.Fatalf("cancellation was not preserved: %q %v", diff, err)
	}
}
