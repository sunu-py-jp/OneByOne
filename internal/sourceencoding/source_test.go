package sourceencoding

import (
	"bytes"
	"strings"
	"testing"

	"golang.org/x/text/encoding/japanese"
)

func sjis(t *testing.T, value string) []byte {
	t.Helper()
	data, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(value))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestDetectionAndUnchangedBytes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     []byte
		text     string
		encoding Encoding
	}{
		{"UTF-8", []byte("日本語\n"), "日本語\n", UTF8},
		{"BOM CRLF", []byte("\ufeff日本語\r\n"), "日本語\n", UTF8},
		{"mixed newlines", []byte("first\r\nsecond\nthird\rlast"), "first\nsecond\nthird\nlast", UTF8},
		{"Shift JIS", sjis(t, "日本語\r\nｱｲｳ\n"), "日本語\nｱｲｳ\n", ShiftJIS},
		{"CP932 alternate encoding", []byte{0xed, 0x40, '\r', '\n'}, "纊\n", ShiftJIS},
		{"empty BOM", []byte("\ufeff"), "", UTF8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := Decode(tc.data)
			if err != nil || doc.Encoding != tc.encoding || doc.Text != tc.text {
				t.Fatalf("decode: %+v %v", doc, err)
			}
			got, err := doc.Encode(doc.Text)
			if err != nil || !bytes.Equal(got, tc.data) {
				t.Fatalf("unchanged bytes differ: %x != %x (%v)", got, tc.data, err)
			}
		})
	}
}

func TestEditsPreserveOriginalRepresentation(t *testing.T) {
	original := append([]byte{0xed, 0x40, ' '}, sjis(t, "旧処理\r\n保持\n末尾\r")...)
	doc, err := Decode(original)
	if err != nil {
		t.Fatal(err)
	}
	start := strings.Index(doc.Text, "旧処理")
	after, err := doc.Apply([]Edit{{Start: start, End: start + len("旧処理"), Text: "新処理\n追加"}})
	want := append([]byte{0xed, 0x40, ' '}, sjis(t, "新処理\r\n追加\r\n保持\n末尾\r")...)
	if err != nil || !bytes.Equal(after, want) {
		t.Fatalf("bytes changed unexpectedly: %x want %x err %v", after, want, err)
	}
	if _, err := doc.Apply([]Edit{{Start: start, End: start + len("旧処理"), Text: "😀"}}); err == nil {
		t.Fatal("unrepresentable emoji was accepted")
	}
}

func TestContextualEditsPreserveMixedUnchangedLines(t *testing.T) {
	doc, err := Decode([]byte("old\r\nkeep\nend\r"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := doc.Encode("new\ninsert\nkeep\nfinish\n")
	if err != nil || string(got) != "new\r\ninsert\nkeep\nfinish\r" {
		t.Fatalf("mixed endings: %q %v", got, err)
	}
}

func TestInvalidEncodingAndUnsafeBoundariesRejected(t *testing.T) {
	for _, data := range [][]byte{{0xff}, {0x82}, {0x82, 0x20}, []byte("a\x00b"), {0xef, 0xbb, 0xbf, 0x82, 0xa0}} {
		if _, err := Decode(data); err == nil {
			t.Fatalf("accepted invalid text %x", data)
		}
	}
	doc, err := Decode([]byte("日本語"))
	if err != nil {
		t.Fatal(err)
	}
	for _, edits := range [][]Edit{
		{{Start: 1, End: 3, Text: "a"}},
		{{Start: 0, End: 3, Text: "a"}, {Start: 0, End: 3, Text: "b"}},
		{{Start: 0, End: 3, Text: "a\r\n"}},
	} {
		if _, err := doc.Apply(edits); err == nil {
			t.Fatalf("accepted unsafe edits: %+v", edits)
		}
	}
}

func TestContextEditKeepsRepeatedCP932AndMixedNewlineBytes(t *testing.T) {
	// ED40 and FA5C decode to the same character. Both raw representations,
	// and both differently terminated empty lines, must survive an insertion
	// inside a broader contextual edit.
	original := []byte("old\r\n")
	original = append(original, 0xed, 0x40, '\r', '\n', '\r', '\n')
	original = append(original, 0xfa, 0x5c, '\n', '\n')
	original = append(original, []byte("end\r\n")...)
	doc, err := Decode(original)
	if err != nil {
		t.Fatal(err)
	}
	after := "new\nextra\n纊\n\n纊\n\nfinish\n"
	got, err := doc.Encode(after)
	if err != nil {
		t.Fatal(err)
	}
	expected := []byte("new\r\nextra\r\n")
	expected = append(expected, original[len("old\r\n"):len(original)-len("end\r\n")]...)
	expected = append(expected, []byte("finish\r\n")...)
	if !bytes.Equal(got, expected) {
		t.Fatalf("repeated unchanged raw lines changed: %x want %x", got, expected)
	}
	decoded, err := Decode(got)
	if err != nil || decoded.Text != after {
		t.Fatalf("wrong edited text: %q %v", decoded.Text, err)
	}
}
