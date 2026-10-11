package rulepack

import (
	"bytes"
	"encoding/csv"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestCSVTemplateIsEmptyAndExcelReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "import-template.CSV")
	if err := WriteCSVTemplate(path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "\ufeffid,name,description,path_pattern,content_pattern,diagnostic_pattern,変更概要,変換前,変換後,補足,変換を保留にすべきケース,修正後に残っていてはいけないパターン\r\n"
	if string(data) != want {
		t.Fatalf("template must contain the UTF-8 BOM and one CRLF-terminated header row: %q", data)
	}
	p, err := DecodeCSV(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rules) != 0 {
		t.Fatalf("unfilled template imported %d rules", len(p.Rules))
	}
}

func TestFilledCSVTemplateImportsMetadataAndJapaneseSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "import-template.csv")
	if err := WriteCSVTemplate(path); err != nil {
		t.Fatal(err)
	}
	template, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	headers, err := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(template, utf8BOM))).Read()
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{
		"id":                 "R_1",
		"name":               "保存処理の置換",
		"description":        "保存処理を新しいAPIへ変換する",
		"path_pattern":       "src/**/*.ts",
		"content_pattern":    `\bSave\b`,
		"diagnostic_pattern": `^CS1061\b`,
		"変更概要":               "旧APIを変更する, 引数は維持する\n日本語の2行目",
		"変換前":                `Save("変更前, 本文");`,
		"変換後":                `Store("変更後, 本文");`,
		"補足":                 "引用符\"を含む補足\n複数行を維持する",
		"変換を保留にすべきケース":       "引数の型を特定できない場合",
		"修正後に残っていてはいけないパターン": `Save("`,
	}
	row := make([]string, len(headers))
	for i, h := range headers {
		var exists bool
		row[i], exists = values[h]
		if !exists {
			t.Fatalf("unexpected template header %q", h)
		}
	}
	var filled bytes.Buffer
	filled.Write(template)
	w := csv.NewWriter(&filled)
	w.UseCRLF = true
	if err := w.Write(row); err != nil {
		t.Fatal(err)
	}
	w.Flush()
	if err := w.Error(); err != nil {
		t.Fatal(err)
	}
	p, err := DecodeCSV(filled.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rules) != 1 || p.Rules[0].ID != "R_1" {
		t.Fatalf("filled template imported unexpected rules: %+v", p.Rules)
	}
	d, err := ruleformat.Decode([]byte(p.Rules[0].Markdown))
	if err != nil {
		t.Fatal(err)
	}
	want := model.RuleDefinition{
		ID: "R_1", Name: "保存処理の置換", Description: "保存処理を新しいAPIへ変換する",
		PathPattern: "src/**/*.ts", ContentPattern: `\bSave\b`, DiagnosticPattern: `^CS1061\b`,
		Body: strings.Join([]string{
			"# 変更概要\n\n旧APIを変更する, 引数は維持する\n日本語の2行目",
			"# 変換前\n\nSave(\"変更前, 本文\");",
			"# 変換後\n\nStore(\"変更後, 本文\");",
			"# 補足\n\n引用符\"を含む補足\n複数行を維持する",
			"# 変換を保留にすべきケース\n\n引数の型を特定できない場合",
			"# 修正後に残っていてはいけないパターン\n\nSave(\"",
		}, "\n\n"),
	}
	if !reflect.DeepEqual(d, want) {
		t.Fatalf("filled template changed metadata or body:\ngot  %+v\nwant %+v", d, want)
	}
}

func TestCSVTemplateRejectsInvalidDestinations(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"template", "template.json", "template.xlsx"} {
		path := filepath.Join(dir, name)
		if err := WriteCSVTemplate(path); err == nil {
			t.Fatalf("accepted non-CSV destination %q", name)
		}
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("invalid destination was created: %s: %v", path, err)
		}
	}
	directory := filepath.Join(dir, "directory.csv")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := WriteCSVTemplate(directory); err == nil {
		t.Fatal("accepted a directory as the destination")
	}
	if info, err := os.Stat(directory); err != nil || !info.IsDir() {
		t.Fatalf("destination directory was changed: %v", err)
	}
}

func TestCSVTemplateRejectsSymlinkDestinations(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target.csv")
	before := []byte("keep this file")
	if err := os.WriteFile(target, before, 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.csv")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	if err := WriteCSVTemplate(link); err == nil {
		t.Fatal("accepted a symlink as the destination")
	}
	if data, err := os.ReadFile(target); err != nil || !bytes.Equal(data, before) {
		t.Fatalf("symlink target was changed: %q %v", data, err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("destination symlink was changed: %v", err)
	}
	parentLink := filepath.Join(t.TempDir(), "parent")
	if err := os.Symlink(dir, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := WriteCSVTemplate(filepath.Join(parentLink, "new.csv")); err == nil {
		t.Fatal("accepted a symlink in the destination parent")
	}
	if _, err := os.Lstat(filepath.Join(dir, "new.csv")); !os.IsNotExist(err) {
		t.Fatalf("created a file through a symlink parent: %v", err)
	}
}

func TestCSVTemplateCanReplaceAnExistingRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "template.csv")
	if err := os.WriteFile(path, []byte("old template contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := WriteCSVTemplate(path); err != nil {
		t.Fatal(err)
	}
	p, err := Read(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rules) != 0 {
		t.Fatalf("replacement template imported %d rules", len(p.Rules))
	}
}
