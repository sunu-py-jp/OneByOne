// Package rulepack reads and writes portable rules.json documents.
package rulepack

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/store"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	MaxFileBytes     int64 = 4 << 20
	MaxExpandedBytes int64 = 32 << 20
	MaxFiles               = 5000
)

// Entry keeps a rule's Markdown verbatim. ID is its front matter id.
type Entry struct {
	ID       string `json:"id"`
	Markdown string `json:"markdown"`
}
type Package struct{ Rules []Entry }
type document struct {
	Rules []string `json:"rules"`
}

var utf8BOM = []byte{0xef, 0xbb, 0xbf}

func decodeDocument(data []byte) ([]string, error) {
	if int64(len(data)) > MaxExpandedBytes || !utf8.Valid(data) {
		return nil, fmt.Errorf("rules.json は32MiB以内のUTF-8で指定してください")
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, fmt.Errorf("rules.json はrules配列を持つJSONオブジェクトで指定してください")
	}
	var docs []string
	seen := false
	for dec.More() {
		t, err := dec.Token()
		if err != nil || t != "rules" || seen {
			return nil, fmt.Errorf("rules.json に未定義または重複した項目があります")
		}
		seen = true
		var raw json.RawMessage
		if err = dec.Decode(&raw); err != nil || bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return nil, fmt.Errorf("rules はMarkdown文字列の配列で指定してください")
		}
		var values []json.RawMessage
		if err = json.Unmarshal(raw, &values); err != nil {
			return nil, fmt.Errorf("rules はMarkdown文字列の配列で指定してください")
		}
		for _, v := range values {
			var s string
			if bytes.Equal(bytes.TrimSpace(v), []byte("null")) || json.Unmarshal(v, &s) != nil {
				return nil, fmt.Errorf("rules の各要素はMarkdown文字列で指定してください")
			}
			docs = append(docs, s)
		}
	}
	if t, err := dec.Token(); err != nil || t != json.Delim('}') || !seen {
		return nil, fmt.Errorf("rules 配列がありません")
	}
	if dec.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("rules.json に余分な内容があります")
	}
	if len(docs) > MaxFiles {
		return nil, fmt.Errorf("ルールは%d件以内にしてください", MaxFiles)
	}
	return docs, nil
}

// identify requires a readable, unique id for every rule. The rest of each
// Markdown document is validated separately so one invalid rule stays visible.
func identify(docs []string) (*Package, error) {
	p := &Package{Rules: []Entry{}}
	seen := map[string]int{}
	for i, md := range docs {
		if int64(len(md)) > MaxFileBytes {
			return nil, fmt.Errorf("ルール%d: 各ルールは4MiB以内にしてください", i+1)
		}
		id, err := ruleformat.PeekID([]byte(md))
		if err != nil {
			return nil, fmt.Errorf("ルール%d: %w", i+1, err)
		}
		if previous, ok := seen[strings.ToLower(id)]; ok {
			return nil, fmt.Errorf("ルール%d: id %s はルール%dと重複しています", i+1, id, previous)
		}
		seen[strings.ToLower(id)] = i + 1
		p.Rules = append(p.Rules, Entry{ID: id, Markdown: md})
	}
	return p, nil
}

// Decode reads a portable document and validates every rule.
func Decode(data []byte) (*Package, error) {
	docs, err := decodeDocument(data)
	if err != nil {
		return nil, err
	}
	p, err := identify(docs)
	if err != nil {
		return nil, err
	}
	return p, Validate(p)
}
func Validate(p *Package) error {
	if p == nil || len(p.Rules) > MaxFiles {
		return fmt.Errorf("ルール数が上限を超えています")
	}
	seen := map[string]bool{}
	for i, e := range p.Rules {
		d, err := ruleformat.Decode([]byte(e.Markdown))
		if err != nil {
			return fmt.Errorf("ルール%d: %w", i+1, err)
		}
		if d.ID != e.ID || seen[strings.ToLower(e.ID)] {
			return fmt.Errorf("ルール%d: id %s が不正または重複しています", i+1, e.ID)
		}
		seen[strings.ToLower(e.ID)] = true
	}
	return nil
}

// WithID rewrites a valid rule's front matter id without changing its body.
func WithID(e Entry, id string) (Entry, error) {
	d, err := ruleformat.Decode([]byte(e.Markdown))
	if err != nil {
		return e, err
	}
	d.ID = id
	data, err := ruleformat.Encode(d)
	if err != nil {
		return e, err
	}
	return Entry{ID: id, Markdown: string(data)}, nil
}
func Encode(p *Package) ([]byte, error) {
	if err := Validate(p); err != nil {
		return nil, err
	}
	return encodeDocument(p)
}
func encodeDocument(p *Package) ([]byte, error) {
	d := document{Rules: []string{}}
	for _, e := range p.Rules {
		d.Rules = append(d.Rules, e.Markdown)
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if int64(len(b)) > MaxExpandedBytes {
		return nil, fmt.Errorf("rules.json は32MiB以内にしてください")
	}
	return append(b, '\n'), err
}
func readFile(path string) ([]byte, error) {
	full, err := checkedAbsolute(path)
	if err != nil {
		return nil, err
	}
	root, err := openDirectory(filepath.Dir(full))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := openRegular(root, filepath.Base(full))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, MaxExpandedBytes+1))
	if err == nil && int64(len(b)) > MaxExpandedBytes {
		err = fmt.Errorf("ルールファイルは32MiB以内にしてください")
	}
	return b, err
}
func Read(path string) (*Package, error) {
	b, err := readFile(path)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return Decode(b)
	case ".csv":
		return DecodeCSV(b)
	default:
		return nil, fmt.Errorf("rules.json またはCSVファイルを選択してください")
	}
}
func Write(path string, p *Package) error {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		return fmt.Errorf("保存先はJSONファイルを指定してください")
	}
	b, err := Encode(p)
	if err != nil {
		return err
	}
	parent, err := openDirectory(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer parent.Close()
	if info, statErr := parent.Lstat(filepath.Base(path)); statErr == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("書き出し先は通常ファイルにしてください")
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	return store.WriteFile(path, b, 0600)
}

// CSV metadata columns may occur in any order. Remaining columns become H1 sections.
func DecodeCSV(data []byte) (*Package, error) {
	if int64(len(data)) > MaxExpandedBytes || !utf8.Valid(data) {
		return nil, fmt.Errorf("CSVは32MiB以内のUTF-8で指定してください")
	}
	r := csv.NewReader(bytes.NewReader(bytes.TrimPrefix(data, utf8BOM)))
	headers, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("CSVのヘッダーを読み込めません: %w", err)
	}
	indexes := map[string]int{}
	for i, h := range headers {
		h = strings.TrimSpace(h)
		if h == "" || strings.ContainsAny(h, "\r\n\x00") {
			return nil, fmt.Errorf("CSVのカラム名は空でない1行にしてください")
		}
		if _, ok := indexes[h]; ok {
			return nil, fmt.Errorf("CSVのカラム名が重複しています: %s", h)
		}
		indexes[h] = i
		headers[i] = h
	}
	meta := map[string]bool{"id": true, "name": true, "description": true, "path_pattern": true, "content_pattern": true}
	for _, key := range []string{"id", "name", "description", "path_pattern", "content_pattern"} {
		if _, ok := indexes[key]; !ok {
			return nil, fmt.Errorf("CSVに必須カラム %s がありません", key)
		}
	}
	p := &Package{Rules: []Entry{}}
	rows := map[string]int{}
	for line := 2; ; line++ {
		row, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("CSVの%d行目を読み込めません: %w", line, err)
		}
		if blankCSVRow(row) {
			continue // Spreadsheet exports often keep empty trailing rows.
		}
		if len(p.Rules) >= MaxFiles {
			return nil, fmt.Errorf("ルールは%d件以内にしてください", MaxFiles)
		}
		d := model.RuleDefinition{ID: row[indexes["id"]], Name: row[indexes["name"]], Description: row[indexes["description"]], PathPattern: row[indexes["path_pattern"]], ContentPattern: row[indexes["content_pattern"]]}
		var body strings.Builder
		for i, h := range headers {
			if !meta[h] && strings.TrimSpace(row[i]) != "" {
				if body.Len() > 0 {
					body.WriteString("\n\n")
				}
				body.WriteString("# " + h + "\n\n" + row[i])
			}
		}
		d.Body = body.String()
		md, err := ruleformat.Encode(d)
		if err != nil {
			return nil, fmt.Errorf("CSVの%d行目: %w", line, err)
		}
		id := strings.TrimSpace(d.ID)
		if previous, ok := rows[strings.ToLower(id)]; ok {
			return nil, fmt.Errorf("CSVの%d行目: id %s は%d行目と重複しています", line, id, previous)
		}
		rows[strings.ToLower(id)] = line
		p.Rules = append(p.Rules, Entry{ID: id, Markdown: string(md)})
	}
	return p, nil
}

func blankCSVRow(row []string) bool {
	for _, cell := range row {
		if strings.TrimSpace(cell) != "" {
			return false
		}
	}
	return true
}
