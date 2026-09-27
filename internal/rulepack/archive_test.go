package rulepack

import (
	"bytes"
	"encoding/json"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testRule(t *testing.T, id string) Entry {
	t.Helper()
	b, e := ruleformat.Encode(model.RuleDefinition{ID: id, Name: "Rule " + id, Description: "説明", ContentPattern: `\bSave\b`, Body: "# 任意の見出し\n\n日本語\n"})
	if e != nil {
		t.Fatal(e)
	}
	return Entry{ID: id, Markdown: string(b)}
}
func testPackage(t *testing.T) *Package {
	t.Helper()
	return &Package{Rules: []Entry{testRule(t, "1"), testRule(t, "2")}}
}
func TestJSONRoundTripKeepsFrontMatterIDs(t *testing.T) {
	p := testPackage(t)
	dir := t.TempDir()
	if e := Extract(p, dir); e != nil {
		t.Fatal(e)
	}
	if entries, e := os.ReadDir(dir); e != nil || len(entries) != 1 || entries[0].Name() != "rules.json" {
		t.Fatal("a version must contain only rules.json")
	}
	got, e := Snapshot(filepath.Join(dir, "rules.json"))
	if e != nil || !reflect.DeepEqual(p, got) {
		t.Fatalf("snapshot: %+v %v", got, e)
	}
	data, e := Encode(p)
	if e != nil {
		t.Fatal(e)
	}
	var raw map[string]json.RawMessage
	if e = json.Unmarshal(data, &raw); e != nil || len(raw) != 1 {
		t.Fatal("public format must only contain rules")
	}
	imported, e := Decode(data)
	if e != nil || !reflect.DeepEqual(p, imported) {
		t.Fatalf("import must keep ids and exact markdown: %+v %v", imported, e)
	}
}
func TestJSONRejectsInvalidDocuments(t *testing.T) {
	dup := testRule(t, "1").Markdown
	upper := strings.Replace(testRule(t, "r1").Markdown, "id: r1", "id: R1", 1)
	quoted, _ := json.Marshal([]string{dup, dup})
	caseDup, _ := json.Marshal([]string{testRule(t, "r1").Markdown, upper})
	for _, v := range []string{`[]`, `{}`, `{"rules":null}`, `{"rules":[null]}`, `{"rules":[{}]}`, `{"rules":[],"rules":[]}`, `{"rules":[],"settings":{}}`, `{"rules":[]} {}`, `{"rules":["no frontmatter"]}`, `{"rules":["---\nname: x\ndescription: y\n---\n"]}`, `{"rules":` + string(quoted) + `}`, `{"rules":` + string(caseDup) + `}`} {
		if _, e := Decode([]byte(v)); e == nil {
			t.Fatalf("accepted %s", v)
		}
	}
	if _, e := Decode(bytes.Repeat([]byte("x"), int(MaxExpandedBytes)+1)); e == nil {
		t.Fatal("accepted oversize")
	}
}
func TestSnapshotIdentifiesInvalidRulesButRequiresIDs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rules.json")
	data, _ := json.Marshal(map[string][]string{"rules": {testRule(t, "1").Markdown, "---\nid: 2\nname: ''\n---\n"}})
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	p, e := Snapshot(path)
	if e != nil || len(p.Rules) != 2 || p.Rules[1].ID != "2" {
		t.Fatalf("an invalid rule with an id must remain identifiable: %+v %v", p, e)
	}
	if Validate(p) == nil {
		t.Fatal("snapshot hid the invalid rule")
	}
	data, _ = json.Marshal(map[string][]string{"rules": {"---\nname: x\n---\n"}})
	if e = os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = Snapshot(path); e == nil {
		t.Fatal("accepted a stored rule without an id")
	}
}
func TestWithIDRewritesOnlyTheFrontMatterID(t *testing.T) {
	e, err := WithID(testRule(t, "1"), "1_2")
	if err != nil || e.ID != "1_2" || !strings.HasSuffix(e.Markdown, "# 任意の見出し\n\n日本語\n") {
		t.Fatalf("renamed: %+v %v", e, err)
	}
	// YAML would read an unquoted 1_2 as the integer 12, so the text stays quoted.
	if d, err := ruleformat.Decode([]byte(e.Markdown)); err != nil || d.ID != "1_2" || !strings.Contains(e.Markdown, "\nid: \"1_2\"\n") {
		t.Fatalf("renamed front matter: %+v %v", d, err)
	}
}
func TestCSVBuildsMarkdownWithQuotedAndMultilineCells(t *testing.T) {
	csv := "\ufeffid,name,description,path_pattern,content_pattern,変更概要,変換前\r\n1,Rule,説明,src/**/*.ts,\\bSave\\b,\"line 1\nline 2, quoted\",\"say(\"\"hello\"\");\"\r\n"
	p, e := DecodeCSV([]byte(csv))
	if e != nil {
		t.Fatal(e)
	}
	d, e := ruleformat.Decode([]byte(p.Rules[0].Markdown))
	if e != nil || p.Rules[0].ID != "1" || d.ID != "1" || d.ContentPattern != `\bSave\b` || d.Body != "# 変更概要\n\nline 1\nline 2, quoted\n\n# 変換前\n\nsay(\"hello\");" {
		t.Fatalf("csv: %+v %v", d, e)
	}
	for _, bad := range []string{"name,description,path_pattern,content_pattern\nRule,info,,", "id,name,description\n1,Rule,info", "id,name,description,path_pattern,content_pattern,name\n1,Rule,info,,,Rule", "id,name,description,path_pattern,content_pattern\n1,Rule, , , ", "id,name,description,path_pattern,content_pattern\n,Rule,info,,", "id,name,description,path_pattern,content_pattern\nR 1,Rule,info,,", "id,name,description,path_pattern,content_pattern\n1,A,info,,\n1,B,info,,"} {
		if _, e := DecodeCSV([]byte(bad)); e == nil {
			t.Fatalf("accepted invalid csv %q", bad)
		}
	}
}
func TestCSVSkipsBlankRowsAndEmptySections(t *testing.T) {
	csv := "content_pattern,name,description,path_pattern,id,補足,変更概要\n,Rule,説明,,7,,概要\n,,,,,,\n , , , , , , \n"
	p, e := DecodeCSV([]byte(csv))
	if e != nil || len(p.Rules) != 1 {
		t.Fatalf("blank spreadsheet rows were not skipped: %+v %v", p, e)
	}
	d, e := ruleformat.Decode([]byte(p.Rules[0].Markdown))
	if e != nil || d.ID != "7" || d.Name != "Rule" || d.Description != "説明" || d.PathPattern != "" || d.Body != "# 変更概要\n\n概要" {
		t.Fatalf("csv: %+v %v", d, e)
	}
}
func TestLinksRejected(t *testing.T) {
	dir := t.TempDir()
	if e := Extract(testPackage(t), dir); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if e := os.Symlink(filepath.Join(dir, "rules.json"), link); e != nil {
		t.Skip("symlinks unavailable")
	}
	if _, e := Read(link); e == nil {
		t.Fatal("followed symlink")
	}
}
func TestJSONExportContainsNoMetadataOrSecrets(t *testing.T) {
	p := testPackage(t)
	path := filepath.Join(t.TempDir(), "rules.json")
	if e := Write(path, p); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(path)
	if e != nil || !strings.HasPrefix(string(b), "{\n  \"rules\": [") || !strings.Contains(string(b), `"---\nid: 1\nname: Rule 1\n`) {
		t.Fatalf("export: %s %v", b, e)
	}
	if e := Write(path+".oborules", p); e == nil {
		t.Fatal("old extension accepted")
	}
}
