// Package diagnostics reads the deliberately small, fixed JSONL interchange
// format for source diagnostics. It does not execute a compiler or other tool.
package diagnostics

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
)

const MaxBytes = 32 << 20

// InputError identifies a diagnostic-source problem independently of errors in
// a rule definition. Callers can keep the import/clear controls reachable.
type InputError struct{ err error }

func (e *InputError) Error() string { return e.err.Error() }
func (e *InputError) Unwrap() error { return e.err }

func inputError(err error) error {
	var input *InputError
	if err == nil || errors.As(err, &input) {
		return err
	}
	return &InputError{err: err}
}

// Read accepts a regular file only and bounds both the initial stat and the
// actual read, including a file that grows while it is being imported.
func Read(name string) (items []model.SourceDiagnostic, err error) {
	defer func() { err = inputError(err) }()
	info, err := os.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("診断JSONLを開けません: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("診断JSONLにはシンボリックリンクではない通常のファイルを指定してください")
	}
	if info.Size() > MaxBytes {
		return nil, fmt.Errorf("診断JSONLは32 MiB以内にしてください")
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	current, err := os.Lstat(name)
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(info, opened) || !current.Mode().IsRegular() || !os.SameFile(current, opened) {
		return nil, fmt.Errorf("診断JSONLが読み込み中に変更されました")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	return Decode(data)
}

// Decode validates every physical line before returning any diagnostics.
// Duplicate object keys and unknown fields are errors, rather than silently
// selecting the last value or accepting fields from a different format.
func Decode(data []byte) (items []model.SourceDiagnostic, err error) {
	defer func() { err = inputError(err) }()
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("診断JSONLは32 MiB以内にしてください")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("診断JSONLはUTF-8で保存してください")
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	out := []model.SourceDiagnostic{}
	seen := map[string]bool{}
	for i := 1; len(data) > 0; i++ {
		var line []byte
		line, data, _ = bytes.Cut(data, []byte{'\n'})
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		d, err := decodeLine(line)
		if err != nil {
			return nil, fmt.Errorf("診断JSONL %d行: %w", i, err)
		}
		key := d.File + "\x00" + strconv.Itoa(d.Line) + "\x00" + d.Code + "\x00" + d.Message
		if !seen[key] {
			out = append(out, d)
			seen[key] = true
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("診断JSONLに診断がありません。診断を解除するには解除操作を使用してください")
	}
	return out, nil
}

func decodeLine(line []byte) (model.SourceDiagnostic, error) {
	var out model.SourceDiagnostic
	d := json.NewDecoder(bytes.NewReader(line))
	start, err := d.Token()
	if err != nil || start != json.Delim('{') {
		return out, fmt.Errorf("1行に1つのJSONオブジェクトを指定してください")
	}
	seen := map[string]bool{}
	for d.More() {
		token, err := d.Token()
		if err != nil {
			return out, err
		}
		key, ok := token.(string)
		if !ok || seen[key] {
			return out, fmt.Errorf("同じ項目を重複して指定できません: %v", token)
		}
		seen[key] = true
		var raw json.RawMessage
		if err = d.Decode(&raw); err != nil {
			return out, err
		}
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
			return out, fmt.Errorf("%s に null は指定できません", key)
		}
		switch key {
		case "file", "message", "code":
			var value string
			if err = json.Unmarshal(raw, &value); err != nil {
				return out, fmt.Errorf("%s は文字列で指定してください", key)
			}
			if strings.ContainsRune(value, 0) {
				return out, fmt.Errorf("%s にNUL文字は指定できません", key)
			}
			switch key {
			case "file":
				out.File, err = normalizeFile(value)
				if err != nil {
					return out, err
				}
			case "message":
				if strings.TrimSpace(value) == "" {
					return out, fmt.Errorf("message は空ではない文字列で指定してください")
				}
				out.Message = value
			case "code":
				out.Code = value
			}
		case "line":
			if err = json.Unmarshal(raw, &out.Line); err != nil || out.Line <= 0 {
				return out, fmt.Errorf("line は1以上の整数で指定してください")
			}
		default:
			return out, fmt.Errorf("不明な項目 %q（使用可能: file, message, line, code）", key)
		}
	}
	if _, err = d.Token(); err != nil {
		return out, err
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return out, fmt.Errorf("1行に1つのJSONオブジェクトを指定してください")
	}
	if !seen["file"] || !seen["message"] {
		return out, fmt.Errorf("file と message は必須です")
	}
	return out, nil
}

func normalizeFile(value string) (string, error) {
	value = strings.ReplaceAll(value, "\\", "/")
	if strings.TrimSpace(value) == "" || strings.HasPrefix(value, "/") || strings.ContainsAny(value, ":\x00\r\n") {
		return "", fmt.Errorf("file は対象フォルダ内の相対ファイルパスで指定してください")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", fmt.Errorf("file に親フォルダへの移動は指定できません")
		}
	}
	clean := path.Clean(value)
	if clean == "." || strings.HasSuffix(value, "/") {
		return "", fmt.Errorf("file はファイル名まで指定してください")
	}
	return clean, nil
}

// Encode serializes the public interchange properties, never extracted RuleIDs.
// Decode verifies that even programmatically supplied data obeys the format.
func Encode(items []model.SourceDiagnostic) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	for _, item := range items {
		item.RuleIDs = nil
		if err := enc.Encode(item); err != nil {
			return nil, err
		}
		if b.Len() > MaxBytes {
			return nil, fmt.Errorf("診断JSONLは32 MiB以内にしてください")
		}
	}
	if _, err := Decode(b.Bytes()); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
