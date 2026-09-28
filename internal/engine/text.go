package engine

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"sort"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
	"onebyone/internal/sourceencoding"
)

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

type sourceText struct {
	text     string
	document sourceencoding.Document
}

func decodeSource(data []byte) (sourceText, error) {
	document, err := sourceencoding.Decode(data)
	return sourceText{text: document.Text, document: document}, err
}

func (s sourceText) encode(text string, edits ...[]model.Edit) ([]byte, error) {
	if len(edits) == 0 {
		return s.document.Encode(text)
	}
	spans := make([]sourceencoding.Edit, 0, len(edits[0]))
	for _, edit := range edits[0] {
		start := strings.Index(s.text, edit.OldText)
		if edit.OldText == "" || start < 0 || start != strings.LastIndex(s.text, edit.OldText) {
			return nil, fmt.Errorf("変更元が一意に一致しません")
		}
		spans = append(spans, sourceencoding.Edit{Start: start, End: start + len(edit.OldText), Text: edit.NewText})
	}
	encoded, err := s.document.Apply(spans)
	if err != nil {
		return nil, err
	}
	verified, err := decodeSource(encoded)
	if err != nil || verified.text != text {
		return nil, fmt.Errorf("元の文字コード・改行を保持した変更結果が一致しません")
	}
	return encoded, nil
}

func sourceDisplay(data []byte) (string, error) {
	source, err := decodeSource(data)
	return source.text, err
}

// Git diff metadata is UTF-8 while each hunk retains its source encoding.
// Decode the payload of hunk lines only; raw .diff artifacts stay applicable.
func sourceDiffDisplay(data, before, after []byte) (string, error) {
	original, err := sourceencoding.Decode(before)
	if err != nil {
		return "", err
	}
	candidate, err := sourceencoding.Decode(after)
	if err != nil {
		return "", err
	}
	if hasStandaloneCR(before) || hasStandaloneCR(after) {
		return normalizedDisplayDiff(context.Background(), data, original.Text, candidate.Text)
	}
	return sourceDiffDisplayWithEncoding(data, original.Encoding, candidate.Encoding)
}

func hasStandaloneCR(data []byte) bool {
	for i, b := range data {
		if b == '\r' && (i+1 == len(data) || data[i+1] != '\n') {
			return true
		}
	}
	return false
}

// Git counts LF lines while the editor counts normalized source lines. A CR
// file needs a display-only diff to keep line annotations aligned. Raw patch
// artifacts are never overwritten or used as display text for publication.
func normalizedDisplayDiff(ctx context.Context, metadata []byte, before, after string) (string, error) {
	diff, err := candidateSnapshotDiff(ctx, os.TempDir(), "source", []byte(before), []byte(after))
	if err != nil || diff == "" {
		return diff, err
	}
	oldHeader := strings.Index(string(metadata), "\n@@ ")
	newHeader := strings.Index(diff, "\n@@ ")
	if oldHeader >= 0 && newHeader >= 0 {
		diff = string(metadata[:oldHeader+1]) + diff[newHeader+1:]
	}
	return diff, nil
}

func sourceDiffDisplayWithEncoding(data []byte, before, after sourceencoding.Encoding) (string, error) {
	lines := strings.SplitAfter(string(data), "\n")
	inHunk := false
	for i, line := range lines {
		if strings.HasPrefix(line, "@@ ") {
			inHunk = true
			continue
		}
		if !inHunk || len(line) == 0 || (line[0] != '+' && line[0] != '-' && line[0] != ' ') {
			continue
		}
		encoding := before
		if line[0] == '+' {
			encoding = after
		}
		text, err := sourceencoding.DecodeFragment([]byte(line[1:]), encoding)
		if err != nil {
			return "", err
		}
		lines[i] = line[:1] + text
	}
	return strings.Join(lines, ""), nil
}

func applyEdits(text string, edits []model.Edit) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("変更箇所が1件以上必要です")
	}
	type span struct {
		start, end  int
		replacement string
	}
	spans := []span{}
	for i, e := range edits {
		if e.OldText == "" || e.OldText == e.NewText {
			return "", fmt.Errorf("変更%d: 空または同一内容の変更です", i+1)
		}
		if strings.Contains(e.OldText, "\r") || strings.Contains(e.NewText, "\r") {
			return "", fmt.Errorf("変更%d: 改行はLFで指定してください", i+1)
		}
		if strings.ContainsRune(e.NewText, 0) || !utf8.ValidString(e.NewText) {
			return "", fmt.Errorf("変更%d: 不正なテキストです", i+1)
		}
		start := strings.Index(text, e.OldText)
		if start < 0 || start != strings.LastIndex(text, e.OldText) {
			return "", fmt.Errorf("変更%d: oldTextが一意に一致しません。十分な前後の文脈を含めてください", i+1)
		}
		spans = append(spans, span{start, start + len(e.OldText), e.NewText})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", fmt.Errorf("変更箇所が重なっています")
		}
	}
	result := text
	for i := len(spans) - 1; i >= 0; i-- {
		p := spans[i]
		result = result[:p.start] + p.replacement + result[p.end:]
	}
	if result == text {
		return "", fmt.Errorf("実際の変更がありません")
	}
	if strings.TrimSpace(result) == "" {
		return "", fmt.Errorf("ファイル全体の削除は採用できません")
	}
	return result, nil
}
