package diagnostics

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func TestDecodeNormalizesAndDeduplicatesWithoutLosingEvidence(t *testing.T) {
	data := "\ufeff\r\n" +
		`{"file":".\\src\\Program.cs","line":12,"code":"CS0103","message":"The name x does not exist"}` + "\r\n" +
		`{"file":"src/Program.cs","line":12,"code":"CS0103","message":"The name x does not exist"}` + "\n" +
		`{"file":"src/Program.cs","message":"日本語\n次の行"}` + "\n"
	items, err := Decode([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	want := []model.SourceDiagnostic{
		{File: "src/Program.cs", Line: 12, Code: "CS0103", Message: "The name x does not exist"},
		{File: "src/Program.cs", Message: "日本語\n次の行"},
	}
	if !reflect.DeepEqual(items, want) {
		t.Fatalf("got %#v, want %#v", items, want)
	}
	items[0].RuleIDs = []string{"R001"}
	encoded, err := Encode(items)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("ruleIds")) {
		t.Fatal("internal rule mapping escaped into the interchange format")
	}
	decoded, err := Decode(encoded)
	if err != nil || !reflect.DeepEqual(decoded, want) {
		t.Fatalf("round trip: %#v, %v", decoded, err)
	}
}

func TestDecodeRejectsAmbiguousOrUnsafeRecords(t *testing.T) {
	cases := []string{
		``, ` `, `null`, `[]`, `"text"`, `{}`, `{"file":"a"}`, `{"message":"x"}`,
		`{"file":"a","message":""}`, `{"file":"a","message":"  "}`,
		`{"file":null,"message":"x"}`, `{"file":"a","message":null}`,
		`{"file":"a","message":"x","line":null}`, `{"file":"a","message":"x","code":null}`,
		`{"file":12,"message":"x"}`, `{"file":"a","message":{}}`,
		`{"file":"a","message":"x","code":123}`, `{"file":"a","message":"x","line":"2"}`,
		`{"file":"a","message":"x","line":0}`, `{"file":"a","message":"x","line":-1}`,
		`{"file":"a","message":"x","line":1.2}`, `{"file":"a","message":"x","line":2e1}`,
		`{"file":"a","message":"x","line":99999999999999999999999999}`,
		`{"file":"a","file":"b","message":"x"}`,
		`{"file":"a","message":"x","message":"y"}`,
		`{"file":"a","message":"x","line":1,"line":2}`,
		`{"file":"a","message":"x","severity":"error"}`,
		`{"file":"a","message":"x","ruleIds":["R001"]}`,
		`{"File":"a","message":"x"}`,
		`{"file":"a","message":"x"} {"file":"b","message":"y"}`,
		`{"file":"a","message":"x",}`, `{"file":"a","message":"x"`,
		`{"file":"../a","message":"x"}`, `{"file":"x/../a","message":"x"}`,
		`{"file":"x\\..\\a","message":"x"}`, `{"file":"/a","message":"x"}`,
		`{"file":"C:\\a","message":"x"}`, `{"file":"C:a","message":"x"}`,
		`{"file":"\\\\server\\a","message":"x"}`, `{"file":"a:stream","message":"x"}`,
		`{"file":".","message":"x"}`, `{"file":"a/","message":"x"}`,
		`{"file":"a\u0000b","message":"x"}`, `{"file":"a","message":"x\u0000y"}`,
		"{\n\"file\":\"a\",\"message\":\"x\"\n}",
		`{"file":"a","message":"x"}` + "\n\ufeff" + `{"file":"b","message":"y"}`,
		"{\"file\":\"a\",\"message\":\"\xff\"}",
	}
	for _, data := range cases {
		t.Run(data, func(t *testing.T) {
			if items, err := Decode([]byte(data)); err == nil {
				t.Fatalf("accepted invalid JSONL: %#v", items)
			}
		})
	}
}

func TestDecodeReportsPhysicalLineAndRejectsWholeImport(t *testing.T) {
	data := "\n" + `{"file":"a","message":"x"}` + "\n" + `{"file":"b","message":"y","line":0}`
	items, err := Decode([]byte(data))
	if err == nil || !strings.Contains(err.Error(), "3行") || items != nil {
		t.Fatalf("expected atomic rejection at physical line 3: %#v %v", items, err)
	}
}

func TestReadRejectsDirectorySymlinkAndOversizedFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "input.jsonl")
	if err := os.WriteFile(file, []byte(`{"file":"a","message":"x"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if items, err := Read(file); err != nil || len(items) != 1 {
		t.Fatalf("valid file: %#v %v", items, err)
	}
	if _, err := Read(dir); err == nil {
		t.Fatal("accepted a directory")
	}
	link := filepath.Join(dir, "link.jsonl")
	if err := os.Symlink(file, link); err == nil {
		if _, err = Read(link); err == nil {
			t.Fatal("accepted a symlink")
		}
	}
	if err := os.Truncate(file, MaxBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(file); err == nil {
		t.Fatal("accepted an oversized file")
	}
	if _, err := Decode(make([]byte, MaxBytes+1)); err == nil {
		t.Fatal("accepted oversized input")
	}
}
