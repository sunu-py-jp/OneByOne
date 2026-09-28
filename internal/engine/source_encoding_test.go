package engine

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func shiftJISSource(t *testing.T, text string) []byte {
	t.Helper()
	data, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestShiftJISCandidatePreservesRawEncodingAndRejectsLoss(t *testing.T) {
	before := shiftJISSource(t, "// 日本語\r\nLegacy.Save()\n// 保持\r")
	in := candidateFixture(t, string(before))
	result, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save()"))
	if err != nil || !result.Passed {
		t.Fatalf("Shift_JIS validation failed: %+v %v", result, err)
	}
	after, err := os.ReadFile(in.Artifact + ".candidate-" + result.CandidateID + ".after")
	want := shiftJISSource(t, "// 日本語\r\nModern.Save()\n// 保持\r")
	if err != nil || !bytes.Equal(after, want) || result.CandidateHash != digest(want) {
		t.Fatalf("candidate representation changed: %x %v", after, err)
	}
	diff := []byte(readTest(t, in.Artifact+".candidate-"+result.CandidateID+".diff"))
	preview, err := sourceDiffDisplay(diff, before, after)
	if err != nil || !utf8.ValidString(preview) || !strings.Contains(preview, "日本語") || !strings.Contains(preview, "+Modern.Save()") {
		t.Fatalf("diff mojibake: %q %v", preview, err)
	}
	failed, err := validateCandidate(context.Background(), in, candidateRequest(in, "Legacy.Save()", "Modern.Save(); // 😀"))
	if err != nil || failed.Passed || !strings.Contains(checkSummary(failed.Checks), "Shift_JIS") {
		t.Fatalf("lossy candidate accepted: %+v %v", failed, err)
	}
	assertCandidateClean(t, in)
}

func TestShiftJISRunAndSavedViewsUseDecodedTextButOriginalHashes(t *testing.T) {
	beforeText := "// 日本語\r\nLegacy.Save()\r\n// 末尾\n"
	afterText := strings.ReplaceAll(beforeText, "Legacy.Save()", "Modern.Save()")
	before, after := shiftJISSource(t, beforeText), shiftJISSource(t, afterText)
	s, cfg := fixture(t, map[string]string{"src/A.txt": string(before)})
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		if in.Content != strings.ReplaceAll(beforeText, "\r\n", "\n") || in.BaseHash != digest(before) {
			t.Fatalf("LLM got encoded bytes or rewritten hash: %q %s", in.Content, in.BaseHash)
		}
		return successfulProposal(ctx, in)
	}
	state := runTest(t, s, 0)
	if state.Tasks[0].Status != "done" {
		t.Fatalf("run failed: %+v", state.Tasks[0])
	}
	if got := readTest(t, filepath.Join(state.Worktree, "src/A.txt")); !bytes.Equal([]byte(got), after) {
		t.Fatal("adopted bytes changed encoding")
	}
	if got := readTest(t, filepath.Join(cfg.Root, "src/A.txt")); !bytes.Equal([]byte(got), before) {
		t.Fatal("source checkout was changed")
	}
	for _, index := range []int{-1, 0} {
		detail, err := s.GetFileDetail("src/A.txt", index)
		if err != nil || detail.Before != strings.ReplaceAll(beforeText, "\r\n", "\n") || detail.After != strings.ReplaceAll(afterText, "\r\n", "\n") || !utf8.ValidString(detail.Diff) || !strings.Contains(detail.Diff, "日本語") {
			t.Fatalf("detail not decoded: %+v %v", detail, err)
		}
	}
	frozen, err := s.GetExecutionFileDetail(state.ExecutionRuns[0].ID, "src/A.txt", 0)
	if err != nil || frozen.Before != strings.ReplaceAll(beforeText, "\r\n", "\n") || !utf8.ValidString(frozen.Diff) || !strings.Contains(frozen.Diff, "日本語") {
		t.Fatalf("saved execution not decoded: %+v %v", frozen, err)
	}
}

func TestStandaloneCRDiffPositionsFollowNormalizedSource(t *testing.T) {
	before, after := []byte("first\rold\rlast\r"), []byte("first\rnew\rlast\r")
	raw, err := candidateSnapshotDiff(context.Background(), t.TempDir(), "source.txt", before, after)
	if err != nil {
		t.Fatal(err)
	}
	display, err := sourceDiffDisplay([]byte(raw), before, after)
	if err != nil || !strings.Contains(display, "@@ -1,3 +1,3 @@") || !strings.Contains(display, "\n-old\n+new\n") || !strings.Contains(display, "--- a/source.txt") {
		t.Fatalf("CR line positions diverged: %q %v", display, err)
	}
}
