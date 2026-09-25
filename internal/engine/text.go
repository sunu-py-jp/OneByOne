package engine

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"onebyone/internal/model"
)

func digest(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }

type sourceText struct {
	text string
	bom  bool
	crlf bool
}

func decodeSource(data []byte) (sourceText, error) {
	s := sourceText{bom: bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf})}
	if s.bom {
		data = data[3:]
	}
	if !utf8.Valid(data) || bytes.IndexByte(data, 0) >= 0 {
		return s, fmt.Errorf("UTF-8以外またはバイナリのファイルは自動編集しません")
	}
	x := string(data)
	s.crlf = strings.Contains(x, "\r\n")
	if s.crlf && strings.Contains(strings.ReplaceAll(x, "\r\n", ""), "\n") {
		return s, fmt.Errorf("改行コードが混在しているため確認が必要です")
	}
	if strings.Contains(strings.ReplaceAll(x, "\r\n", ""), "\r") {
		return s, fmt.Errorf("CR単独の改行には対応していません")
	}
	s.text = strings.ReplaceAll(x, "\r\n", "\n")
	return s, nil
}

func (s sourceText) encode(text string) []byte {
	if s.crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	if s.bom {
		text = "\ufeff" + text
	}
	return []byte(text)
}

func applyEdits(text string, edits []model.Edit) (string, error) {
	if len(edits) == 0 || len(edits) > 100 {
		return "", fmt.Errorf("変更箇所は1〜100件必要です")
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
