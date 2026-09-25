package engine

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"onebyone/internal/model"
)

func TestCRLFBOMAndExactEditsPreserved(t *testing.T) {
	original := []byte("\ufefffirst\r\nLegacy.Save()\r\nlast\r\n")
	decoded, err := decodeSource(original)
	if err != nil || decoded.text != "first\nLegacy.Save()\nlast\n" {
		t.Fatal(decoded, err)
	}
	after, err := applyEdits(decoded.text, []model.Edit{{OldText: "Legacy.Save()", NewText: "Modern.Save()"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.encode(after); !bytes.Equal(got, []byte("\ufefffirst\r\nModern.Save()\r\nlast\r\n")) {
		t.Errorf("encoding/newlines changed: %q", got)
	}
	for _, bad := range [][]byte{[]byte("mixed\r\nnew\n"), []byte("old\rnew"), {0xff}, []byte("a\x00b")} {
		if _, err := decodeSource(bad); err == nil {
			t.Errorf("accepted unsupported source: %q", bad)
		}
	}
}

func TestEditsUseDisjointOriginalLocations(t *testing.T) {
	got, err := applyEdits("alpha beta", []model.Edit{{OldText: "alpha", NewText: "beta"}, {OldText: "beta", NewText: "gamma"}})
	if err != nil || got != "beta gamma" {
		t.Fatalf("independent original edits should succeed: %q, %v", got, err)
	}
	for _, tc := range []struct {
		source string
		edits  []model.Edit
	}{
		{"aaa", []model.Edit{{OldText: "aa", NewText: "b"}}},
		{"alpha", []model.Edit{{OldText: "alpha", NewText: "beta"}, {OldText: "beta", NewText: "gamma"}}},
		{"abc", []model.Edit{{OldText: "ab", NewText: "x"}, {OldText: "bc", NewText: "y"}}},
		{"abc", []model.Edit{{OldText: "abc", NewText: ""}}},
		{"abc", []model.Edit{{OldText: "abc", NewText: "a\x00b"}}},
	} {
		if out, err := applyEdits(tc.source, tc.edits); err == nil {
			t.Errorf("unsafe edit accepted: %q -> %q", tc.source, out)
		}
	}
}

func TestContextScopeAndLineBounds(t *testing.T) {
	root := t.TempDir()
	writeTest(t, filepath.Join(root, "source.txt"), []byte("first\r\nsecond\r\nthird\r\n"))
	if got, err := readContext(root, "source.txt", 2, 3, 4096); err != nil || got != "second\nthird" {
		t.Errorf("bad line slice: %q %v", got, err)
	}
	for _, forbidden := range []string{"AGENTS.md", "nested/CLAUDE.md", ".git/config", ".env", ".env.local", ".ssh/config", ".aws/credentials", ".azure/accessTokens.json", ".claude/settings.json", ".npmrc", "service.pem", "service.key", "service.pfx", "GEMINI.md", "SKILL.md"} {
		writeTest(t, filepath.Join(root, filepath.FromSlash(forbidden)), []byte("secret\n"))
		if got, err := readContext(root, forbidden, 1, 1, 4096); err == nil {
			t.Errorf("read forbidden context %q: %q", forbidden, got)
		}
	}
	for _, path := range []string{"../secret", "/etc/passwd", "C:/Users/secret", "source.txt.", "source.txt ", "NUL"} {
		if _, err := readContext(root, path, 1, 1, 4096); err == nil {
			t.Errorf("unsafe path accepted: %q", path)
		}
	}
	for _, span := range [][2]int{{0, 1}, {1, 201}, {3, 2}, {999, 1000}} {
		if _, err := readContext(root, "source.txt", span[0], span[1], 4096); err == nil {
			t.Errorf("unsafe range accepted: %v", span)
		}
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	writeTest(t, outside, []byte("secret"))
	if err := os.Symlink(outside, filepath.Join(root, "link.txt")); err == nil {
		if _, err := readContext(root, "link.txt", 1, 1, 4096); err == nil {
			t.Error("followed context symlink")
		}
	}
}
