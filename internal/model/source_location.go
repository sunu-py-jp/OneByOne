package model

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// SourceLocation names inclusive, 1-based full source lines. Plan locations
// refer only to the immutable original; review issues declare their own side.
type SourceLocation struct {
	StartLine int    `json:"startLine"`
	EndLine   int    `json:"endLine"`
	Excerpt   string `json:"excerpt"`
}

// ValidateSourceLocation checks explicit evidence, never guessing a position
// from a substring. BOM/CRLF follow source decoding conventions. A final line
// terminator in the excerpt is optional; all other whitespace is significant.
func ValidateSourceLocation(content string, location SourceLocation) error {
	normalize := func(value string) string {
		return strings.ReplaceAll(strings.TrimPrefix(value, "\ufeff"), "\r\n", "\n")
	}
	if !utf8.ValidString(content) || !utf8.ValidString(location.Excerpt) {
		return errors.New("source location requires valid UTF-8")
	}
	content = normalize(content)
	excerpt := normalize(location.Excerpt)
	if strings.TrimSpace(excerpt) == "" {
		return errors.New("source location requires a nonempty code excerpt")
	}
	if location.StartLine < 1 || location.EndLine < location.StartLine {
		return errors.New("source location requires 1-based inclusive startLine/endLine")
	}
	if content == "" {
		return errors.New("source location cannot reference an empty file")
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if location.EndLine > len(lines) {
		return fmt.Errorf("source location lines %d-%d are outside the %d-line source", location.StartLine, location.EndLine, len(lines))
	}
	expected := strings.Join(lines[location.StartLine-1:location.EndLine], "\n")
	if excerpt != expected && excerpt != expected+"\n" {
		return errors.New("source location excerpt does not match the exact full source lines; correct the line range or excerpt")
	}
	return nil
}

// ResolveSourceLocation is for fresh LLM output only. Prefer an exact declared
// range; if numbering is wrong, a unique full-line excerpt can establish the
// range mechanically. Archived evidence must use ValidateSourceLocation instead
// so old claims are never silently upgraded or relocated.
func ResolveSourceLocation(content string, location SourceLocation) (SourceLocation, error) {
	if err := ValidateSourceLocation(content, location); err == nil {
		return location, nil
	}
	if !utf8.ValidString(content) || !utf8.ValidString(location.Excerpt) {
		return SourceLocation{}, errors.New("source location requires valid UTF-8")
	}
	normalize := func(value string) string {
		return strings.ReplaceAll(strings.TrimPrefix(value, "\ufeff"), "\r\n", "\n")
	}
	content = normalize(content)
	excerpt := normalize(location.Excerpt)
	if content == "" || strings.TrimSpace(excerpt) == "" {
		return SourceLocation{}, errors.New("source location requires an existing nonempty code excerpt")
	}
	// Match only complete lines, preserving indentation and internal blank lines.
	quoted := strings.TrimSuffix(excerpt, "\n")
	matches, start := 0, 0
	for offset := 0; offset < len(content); {
		relative := strings.Index(content[offset:], quoted)
		if relative < 0 {
			break
		}
		index := offset + relative
		end := index + len(quoted)
		if (index == 0 || content[index-1] == '\n') && (end == len(content) || content[end] == '\n') {
			matches++
			start = index
			if matches > 1 {
				break
			}
		}
		offset = index + 1
	}
	if matches != 1 {
		return SourceLocation{}, fmt.Errorf("source location has %d exact full-line excerpt matches; provide a correct range or a unique enclosing declaration/call", matches)
	}
	location.StartLine = 1 + strings.Count(content[:start], "\n")
	location.EndLine = location.StartLine + strings.Count(quoted, "\n")
	if err := ValidateSourceLocation(content, location); err != nil {
		return SourceLocation{}, err
	}
	return location, nil
}
