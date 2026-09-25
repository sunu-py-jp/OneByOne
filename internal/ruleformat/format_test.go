package ruleformat

import (
	"bytes"
	"encoding/json"
	"onebyone/internal/model"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"
)

func fixtureDefinition() model.RuleDefinition {
	return model.RuleDefinition{Version: 1, Name: "保存APIの更新", Overview: "Legacy.Save を更新する。\n順序は保つ。", Before: "const before = `raw`;\n```nested\nvalue\n```\n", After: "updated();\r\n", Notes: "備考を保持", HoldConditions: "型を特定できない場合", Pattern: `Legacy\.Save`}
}

func TestStructuredRoundTripAndAIOnlyMarkdownPreserveIndependentFields(t *testing.T) {
	d := fixtureDefinition()
	data, err := Encode(d)
	if err != nil {
		t.Fatal(err)
	}
	got, err := Decode(data)
	if err != nil || !reflect.DeepEqual(got, d) {
		t.Fatalf("independent fields changed: %+v %v", got, err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw) != 8 || raw["before"] != d.Before || raw["after"] != d.After {
		t.Fatal("stored JSON did not keep the original code fields")
	}
	markdown := Markdown(d)
	for _, section := range []string{"# 変更概要\n", "# 変更前\n", "# 変更後\n", "# 備考\n", "# 修正を保留すべきケース\n"} {
		if strings.Count(markdown, section) != 1 {
			t.Fatalf("missing or repeated generated section: %s", section)
		}
	}
	if !strings.Contains(markdown, "````\n"+d.Before+"````") || !strings.Contains(markdown, "```\n"+d.After+"```") {
		t.Fatal("code fences did not preserve original newlines and nested backticks")
	}
	if strings.Contains(markdown, d.Pattern) || strings.Contains(markdown, d.Name) || strings.Contains(markdown, `"version"`) {
		t.Fatal("AI Markdown includes pattern, title metadata, or persisted JSON")
	}
	for _, change := range []func(*model.RuleDefinition){func(d *model.RuleDefinition) { d.Name += "changed" }, func(d *model.RuleDefinition) { d.Overview += "changed" }, func(d *model.RuleDefinition) { d.Before += "changed" }, func(d *model.RuleDefinition) { d.After += "changed" }, func(d *model.RuleDefinition) { d.Notes += "changed" }, func(d *model.RuleDefinition) { d.HoldConditions += "changed" }, func(d *model.RuleDefinition) { d.Pattern += "changed" }} {
		changed := d
		change(&changed)
		if Revision(d) == Revision(changed) {
			t.Fatal("revision omitted an independently editable field")
		}
	}
	empty := model.RuleDefinition{Version: 1, Name: "Empty sections"}
	if _, err := Encode(empty); err != nil {
		t.Fatalf("empty five sections are valid: %v", err)
	}
}

func TestDecodeRejectsMissingUnknownDuplicateNullAndWrongTypedFields(t *testing.T) {
	data, _ := Encode(fixtureDefinition())
	for key := range fields {
		t.Run("missing-"+key, func(t *testing.T) {
			var value map[string]json.RawMessage
			_ = json.Unmarshal(data, &value)
			delete(value, key)
			bad, _ := json.Marshal(value)
			if _, err := Decode(bad); err == nil {
				t.Fatal("missing required field accepted")
			}
		})
	}
	invalid := []string{
		strings.Replace(string(data), `"version": 1`, `"version": 2`, 1),
		strings.Replace(string(data), `"version": 1`, `"version": 1, "version": 1`, 1),
		strings.Replace(string(data), `"version": 1`, `"Version": 1`, 1),
		strings.Replace(string(data), `"notes": "備考を保持"`, `"notes": null`, 1),
		strings.Replace(string(data), `"version": 1`, `"version": "1"`, 1),
		strings.Replace(string(data), `"version": 1`, `"version": 1, "markdown":"old format"`, 1),
		string(data) + `{}`, `null`, `[]`, strings.Replace(string(data), `"pattern": "Legacy\\.Save"`, `"pattern": "first\nsecond"`, 1),
	}
	for i, bad := range invalid {
		if _, err := Decode([]byte(bad)); err == nil {
			t.Fatalf("invalid JSON case %d accepted", i)
		}
	}
	if _, err := Decode(bytes.Repeat([]byte("x"), MaxBytes+1)); err == nil {
		t.Fatal("oversize input accepted")
	}
}

func TestValidationSingleLinePatternsAndBoundedOverviewIndex(t *testing.T) {
	for _, separator := range []string{"\n", "\r", "\u0085", "\u2028", "\u2029"} {
		d := fixtureDefinition()
		d.Pattern = "one" + separator + "two"
		if err := Validate(d); err == nil {
			t.Fatal("multiline pattern accepted")
		}
	}
	d := fixtureDefinition()
	d.Notes = "invalid\x00"
	if err := Validate(d); err == nil {
		t.Fatal("NUL content accepted")
	}
	d = fixtureDefinition()
	d.After = string([]byte{0xff})
	if err := Validate(d); err == nil {
		t.Fatal("non-UTF8 content accepted")
	}
	d = fixtureDefinition()
	d.Overview = strings.Repeat("日", 3000)
	rule := ToRule("R019", d)
	if rule.Overview != d.Overview || !utf8.ValidString(rule.Summary) || utf8.RuneCountInString(rule.Summary) != 241 || !strings.HasSuffix(rule.Summary, "…") {
		t.Fatal("overview was lost or the index summary is not bounded by Unicode characters")
	}
	if rule.Title != d.Name || rule.ID != "R019" || rule.Before != d.Before || rule.After != d.After || rule.Always {
		t.Fatal("structured rule view lost fields")
	}
	d.Pattern = " \t"
	if !ToRule("R019", d).Always {
		t.Fatal("blank pattern is not common")
	}
	d = fixtureDefinition()
	d.Overview = strings.Repeat("x", MaxBytes)
	if _, err := Encode(d); err == nil {
		t.Fatal("oversize encoded definition accepted")
	}
}
