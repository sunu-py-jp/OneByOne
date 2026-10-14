package solutionrules

import (
	"encoding/csv"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func testRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func exampleRules() []Rule {
	return []Rule{{ID: "replace-legacy-api", Description: "VB.NETの旧APIを置換する場合に使います。", Content: "# 手順\n\n対象APIを置換し、コンパイルエラーを解消してください。"}, {ID: "fix-type-errors", Description: "型エラーを修正します。", Content: "Option Strict を維持してください。"}}
}

func TestJSONRoundTripAndThreeFieldsOnly(t *testing.T) {
	rules := exampleRules()
	encoded, err := Encode(rules)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules, decoded) {
		t.Fatalf("decoded: %#v", decoded)
	}
	for _, extra := range []string{"include", "exclude", "filter", "name", "version", "references", "metadata"} {
		input := `{"rules":[{"id":"valid-rule","description":"説明","content":"本文","` + extra + `":"value"}]}`
		if _, err := Decode([]byte(input)); err == nil {
			t.Fatalf("accepted %s", extra)
		}
	}
}

func TestJSONRejectsUnknownDuplicateAndMalformedFields(t *testing.T) {
	for _, input := range []string{
		`{"rules":[{"id":"first","id":"second","description":"D","content":"C"}]}`,
		`{"rules":[],"rules":[{"id":"first","description":"D","content":"C"}]}`,
		`{"rules":[{"id":"first","description":"D","Content":"C"}]}`,
		`{"rules":[{"id":"first","description":"D","content":null}]}`,
		`{"rules":[{"id":"first","description":"D","content":7}]}`,
		`{"rules":[{"id":"first","description":"D"}]}`,
		`{"rules":[]}`, `{"rules":null}`, `[]`, `{"rules":[{"id":"first","description":"D","content":"C"}],"version":1}`,
	} {
		if _, err := Decode([]byte(input)); err == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestValidationRejectsUnsafeIDsAndEmptyInstructions(t *testing.T) {
	for _, id := range []string{"../escape", "UPPER", "-start", "end-", "two--dashes", "spaces here", "nul", "com1", strings.Repeat("a", 65), ""} {
		if err := Validate([]Rule{{ID: id, Description: "D", Content: "C"}}); err == nil {
			t.Fatalf("accepted id %q", id)
		}
	}
	for _, rule := range []Rule{{ID: "ok", Description: "", Content: "C"}, {ID: "ok", Description: "D", Content: " "}, {ID: "ok", Description: strings.Repeat("あ", 1025), Content: "C"}} {
		if err := Validate([]Rule{rule}); err == nil {
			t.Fatalf("accepted %#v", rule)
		}
	}
	if err := Validate([]Rule{{ID: "same", Description: "D", Content: "C"}, {ID: "same", Description: "D", Content: "C"}}); err == nil {
		t.Fatal("duplicate ID accepted")
	}
}

func TestSkillsExportImportRoundTrip(t *testing.T) {
	root := testRoot(t)
	destination := filepath.Join(root, "skills")
	rules := exampleRules()
	if err := ExportSkills(destination, rules); err != nil {
		t.Fatal(err)
	}
	for _, rule := range rules {
		path := filepath.Join(destination, rule.ID, "SKILL.md")
		decoded, err := Read(path)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, []Rule{rule}) {
			t.Fatalf("decoded: %#v", decoded)
		}
		decoded, err = Read(filepath.Dir(path))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(decoded, []Rule{rule}) {
			t.Fatalf("decoded folder: %#v", decoded)
		}
	}
	decoded, err := Read(destination)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, []Rule{rules[1], rules[0]}) {
		t.Fatalf("decoded all: %#v", decoded)
	}
}

func TestExportValidatesAllCollisionsBeforeWriting(t *testing.T) {
	root := testRoot(t)
	rules := exampleRules()
	existing := filepath.Join(root, rules[1].ID)
	if err := os.Mkdir(existing, 0755); err != nil {
		t.Fatal(err)
	}
	if err := ExportSkills(root, rules); err == nil {
		t.Fatal("collision accepted")
	}
	if _, err := os.Stat(filepath.Join(root, rules[0].ID)); !os.IsNotExist(err) {
		t.Fatal("export partially written despite known collision")
	}
	if err := ExportSkills(filepath.Join(root, "new"), []Rule{{ID: "../unsafe", Description: "D", Content: "C"}}); err == nil {
		t.Fatal("invalid rule exported")
	}
	if _, err := os.Stat(filepath.Join(root, "new")); !os.IsNotExist(err) {
		t.Fatal("invalid export created folder")
	}
}

func TestReadAndExportRejectSymlinkAncestors(t *testing.T) {
	root := testRoot(t)
	outside := testRoot(t)
	link := filepath.Join(root, "linked")
	if err := os.Symlink(outside, link); err != nil {
		t.Skip(err)
	}
	if err := ExportSkills(filepath.Join(link, "nested"), exampleRules()); err == nil {
		t.Fatal("symlink export accepted")
	}
	data, _ := Encode(exampleRules())
	if err := os.WriteFile(filepath.Join(outside, "rules.json"), data, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(filepath.Join(link, "rules.json")); err == nil {
		t.Fatal("symlink read accepted")
	}
}

func TestCSVMultilineContentAndUnknownHeader(t *testing.T) {
	root := testRoot(t)
	var body strings.Builder
	writer := csv.NewWriter(&body)
	writer.Write([]string{"content", "id", "description"})
	rule := exampleRules()[0]
	writer.Write([]string{rule.Content, rule.ID, rule.Description})
	writer.Flush()
	path := filepath.Join(root, "rules.csv")
	if err := os.WriteFile(path, []byte(body.String()), 0644); err != nil {
		t.Fatal(err)
	}
	rules, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(rules, []Rule{rule}) {
		t.Fatalf("CSV: %#v", rules)
	}
	for _, header := range []string{"id,name,content\nvalid,desc,body\n", "id,id,content\nvalid,desc,body\n"} {
		if _, err := decodeCSV([]byte(header)); err == nil {
			t.Fatalf("accepted %s", header)
		}
	}
}

func TestSkillRejectsUnsupportedFrontmatterAndMismatchedFolder(t *testing.T) {
	for _, header := range []string{"name: valid-rule\ndescription: desc\nfilter: '*.vb'", "name: valid-rule\nname: other-rule\ndescription: desc", "name: valid-rule"} {
		if _, err := decodeSkill([]byte("---\n" + header + "\n---\n\nBody\n")); err == nil {
			t.Fatalf("accepted header %s", header)
		}
	}
	root := testRoot(t)
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: other-rule\ndescription: desc\n---\n\nBody\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Read(root); err == nil {
		t.Fatal("mismatched folder accepted")
	}
}
