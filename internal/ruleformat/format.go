// Package ruleformat parses Markdown rules with YAML front matter.
package ruleformat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"

	"onebyone/internal/model"
)

const MaxBytes = 4 << 20

// Rule IDs are also lock-file names, commit trailers and LLM tool arguments.
var (
	validID   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	decimalID = regexp.MustCompile(`^(0|[1-9][0-9]*)$`)
)

var frontMatterKeys = []string{"id", "name", "description", "path_pattern", "content_pattern"}

const invalidIDMessage = "id は英数字で始まる64文字以内の英数字・ハイフン・アンダースコアで入力してください"

func ValidID(id string) bool { return validID.MatchString(id) }

func NormalizeID(id string) (string, error) {
	id = strings.TrimSpace(strings.TrimPrefix(id, "\ufeff"))
	if id == "" {
		return "", fmt.Errorf("id を入力してください")
	}
	if !validID.MatchString(id) {
		return "", fmt.Errorf(invalidIDMessage)
	}
	return id, nil
}

func NormalizeName(name string) (string, error) {
	name = strings.TrimSpace(strings.TrimPrefix(name, "\ufeff"))
	if name == "" {
		return "", fmt.Errorf("name を入力してください")
	}
	if !utf8.ValidString(name) || utf8.RuneCountInString(name) > 200 || strings.ContainsAny(name, "\r\n\x00\u0085  ") {
		return "", fmt.Errorf("name は改行やNULを含まない1〜200文字で入力してください")
	}
	return name, nil
}
func Validate(d model.RuleDefinition) error {
	if _, err := NormalizeID(d.ID); err != nil {
		return err
	}
	if _, err := NormalizeName(d.Name); err != nil {
		return err
	}
	if strings.TrimSpace(d.Description) == "" {
		return fmt.Errorf("description を入力してください")
	}
	for _, v := range []string{d.Name, d.Description, d.PathPattern, d.ContentPattern, d.Body} {
		if !utf8.ValidString(v) || strings.ContainsRune(v, 0) {
			return fmt.Errorf("ルールはNULを含まないUTF-8で入力してください")
		}
	}
	for _, v := range []string{d.PathPattern, d.ContentPattern} {
		if strings.ContainsAny(v, "\r\n\u0085  ") {
			return fmt.Errorf("適用パターンは1行で入力してください")
		}
	}
	data, _ := json.Marshal(d)
	if len(data) > MaxBytes {
		return fmt.Errorf("ルールは4MiB以内にしてください")
	}
	return nil
}

// parse splits the front matter from the body. A lenient parse ignores
// unknown or repeated keys so a stored rule can still be identified.
func parse(data []byte, strict bool) (map[string]*yaml.Node, string, error) {
	if len(data) > MaxBytes || !utf8.Valid(data) {
		return nil, "", fmt.Errorf("ルールは4MiB以内のUTF-8で指定してください")
	}
	text := strings.TrimPrefix(string(data), "\ufeff")
	lines := strings.SplitAfter(text, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], "\r\n") != "---" {
		return nil, "", fmt.Errorf("ルールの先頭に --- で囲むfront matterが必要です")
	}
	end := -1
	for i := 1; i < len(lines); i++ {
		if strings.TrimRight(lines[i], "\r\n") == "---" {
			end = i
			break
		}
	}
	if end < 0 {
		return nil, "", fmt.Errorf("front matter の終端 --- がありません")
	}
	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(lines[1:end], "")))
	var node yaml.Node
	if err := decoder.Decode(&node); err != nil {
		return nil, "", fmt.Errorf("front matter を読み込めません: %w", err)
	}
	if len(node.Content) != 1 || node.Content[0].Kind != yaml.MappingNode {
		return nil, "", fmt.Errorf("front matter はid・name・description・path_pattern・content_patternの項目で指定してください")
	}
	known := map[string]bool{}
	for _, key := range frontMatterKeys {
		known[key] = true
	}
	fields := map[string]*yaml.Node{}
	mapping := node.Content[0]
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		k, v := mapping.Content[i], mapping.Content[i+1]
		if !known[k.Value] || fields[k.Value] != nil {
			if strict {
				return nil, "", fmt.Errorf("front matter に未定義または重複した項目があります: %s", k.Value)
			}
			continue
		}
		fields[k.Value] = v
	}
	if err := decoder.Decode(new(any)); strict && err != io.EOF {
		return nil, "", fmt.Errorf("front matter に余分な文書があります")
	}
	body := strings.Join(lines[end+1:], "")
	body = strings.TrimPrefix(body, "\r\n")
	body = strings.TrimPrefix(body, "\n")
	return fields, body, nil
}

// An unquoted `id: 1` is a YAML integer; the ID is always its literal text.
func idValue(v *yaml.Node) (string, error) {
	if v == nil || (v.Kind == yaml.ScalarNode && v.Tag == "!!null") {
		return "", fmt.Errorf("id を入力してください")
	}
	if v.Kind != yaml.ScalarNode {
		return "", fmt.Errorf(invalidIDMessage)
	}
	return NormalizeID(v.Value)
}

// PeekID identifies a stored rule even when its other fields are invalid.
func PeekID(data []byte) (string, error) {
	fields, _, err := parse(data, false)
	if err != nil {
		return "", err
	}
	return idValue(fields["id"])
}

func Decode(data []byte) (model.RuleDefinition, error) {
	var d model.RuleDefinition
	fields, body, err := parse(data, true)
	if err != nil {
		return d, err
	}
	if v := fields["id"]; v != nil && v.Kind != yaml.ScalarNode {
		return d, fmt.Errorf(invalidIDMessage)
	} else if v != nil && v.Tag != "!!null" {
		d.ID = v.Value
	}
	for _, field := range []struct {
		key  string
		dest *string
	}{{"name", &d.Name}, {"description", &d.Description}, {"path_pattern", &d.PathPattern}, {"content_pattern", &d.ContentPattern}} {
		v := fields[field.key]
		if v == nil {
			continue
		}
		if v.Kind != yaml.ScalarNode || (v.Tag != "!!str" && !(v.Tag == "!!null" && v.Value == "")) {
			return d, fmt.Errorf("%s は文字列で入力してください", field.key)
		}
		*field.dest = v.Value
	}
	d.Body = body
	d.ID = strings.TrimSpace(d.ID)
	d.Name = strings.TrimSpace(d.Name)
	d.Description = strings.TrimSpace(d.Description)
	d.PathPattern = strings.TrimSpace(d.PathPattern)
	d.ContentPattern = strings.TrimSpace(d.ContentPattern)
	return d, Validate(d)
}

// scalar keeps each front matter value on one line. yaml.v3 folds long plain
// text at 80 columns; JSON strings are valid YAML double-quoted scalars.
func scalar(tag, value string) string {
	out, err := yaml.Marshal(&yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value})
	line := strings.TrimSuffix(string(out), "\n")
	if err == nil && !strings.Contains(line, "\n") {
		return line
	}
	var quoted bytes.Buffer
	encoder := json.NewEncoder(&quoted)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(value)
	return strings.TrimSuffix(quoted.String(), "\n")
}

func Encode(d model.RuleDefinition) ([]byte, error) {
	if err := Validate(d); err != nil {
		return nil, err
	}
	d.ID, _ = NormalizeID(d.ID)
	d.Name, _ = NormalizeName(d.Name)
	d.Description = strings.TrimSpace(d.Description)
	d.PathPattern = strings.TrimSpace(d.PathPattern)
	d.ContentPattern = strings.TrimSpace(d.ContentPattern)
	idTag := "!!str"
	if decimalID.MatchString(d.ID) {
		idTag = "!!int"
	}
	var out bytes.Buffer
	out.WriteString("---\n")
	out.WriteString("id: " + scalar(idTag, d.ID) + "\n")
	for _, field := range []struct{ key, value string }{{"name", d.Name}, {"description", d.Description}, {"path_pattern", d.PathPattern}, {"content_pattern", d.ContentPattern}} {
		out.WriteString(field.key + ": " + scalar("!!str", field.value) + "\n")
	}
	out.WriteString("---\n\n")
	out.WriteString(d.Body)
	if out.Len() > MaxBytes {
		return nil, fmt.Errorf("ルールは4MiB以内にしてください")
	}
	return out.Bytes(), nil
}
func Revision(d model.RuleDefinition) string {
	data, _ := json.Marshal(d)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func ToRule(id string, d model.RuleDefinition) model.Rule {
	return model.Rule{ID: id, Title: d.Name, Summary: d.Description, PathPattern: d.PathPattern, ContentPattern: d.ContentPattern, Body: d.Body, Always: strings.TrimSpace(d.PathPattern) == "" && strings.TrimSpace(d.ContentPattern) == ""}
}
func Markdown(d model.RuleDefinition) string {
	return "# " + d.Name + "\n\n" + d.Description + "\n\n" + d.Body
}
