package engine

import (
	"sort"
	"strings"

	"onebyone/internal/model"
)

// exactEditLineRanges uses the same immutable source and exact replacements as
// applyEdits. Common anchors are trimmed so surrounding unchanged lines do not
// acquire rule badges. Prose locations and model-generated line numbers are
// deliberately ignored. Call only after applyEdits has validated the proposal.
func exactEditLineRanges(before, after string, edits []model.Edit) []model.EditLineRange {
	type located struct {
		start int
		edit  model.Edit
	}
	ordered := make([]located, 0, len(edits))
	for _, edit := range edits {
		start := strings.Index(before, edit.OldText)
		if edit.OldText == "" || start < 0 || start != strings.LastIndex(before, edit.OldText) {
			return nil
		}
		ordered = append(ordered, located{start: start, edit: edit})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })
	offset := 0
	previousEnd := 0
	var expected strings.Builder
	ranges := []model.EditLineRange{}
	for _, item := range ordered {
		edit := item.edit
		if item.start < previousEnd {
			return nil
		}
		expected.WriteString(before[previousEnd:item.start])
		expected.WriteString(edit.NewText)
		previousEnd = item.start + len(edit.OldText)
		prefix := 0
		for prefix < len(edit.OldText) && prefix < len(edit.NewText) && edit.OldText[prefix] == edit.NewText[prefix] {
			prefix++
		}
		suffix := 0
		for suffix < len(edit.OldText)-prefix && suffix < len(edit.NewText)-prefix && edit.OldText[len(edit.OldText)-suffix-1] == edit.NewText[len(edit.NewText)-suffix-1] {
			suffix++
		}
		oldStart, oldEnd := item.start+prefix, item.start+len(edit.OldText)-suffix
		newStart, newEnd := item.start+offset+prefix, item.start+offset+len(edit.NewText)-suffix
		if newStart < 0 || newEnd < newStart || newEnd > len(after) {
			return nil
		}
		if oldStart < oldEnd || newStart < newEnd {
			span := model.EditLineRange{ItemIDs: append([]string(nil), edit.ItemIDs...)}
			span.BeforeStart, span.BeforeEnd = changedLineRange(before, oldStart, oldEnd, after[newStart:newEnd])
			span.AfterStart, span.AfterEnd = changedLineRange(after, newStart, newEnd, before[oldStart:oldEnd])
			if len(span.ItemIDs) > 0 {
				ranges = append(ranges, span)
			}
		}
		offset += len(edit.NewText) - len(edit.OldText)
	}
	expected.WriteString(before[previousEnd:])
	if expected.String() != after {
		return nil
	}
	return ranges
}

func changedLineRange(text string, start, end int, oppositeChange string) (int, int) {
	if start == end {
		// Whole-line insertion/deletion has no lines on its empty side. An
		// inline insertion/deletion still modifies the existing opposite line.
		atBoundary := start == 0 || text[start-1] == '\n'
		if (atBoundary && strings.HasSuffix(oppositeChange, "\n")) || start == len(text) && atBoundary {
			return 0, 0
		}
		line := strings.Count(text[:start], "\n") + 1
		return line, line
	}
	first := strings.Count(text[:start], "\n") + 1
	last := strings.Count(text[:end-1], "\n") + 1
	return first, last
}
