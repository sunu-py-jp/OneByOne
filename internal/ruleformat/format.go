// Package ruleformat defines the single persisted rule schema and the Markdown
// representation sent to the model. UI and stored data remain structured.
package ruleformat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
)

const Version = 1
const MaxBytes = 4 << 20

var fields = map[string]bool{"version": true, "name": true, "overview": true, "before": true, "after": true, "notes": true, "holdConditions": true, "pattern": true}

func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))
	if !utf8.ValidString(name) || name == "" || utf8.RuneCountInString(name) > 200 || strings.ContainsAny(name, "\r\n\x00\u0085\u2028\u2029") {
		return "", fmt.Errorf("ルール名は改行やNULを含まない1〜200文字のUTF-8テキストにしてください")
	}
	return name, nil
}

func Validate(d model.RuleDefinition) error {
	if d.Version != Version {
		return fmt.Errorf("rule.json のversionは1で指定してください")
	}
	if _, err := NormalizeName(d.Name); err != nil {
		return err
	}
	for _, text := range []string{d.Name, d.Overview, d.Before, d.After, d.Notes, d.HoldConditions, d.Pattern} {
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
			return fmt.Errorf("ルールの各項目はNULを含まないUTF-8テキストにしてください")
		}
	}
	if strings.ContainsAny(d.Pattern, "\r\n\u0085\u2028\u2029") {
		return fmt.Errorf("適用パターンは改行を含まない1行で入力してください")
	}
	data, err := json.Marshal(d)
	if err != nil || len(data) > MaxBytes {
		return fmt.Errorf("rule.json は4MiB以内にしてください")
	}
	return nil
}

func Decode(data []byte) (model.RuleDefinition, error) {
	var d model.RuleDefinition
	if len(data) > MaxBytes || !utf8.Valid(data) {
		return d, fmt.Errorf("rule.json は4MiB以内のUTF-8で指定してください")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return d, fmt.Errorf("rule.json はJSONオブジェクトで指定してください")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return d, fmt.Errorf("rule.json のキーを読み込めません")
		}
		key, ok := token.(string)
		if !ok || !fields[key] || seen[key] {
			return d, fmt.Errorf("rule.json に未定義・重複のキーがあります: %s", key)
		}
		seen[key] = true
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return d, fmt.Errorf("rule.json の%sはnull以外の値で指定してください", key)
		}
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return d, fmt.Errorf("rule.json を読み込めません")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return d, fmt.Errorf("rule.json に余分な内容があります")
	}
	for key := range fields {
		if !seen[key] {
			return d, fmt.Errorf("rule.json の%sは必須です", key)
		}
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return model.RuleDefinition{}, fmt.Errorf("rule.json の項目の型が不正です")
	}
	if err := Validate(d); err != nil {
		return model.RuleDefinition{}, err
	}
	d.Name, _ = NormalizeName(d.Name)
	return d, nil
}

func Encode(d model.RuleDefinition) ([]byte, error) {
	if err := Validate(d); err != nil {
		return nil, err
	}
	d.Name, _ = NormalizeName(d.Name)
	data, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data)+1 > MaxBytes {
		return nil, fmt.Errorf("rule.json は4MiB以内にしてください")
	}
	return append(data, '\n'), nil
}

func Revision(d model.RuleDefinition) string {
	if name, err := NormalizeName(d.Name); err == nil {
		d.Name = name
	}
	data, _ := json.Marshal(d)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func ToRule(id string, d model.RuleDefinition) model.Rule {
	summary := []rune(strings.Join(strings.Fields(d.Overview), " "))
	if len(summary) > 240 {
		summary = append(summary[:240], '…')
	}
	name, _ := NormalizeName(d.Name)
	return model.Rule{ID: id, Title: name, Summary: string(summary), Overview: d.Overview, Before: d.Before, After: d.After, Notes: d.Notes, HoldConditions: d.HoldConditions, Pattern: d.Pattern, Always: strings.TrimSpace(d.Pattern) == ""}
}

func fenced(code string) string {
	longest, current := 0, 0
	for _, r := range code {
		if r == '`' {
			current++
			if current > longest {
				longest = current
			}
		} else {
			current = 0
		}
	}
	length := max(3, longest+1)
	fence := strings.Repeat("`", length)
	suffix := "\n"
	if strings.HasSuffix(code, "\n") {
		suffix = ""
	}
	return fence + "\n" + code + suffix + fence
}

func Markdown(d model.RuleDefinition) string {
	return "# 変更概要\n\n" + d.Overview + "\n\n# 変更前\n\n" + fenced(d.Before) + "\n\n# 変更後\n\n" + fenced(d.After) + "\n\n# 備考\n\n" + d.Notes + "\n\n# 修正を保留すべきケース\n\n" + d.HoldConditions + "\n"
}
