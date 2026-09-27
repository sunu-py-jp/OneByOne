package ruleformat

import (
	"onebyone/internal/model"
	"reflect"
	"strings"
	"testing"
)

func fixtureDefinition() model.RuleDefinition {
	return model.RuleDefinition{ID: "1", Name: "API更新", Description: "呼出し順と契約を保つ。", PathPattern: "src/**/*.ts", ContentPattern: `\bSave\(`, Body: "自由な説明\n\n# 独自セクション\n\n```ts\nSave();\n```\n"}
}
func TestMarkdownRoundTripPreservesBodyAndPattern(t *testing.T) {
	d := fixtureDefinition()
	b, e := Encode(d)
	if e != nil {
		t.Fatal(e)
	}
	got, e := Decode(b)
	if e != nil || !reflect.DeepEqual(d, got) {
		t.Fatalf("round trip: %+v %v", got, e)
	}
	if !strings.Contains(Markdown(d), d.Description) || !strings.Contains(Markdown(d), d.Body) {
		t.Fatal("LLM did not receive description/body")
	}
	for _, change := range []func(*model.RuleDefinition){func(v *model.RuleDefinition) { v.ID += "0" }, func(v *model.RuleDefinition) { v.Name += "x" }, func(v *model.RuleDefinition) { v.Description += "x" }, func(v *model.RuleDefinition) { v.PathPattern += "x" }, func(v *model.RuleDefinition) { v.ContentPattern += "x" }, func(v *model.RuleDefinition) { v.Body += "x" }} {
		next := d
		change(&next)
		if Revision(d) == Revision(next) {
			t.Fatal("revision missed field")
		}
	}
}
func TestEncodedFrontMatterStartsWithReadableOneLineID(t *testing.T) {
	d := fixtureDefinition()
	d.Description = strings.Repeat("keep the calling contract ", 12)
	d.PathPattern = "*.tsx"
	b, e := Encode(d)
	if e != nil {
		t.Fatal(e)
	}
	lines := strings.Split(string(b), "\n")
	if lines[1] != "id: 1" || !strings.HasPrefix(lines[2], "name: ") || !strings.HasPrefix(lines[3], "description: ") || !strings.HasPrefix(lines[4], "path_pattern: ") || !strings.HasPrefix(lines[5], "content_pattern: ") || lines[6] != "---" {
		t.Fatalf("front matter order or line folding changed:\n%s", b)
	}
	for _, id := range []string{"001", "R019", "api-save_2", "0", "08", "1e3"} {
		d.ID = id
		if b, e = Encode(d); e != nil {
			t.Fatal(e)
		}
		got, e := Decode(b)
		if e != nil || got.ID != id || got.Description != strings.TrimSpace(d.Description) || got.PathPattern != d.PathPattern {
			t.Fatalf("id %q did not round trip: %+v %v\n%s", id, got, e, b)
		}
	}
}
func TestUnquotedNumericIDsKeepTheirLiteralText(t *testing.T) {
	for raw, want := range map[string]string{"1": "1", "001": "001", "'007'": "007", "R001": "R001", "0x1F": "0x1F"} {
		d, err := Decode([]byte("---\nid: " + raw + "\nname: Rule\ndescription: Explain\n---\nbody"))
		if err != nil || d.ID != want {
			t.Fatalf("id %s: %+v %v", raw, d, err)
		}
	}
	for _, bad := range []string{"", "~", "[1]", "'R 1'", "'-1'", "ルール1", "1.5"} {
		if _, err := Decode([]byte("---\nid: " + bad + "\nname: Rule\ndescription: Explain\n---\n")); err == nil {
			t.Fatalf("accepted id %q", bad)
		}
	}
	if _, err := Decode([]byte("---\nname: Rule\ndescription: Explain\n---\n")); err == nil || !strings.Contains(err.Error(), "id を入力") {
		t.Fatalf("missing id was not reported: %v", err)
	}
}
func TestPeekIDIdentifiesAnOtherwiseInvalidStoredRule(t *testing.T) {
	id, err := PeekID([]byte("---\nid: 12\nname: ''\nunknown: x\n---\n"))
	if err != nil || id != "12" {
		t.Fatalf("lenient id: %q %v", id, err)
	}
	for _, bad := range []string{"plain markdown", "---\nname: x\n---\n", "---\nid: [1]\n---\n"} {
		if _, err := PeekID([]byte(bad)); err == nil {
			t.Fatalf("identified %q", bad)
		}
	}
}
func TestInvalidFrontMatterRejected(t *testing.T) {
	for _, bad := range []string{"plain markdown", "---\nid: 1\nname: x\n---\n", "---\nid: 1\nname: x\ndescription: ' '\n---\n", "---\nid: 1\nname: x\nname: y\ndescription: text\n---\n", "---\nid: 1\nname: x\ndescription: text\nunknown: x\n---\n", "---\nid: 1\nname: [x]\ndescription: text\n---\n", "---\nid: 1\nname: x\ndescription: text\ncontent_pattern: 42\n---\n", "---\nid: 1\nname: x\ndescription: text\n"} {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatalf("accepted: %q", bad)
		}
	}
}
func TestEmptyPatternsAndLiteralRegex(t *testing.T) {
	d, err := Decode([]byte("---\nid: 1\nname: Rule\ndescription: Explain\npath_pattern:\ncontent_pattern: '\\bSave\\b'\n---\n\n# Body\n"))
	if err != nil || d.ContentPattern != `\bSave\b` {
		t.Fatalf("%+v %v", d, err)
	}
	d.ContentPattern = ""
	if !ToRule("id", d).Always {
		t.Fatal("empty should be common")
	}
	d.PathPattern = "src/**"
	if ToRule("id", d).Always {
		t.Fatal("path scoped is not common")
	}
	d.ContentPattern = "x\ny"
	if Validate(d) == nil {
		t.Fatal("multiline pattern accepted")
	}
}
