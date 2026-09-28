// Package sourceencoding preserves source bytes while presenting normalized
// Unicode text to the model. Detection always prefers valid UTF-8; otherwise
// strict Shift_JIS (including the Windows extensions supported by x/text) is
// used. Invalid sequences are never silently replaced.
package sourceencoding

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/japanese"
)

type Encoding string

const (
	UTF8     Encoding = "UTF-8"
	ShiftJIS Encoding = "Shift_JIS"
)

type boundary struct{ text, raw int }

// Document retains the original representation. Text contains LF newlines and
// no BOM; edits use UTF-8 byte offsets into Text, never offsets into raw bytes.
type Document struct {
	Text       string
	Encoding   Encoding
	raw        []byte
	boundaries []boundary
	newline    string
}

type Edit struct {
	Start, End int
	Text       string
}

// Detect validates a complete source without building its editing index.
func Detect(data []byte) (Encoding, error) {
	if bytes.IndexByte(data, 0) >= 0 {
		return UTF8, fmt.Errorf("NULを含むバイナリファイルは編集できません")
	}
	if utf8.Valid(data) {
		return UTF8, nil
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return UTF8, fmt.Errorf("UTF-8 BOMに続く文字コードが不正です")
	}
	_, err := DecodeFragment(data, ShiftJIS)
	return ShiftJIS, err
}

func Decode(data []byte) (Document, error) {
	d := Document{Encoding: UTF8, raw: bytes.Clone(data), newline: "\n"}
	start := 0
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		start = 3
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return d, fmt.Errorf("NULを含むバイナリファイルは編集できません")
	}
	var shiftJISText string
	if !utf8.Valid(data[start:]) {
		if start != 0 {
			return d, fmt.Errorf("UTF-8 BOMに続く文字コードが不正です")
		}
		d.Encoding = ShiftJIS
		var err error
		shiftJISText, err = DecodeFragment(data, d.Encoding)
		if err != nil {
			return d, err
		}
	}
	var text strings.Builder
	hasNewline := false
	decodedPos := 0
	for pos := start; pos < len(data); {
		d.addBoundary(text.Len(), pos)
		size := 1
		var value string
		if data[pos] == '\r' {
			if pos+1 < len(data) && data[pos+1] == '\n' {
				size = 2
			}
			value = "\n"
			decodedPos += size
		} else if data[pos] < utf8.RuneSelf {
			value = string(data[pos : pos+1])
			decodedPos++
		} else if d.Encoding == UTF8 {
			_, size = utf8.DecodeRune(data[pos:])
			value = string(data[pos : pos+size])
		} else {
			if (data[pos] >= 0x81 && data[pos] <= 0x9f) || (data[pos] >= 0xe0 && data[pos] <= 0xfc) {
				size = 2
			}
			_, decodedSize := utf8.DecodeRuneInString(shiftJISText[decodedPos:])
			value = shiftJISText[decodedPos : decodedPos+decodedSize]
			decodedPos += decodedSize
		}
		if value == "\n" && !hasNewline {
			d.newline, hasNewline = string(data[pos:pos+size]), true
		}
		text.WriteString(value)
		pos += size
	}
	d.addBoundary(text.Len(), len(data))
	d.Text = text.String()
	return d, nil
}

// DecodeFragment decodes a source line without guessing a different encoding.
// It preserves the fragment's newlines, making it suitable for Git diff output.
func DecodeFragment(data []byte, encoding Encoding) (string, error) {
	if bytes.IndexByte(data, 0) >= 0 {
		return "", fmt.Errorf("バイナリデータは表示できません")
	}
	if encoding == UTF8 {
		if !utf8.Valid(data) {
			return "", fmt.Errorf("UTF-8の文字列が不正です")
		}
		return string(data), nil
	}
	decoded, err := japanese.ShiftJIS.NewDecoder().Bytes(data)
	if err != nil || bytes.Contains(decoded, []byte("\ufffd")) {
		return "", fmt.Errorf("UTF-8またはShift_JISとして読み込めない文字コードです")
	}
	return string(decoded), nil
}

func (d *Document) addBoundary(text, raw int) {
	if len(d.boundaries) > 0 {
		last := d.boundaries[len(d.boundaries)-1]
		if last.raw-last.text == raw-text {
			return
		}
	}
	d.boundaries = append(d.boundaries, boundary{text, raw})
}

func (d Document) offset(pos int) (int, error) {
	if pos < 0 || pos > len(d.Text) || (pos < len(d.Text) && !utf8.RuneStart(d.Text[pos])) {
		return 0, fmt.Errorf("文字の途中を変更することはできません")
	}
	i := sort.Search(len(d.boundaries), func(i int) bool { return d.boundaries[i].text > pos }) - 1
	if i < 0 {
		return 0, fmt.Errorf("文字の位置が不正です")
	}
	point := d.boundaries[i]
	return pos + point.raw - point.text, nil
}

func (d Document) Encode(text string) ([]byte, error) {
	return d.Apply([]Edit{{Start: 0, End: len(d.Text), Text: text}})
}

// Apply copies every untouched byte verbatim, including BOM, mixed newline
// styles and alternate CP932 encodings. New lines inherit the nearby original
// line ending; existing matching lines retain their own representation.
func (d Document) Apply(edits []Edit) ([]byte, error) {
	ordered := append([]Edit(nil), edits...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Start < ordered[j].Start })
	var out bytes.Buffer
	previous, rawPrevious := 0, 0
	for _, edit := range ordered {
		if edit.Start < previous || edit.End < edit.Start || edit.End > len(d.Text) {
			return nil, fmt.Errorf("変更範囲が不正または重複しています")
		}
		if !utf8.ValidString(edit.Text) || strings.ContainsAny(edit.Text, "\r\x00") {
			return nil, fmt.Errorf("変更内容はLF改行の有効なUnicode文字列で指定してください")
		}
		start, err := d.offset(edit.Start)
		if err != nil {
			return nil, err
		}
		end, err := d.offset(edit.End)
		if err != nil {
			return nil, err
		}
		out.Write(d.raw[rawPrevious:start])
		replacement, err := d.replacement(edit)
		if err != nil {
			return nil, err
		}
		out.Write(replacement)
		previous, rawPrevious = edit.End, end
	}
	out.Write(d.raw[rawPrevious:])
	return out.Bytes(), nil
}

func (d Document) replacement(edit Edit) ([]byte, error) {
	old := d.Text[edit.Start:edit.End]
	if old == edit.Text {
		a, _ := d.offset(edit.Start)
		b, _ := d.offset(edit.End)
		return bytes.Clone(d.raw[a:b]), nil
	}
	// Trim unchanged rune boundaries so large contextual replacements do not
	// re-encode their surrounding source or normalize its line endings.
	prefix := 0
	for prefix < len(old) && prefix < len(edit.Text) {
		a, an := utf8.DecodeRuneInString(old[prefix:])
		b, bn := utf8.DecodeRuneInString(edit.Text[prefix:])
		if a != b || an != bn {
			break
		}
		prefix += an
	}
	suffix := 0
	for suffix < len(old)-prefix && suffix < len(edit.Text)-prefix {
		a, an := utf8.DecodeLastRuneInString(old[:len(old)-suffix])
		b, bn := utf8.DecodeLastRuneInString(edit.Text[:len(edit.Text)-suffix])
		if a != b || an != bn {
			break
		}
		suffix += an
	}
	begin, _ := d.offset(edit.Start)
	middleStart, _ := d.offset(edit.Start + prefix)
	middleEnd, _ := d.offset(edit.End - suffix)
	end, _ := d.offset(edit.End)
	var out bytes.Buffer
	out.Write(d.raw[begin:middleStart])
	oldMiddle := old[prefix : len(old)-suffix]
	newMiddle := edit.Text[prefix : len(edit.Text)-suffix]
	oldLines := strings.SplitAfter(oldMiddle, "\n")
	// Unchanged lines inside a contextual edit are copied byte for byte in
	// occurrence order, including duplicates with different raw encodings.
	// Consume each original occurrence once, so insertion cannot duplicate its
	// representation or normalize the following repeated line.
	type originalLine struct{ start, end int }
	lines := make(map[string][]originalLine, len(oldLines))
	endings := []string{}
	pos := edit.Start + prefix
	for _, line := range oldLines {
		lines[line] = append(lines[line], originalLine{start: pos, end: pos + len(line)})
		if strings.HasSuffix(line, "\n") {
			a, _ := d.offset(pos + len(line) - 1)
			b, _ := d.offset(pos + len(line))
			endings = append(endings, string(d.raw[a:b]))
		}
		pos += len(line)
	}
	ending := d.nearbyNewline(edit.Start + prefix)
	for i, line := range strings.SplitAfter(newMiddle, "\n") {
		if occurrences := lines[line]; len(occurrences) > 0 && line != "" {
			original := occurrences[0]
			lines[line] = occurrences[1:]
			a, _ := d.offset(original.start)
			b, _ := d.offset(original.end)
			out.Write(d.raw[a:b])
			continue
		}
		if i < len(endings) {
			ending = endings[i]
		}
		value := line
		if strings.HasSuffix(value, "\n") {
			value = strings.TrimSuffix(value, "\n") + ending
		}
		encoded, err := d.encodeFragment(value)
		if err != nil {
			return nil, err
		}
		out.Write(encoded)
	}
	out.Write(d.raw[middleEnd:end])
	return out.Bytes(), nil
}

func (d Document) nearbyNewline(pos int) string {
	// Prefer the edited line's terminator; at EOF use the preceding one.
	if next := strings.IndexByte(d.Text[pos:], '\n'); next >= 0 {
		a, _ := d.offset(pos + next)
		b, _ := d.offset(pos + next + 1)
		return string(d.raw[a:b])
	}
	if previous := strings.LastIndexByte(d.Text[:pos], '\n'); previous >= 0 {
		a, _ := d.offset(previous)
		b, _ := d.offset(previous + 1)
		return string(d.raw[a:b])
	}
	return d.newline
}

func (d Document) encodeFragment(text string) ([]byte, error) {
	if d.Encoding == UTF8 {
		return []byte(text), nil
	}
	encoded, err := japanese.ShiftJIS.NewEncoder().Bytes([]byte(text))
	if err != nil {
		return nil, fmt.Errorf("変更内容にShift_JISで保存できない文字があります: %w", err)
	}
	roundTrip, err := DecodeFragment(encoded, ShiftJIS)
	if err != nil || roundTrip != text {
		return nil, fmt.Errorf("変更内容をShift_JISで損失なく保存できません")
	}
	return encoded, nil
}
