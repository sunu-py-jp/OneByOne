// Package solutionrules handles the three-field, Skills-compatible rules used
// by solution workspaces. It never interprets file filters or extraction rules.
package solutionrules

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
	"onebyone/internal/sourceencoding"
)

type Rule struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Content     string `json:"content"`
}

const maxFileSize = 8 << 20

var ruleID = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func Validate(rules []Rule) error {
	if len(rules) == 0 || len(rules) > 2000 {
		return fmt.Errorf("ルールは1〜2000件で指定してください")
	}
	seen := map[string]bool{}
	total := 0
	for _, rule := range rules {
		if len(rule.ID) > 64 || !ruleID.MatchString(rule.ID) {
			return fmt.Errorf("ルールIDは64文字以下の小文字英数字とハイフンで指定してください（先頭・末尾・連続ハイフンは不可）: %s", rule.ID)
		}
		if windowsReserved(rule.ID) {
			return fmt.Errorf("Windowsの予約名はルールIDに使用できません: %s", rule.ID)
		}
		if seen[rule.ID] {
			return fmt.Errorf("ルールIDが重複しています: %s", rule.ID)
		}
		seen[rule.ID] = true
		if !utf8.ValidString(rule.Description) || strings.TrimSpace(rule.Description) == "" || utf8.RuneCountInString(rule.Description) > 1024 {
			return fmt.Errorf("%s: descriptionは1〜1024文字で指定してください", rule.ID)
		}
		if !utf8.ValidString(rule.Content) || strings.TrimSpace(rule.Content) == "" {
			return fmt.Errorf("%s: contentにMarkdownの指示本文を指定してください", rule.ID)
		}
		if strings.ContainsRune(rule.Description, 0) || strings.ContainsRune(rule.Content, 0) {
			return fmt.Errorf("%s: NUL文字を含めることはできません", rule.ID)
		}
		total += len(rule.ID) + len(rule.Description) + len(rule.Content)
		if total > maxFileSize {
			return fmt.Errorf("ルール全体は8 MiB以下で指定してください")
		}
	}
	return nil
}

func windowsReserved(id string) bool {
	switch id {
	case "con", "prn", "aux", "nul":
		return true
	}
	return len(id) == 4 && (strings.HasPrefix(id, "com") || strings.HasPrefix(id, "lpt")) && id[3] >= '1' && id[3] <= '9'
}

// Decode accepts exactly {"rules":[{"id","description","content"}]}.
// Unknown fields, duplicate JSON keys, and legacy extraction fields are errors.
func Decode(data []byte) ([]Rule, error) {
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("ルールJSONは8 MiB以下で指定してください")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("ルールJSONは有効なUTF-8で保存してください")
	}
	if err := checkJSONKeys(json.NewDecoder(bytes.NewReader(data)), 0); err != nil {
		return nil, err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("ルールJSONを読み込めません: %w", err)
	}
	if len(envelope) != 1 || envelope["rules"] == nil {
		return nil, fmt.Errorf("JSONの最上位はrulesだけを指定してください")
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(envelope["rules"], &records); err != nil {
		return nil, fmt.Errorf("rulesはルールの配列で指定してください: %w", err)
	}
	rules := make([]Rule, 0, len(records))
	for i, record := range records {
		if len(record) != 3 || record["id"] == nil || record["description"] == nil || record["content"] == nil {
			return nil, fmt.Errorf("ルール%dはid・description・contentの3項目だけを指定してください", i+1)
		}
		var rule Rule
		for key, target := range map[string]*string{"id": &rule.ID, "description": &rule.Description, "content": &rule.Content} {
			if bytes.Equal(bytes.TrimSpace(record[key]), []byte("null")) {
				return nil, fmt.Errorf("%sは文字列で指定してください", key)
			}
			if err := json.Unmarshal(record[key], target); err != nil {
				return nil, fmt.Errorf("%sは文字列で指定してください", key)
			}
		}
		rules = append(rules, rule)
	}
	if err := Validate(rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func checkJSONKeys(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return fmt.Errorf("ルールJSONの入れ子が深すぎます")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	start, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch start {
	case '{':
		seen := map[string]bool{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("不正なJSONキーです")
			}
			if seen[key] {
				return fmt.Errorf("JSONキーが重複しています: %s", key)
			}
			seen[key] = true
			if err := checkJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := checkJSONKeys(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("不正なJSONです")
	}
	_, err = decoder.Token()
	return err
}

func Encode(rules []Rule) ([]byte, error) {
	if err := Validate(rules); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(struct {
		Rules []Rule `json:"rules"`
	}{rules}, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data)+1 > maxFileSize {
		return nil, fmt.Errorf("書き出すルールJSONは8 MiB以下にしてください")
	}
	return append(data, '\n'), nil
}

// Read imports JSON, three-column CSV, a SKILL.md, a skill directory, or a
// directory containing skills. Imports never follow symlinks.
func Read(path string) ([]Rule, error) {
	absolute, err := checkedPath(path, false)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(absolute)
	if err != nil {
		return nil, err
	}
	if info.IsDir() {
		return readDirectory(absolute)
	}
	data, err := readFile(absolute)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(absolute)) {
	case ".json":
		return Decode(data)
	case ".csv":
		return decodeCSV(data)
	case ".md":
		if !strings.EqualFold(filepath.Base(absolute), "SKILL.md") {
			return nil, fmt.Errorf("MarkdownルールはSKILL.mdを指定してください")
		}
		rule, err := decodeSkill(data)
		if err != nil {
			return nil, err
		}
		return []Rule{rule}, nil
	default:
		return nil, fmt.Errorf("JSON・CSV・SKILL.mdまたはSkillsフォルダを選択してください")
	}
}

func readFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileSize {
		return nil, fmt.Errorf("通常ファイルかつ8 MiB以下のルールを指定してください: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxFileSize+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxFileSize {
		return nil, fmt.Errorf("ルールファイルは8 MiB以下で指定してください")
	}
	return data, nil
}

func readDirectory(path string) ([]Rule, error) {
	direct := filepath.Join(path, "SKILL.md")
	if _, err := os.Lstat(direct); err == nil {
		data, err := readFile(direct)
		if err != nil {
			return nil, err
		}
		rule, err := decodeSkill(data)
		if err != nil {
			return nil, err
		}
		if filepath.Base(path) != rule.ID {
			return nil, fmt.Errorf("Skillsフォルダ名とnameが一致しません: %s", rule.ID)
		}
		return []Rule{rule}, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	rules := []Rule{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("シンボリックリンクは読み込めません: %s", entry.Name())
		}
		if !entry.IsDir() {
			continue
		}
		file := filepath.Join(path, entry.Name(), "SKILL.md")
		if _, err := os.Lstat(file); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return nil, err
		}
		data, err := readFile(file)
		if err != nil {
			return nil, err
		}
		rule, err := decodeSkill(data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", entry.Name(), err)
		}
		if entry.Name() != rule.ID {
			return nil, fmt.Errorf("Skillsフォルダ名とnameが一致しません: %s", rule.ID)
		}
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool { return rules[i].ID < rules[j].ID })
	if err := Validate(rules); err != nil {
		return nil, err
	}
	return rules, nil
}

func decodeCSV(data []byte) ([]Rule, error) {
	document, err := sourceencoding.Decode(data)
	if err != nil {
		return nil, err
	}
	reader := csv.NewReader(strings.NewReader(document.Text))
	reader.FieldsPerRecord = 3
	header, err := reader.Read()
	if err != nil {
		return nil, err
	}
	columns := map[string]int{}
	for i, key := range header {
		if key != "id" && key != "description" && key != "content" {
			return nil, fmt.Errorf("CSV列はid・description・contentだけを指定してください")
		}
		if _, ok := columns[key]; ok {
			return nil, fmt.Errorf("CSV列が重複しています: %s", key)
		}
		columns[key] = i
	}
	rules := []Rule{}
	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		rules = append(rules, Rule{ID: record[columns["id"]], Description: record[columns["description"]], Content: record[columns["content"]]})
	}
	if err := Validate(rules); err != nil {
		return nil, err
	}
	return rules, nil
}

type skillHeader struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

func decodeSkill(data []byte) (Rule, error) {
	var rule Rule
	document, err := sourceencoding.Decode(data)
	if err != nil {
		return rule, err
	}
	text := document.Text
	if !strings.HasPrefix(text, "---\n") {
		return rule, fmt.Errorf("SKILL.mdにはnameとdescriptionのYAMLフロントマターが必要です")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return rule, fmt.Errorf("SKILL.mdのYAMLフロントマターが閉じていません")
	}
	var header skillHeader
	decoder := yaml.NewDecoder(strings.NewReader(text[4 : 4+end]))
	decoder.KnownFields(true)
	if err := decoder.Decode(&header); err != nil {
		return rule, fmt.Errorf("Skillsのフロントマターはname・descriptionだけを指定してください: %w", err)
	}
	rule = Rule{ID: header.Name, Description: header.Description, Content: strings.TrimSpace(text[4+end+5:])}
	if err := Validate([]Rule{rule}); err != nil {
		return rule, err
	}
	return rule, nil
}

// ExportSkills writes destination/<id>/SKILL.md. All rules and collisions are
// validated before creating any skill. Existing skill folders are never reused.
func ExportSkills(destination string, rules []Rule) error {
	if err := Validate(rules); err != nil {
		return err
	}
	root, err := checkedPath(destination, true)
	if err != nil {
		return err
	}
	contents := make(map[string][]byte, len(rules))
	for _, rule := range rules {
		path := filepath.Join(root, rule.ID)
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("書き出し先に同名の項目があります: %s", rule.ID)
		} else if !os.IsNotExist(err) {
			return err
		}
		header, err := yaml.Marshal(skillHeader{Name: rule.ID, Description: rule.Description})
		if err != nil {
			return err
		}
		contents[rule.ID] = []byte("---\n" + string(header) + "---\n\n" + strings.TrimSpace(rule.Content) + "\n")
	}
	if err := os.MkdirAll(root, 0755); err != nil {
		return err
	}
	if _, err := checkedPath(root, false); err != nil {
		return err
	}
	created := []string{}
	// Reserve all target folders before writing any content. Mkdir is exclusive,
	// so a simultaneous export cannot overwrite another export's output.
	for _, rule := range rules {
		path := filepath.Join(root, rule.ID)
		if err := os.Mkdir(path, 0755); err != nil {
			for _, dir := range created {
				_ = os.Remove(dir)
			}
			return err
		}
		created = append(created, path)
	}
	for _, rule := range rules {
		path := filepath.Join(root, rule.ID, "SKILL.md")
		file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(contents[rule.ID])
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

// checkedPath rejects symlinks in every path component, including ancestor
// folders, so selecting a destination cannot redirect export outside that path.
func checkedPath(path string, allowMissing bool) (string, error) {
	if strings.TrimSpace(path) == "" || strings.ContainsRune(path, 0) {
		return "", fmt.Errorf("ルールのパスを指定してください")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.VolumeName(absolute) + string(filepath.Separator)
	rel := strings.TrimPrefix(absolute, current)
	missing := false
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" {
			continue
		}
		current = filepath.Join(current, part)
		if missing {
			continue
		}
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && allowMissing {
			missing = true
			continue
		}
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("シンボリックリンクは利用できません: %s", current)
		}
		if current != absolute && !info.IsDir() {
			return "", fmt.Errorf("親パスがディレクトリではありません: %s", current)
		}
	}
	return absolute, nil
}
