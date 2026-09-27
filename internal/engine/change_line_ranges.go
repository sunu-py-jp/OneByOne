package engine

import (
	"fmt"
	"sort"
	"strings"

	"onebyone/internal/model"
)

// verifiedEditLineRanges verifies item-specific sub-replacements against the
// immutable source and candidate. Coordinates come only from actual changes;
// model-generated line numbers and prose locations are never trusted.
func verifiedEditLineRanges(before, after string, edits []model.Edit) ([]model.EditLineRange, error) {
	type located struct {
		start int
		edit  model.Edit
	}
	ordered := make([]located, 0, len(edits))
	for _, edit := range edits {
		start := strings.Index(before, edit.OldText)
		if edit.OldText == "" || start < 0 || start != strings.LastIndex(before, edit.OldText) {
			return nil, fmt.Errorf("line attribution: edit oldText must uniquely match the original source")
		}
		ordered = append(ordered, located{start: start, edit: edit})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].start < ordered[j].start })
	var expected strings.Builder
	previousEnd := 0
	for _, item := range ordered {
		if item.start < previousEnd {
			return nil, fmt.Errorf("line attribution: edits overlap")
		}
		expected.WriteString(before[previousEnd:item.start])
		expected.WriteString(item.edit.NewText)
		previousEnd = item.start + len(item.edit.OldText)
	}
	expected.WriteString(before[previousEnd:])
	if expected.String() != after {
		return nil, fmt.Errorf("line attribution: edits do not reproduce the candidate")
	}

	ranges := []model.EditLineRange{}
	offset := 0
	for _, item := range ordered {
		patches, err := verifiedAttributionPatches(item.edit)
		if err != nil {
			return nil, err
		}
		patchOffset := 0
		for _, patch := range patches {
			blocks, err := changedByteBlocks(patch.before, patch.after)
			if err != nil {
				return nil, err
			}
			if len(item.edit.ItemIDs) > 1 && len(blocks) > 1 {
				return nil, fmt.Errorf("line attribution: fragment for %s spans separate changes; split it into individual attributions", strings.Join(patch.itemIDs, ", "))
			}
			for _, block := range blocks {
				oldStart := item.start + patch.start + block.oldStart
				oldEnd := item.start + patch.start + block.oldEnd
				newStart := item.start + offset + patch.start + patchOffset + block.newStart
				newEnd := item.start + offset + patch.start + patchOffset + block.newEnd
				span := model.EditLineRange{ItemIDs: append([]string(nil), patch.itemIDs...)}
				span.BeforeStart, span.BeforeEnd = changedLineRange(before, oldStart, oldEnd, after[newStart:newEnd])
				span.AfterStart, span.AfterEnd = changedLineRange(after, newStart, newEnd, before[oldStart:oldEnd])
				ranges = append(ranges, span)
			}
			patchOffset += len(patch.after) - len(patch.before)
		}
		offset += len(item.edit.NewText) - len(item.edit.OldText)
	}
	return ranges, nil
}

type attributionPatch struct {
	start         int
	before, after string
	itemIDs       []string
}

func verifiedAttributionPatches(edit model.Edit) ([]attributionPatch, error) {
	allowed := make(map[string]bool, len(edit.ItemIDs))
	for _, id := range edit.ItemIDs {
		if strings.TrimSpace(id) == "" || allowed[id] {
			return nil, fmt.Errorf("line attribution: itemIDs must be nonempty and unique")
		}
		allowed[id] = true
	}
	if len(allowed) == 0 {
		return nil, fmt.Errorf("line attribution: each edit must identify its plan items")
	}
	fragments := edit.Attributions
	if len(fragments) == 0 {
		if len(allowed) != 1 {
			return nil, fmt.Errorf("line attribution: edits with multiple itemIDs require item-specific attributions")
		}
		fragments = []model.EditAttribution{{ItemID: edit.ItemIDs[0], BeforeText: edit.OldText, AfterText: edit.NewText}}
	}
	type key struct{ before, after string }
	grouped := make(map[key]int, len(fragments))
	covered := make(map[string]bool, len(allowed))
	patches := make([]attributionPatch, 0, len(fragments))
	for _, fragment := range fragments {
		if !allowed[fragment.ItemID] {
			return nil, fmt.Errorf("line attribution: unknown itemID %q", fragment.ItemID)
		}
		if fragment.BeforeText == "" || fragment.BeforeText == fragment.AfterText {
			return nil, fmt.Errorf("line attribution: %s requires a nonempty beforeText anchor and a real change", fragment.ItemID)
		}
		start := strings.Index(edit.OldText, fragment.BeforeText)
		if start < 0 || start != strings.LastIndex(edit.OldText, fragment.BeforeText) {
			return nil, fmt.Errorf("line attribution: beforeText for %s must uniquely match inside edit oldText", fragment.ItemID)
		}
		k := key{fragment.BeforeText, fragment.AfterText}
		if index, exists := grouped[k]; exists {
			for _, id := range patches[index].itemIDs {
				if id == fragment.ItemID {
					return nil, fmt.Errorf("line attribution: duplicate fragment for %s", fragment.ItemID)
				}
			}
			patches[index].itemIDs = append(patches[index].itemIDs, fragment.ItemID)
		} else {
			grouped[k] = len(patches)
			patches = append(patches, attributionPatch{start: start, before: fragment.BeforeText, after: fragment.AfterText, itemIDs: []string{fragment.ItemID}})
		}
		covered[fragment.ItemID] = true
	}
	for _, id := range edit.ItemIDs {
		if !covered[id] {
			return nil, fmt.Errorf("line attribution: no fragment provided for %s", id)
		}
	}
	sort.Slice(patches, func(i, j int) bool { return patches[i].start < patches[j].start })
	previousEnd := 0
	var reproduced strings.Builder
	for _, patch := range patches {
		if patch.start < previousEnd {
			return nil, fmt.Errorf("line attribution: fragments partially overlap; use disjoint anchors or the exact same beforeText/afterText pair for shared changes")
		}
		reproduced.WriteString(edit.OldText[previousEnd:patch.start])
		reproduced.WriteString(patch.after)
		previousEnd = patch.start + len(patch.before)
	}
	reproduced.WriteString(edit.OldText[previousEnd:])
	if reproduced.String() != edit.NewText {
		return nil, fmt.Errorf("line attribution: fragments must reproduce all of edit newText exactly")
	}
	return patches, nil
}

type changedByteBlock struct {
	oldStart, oldEnd int
	newStart, newEnd int
}

// A bounded line LCS separates changes around unchanged lines. Byte trimming
// retains accurate empty-side semantics for both inline and whole-line edits.
func changedByteBlocks(before, after string) ([]changedByteBlock, error) {
	prefix, suffix := commonByteEdges(before, after)
	oldPart, newPart := before[prefix:len(before)-suffix], after[prefix:len(after)-suffix]
	if oldPart == "" && newPart == "" {
		return nil, fmt.Errorf("line attribution: fragment contains no changes")
	}
	if oldPart == "" || newPart == "" {
		return []changedByteBlock{{prefix, len(before) - suffix, prefix, len(after) - suffix}}, nil
	}
	oldLines, newLines := lineTokens(oldPart), lineTokens(newPart)
	const maxCells = 1_000_000
	n, m := len(oldLines), len(newLines)
	if n+1 > maxCells/(m+1) {
		return nil, fmt.Errorf("line attribution: fragment exceeds the line-diff budget; split it into smaller attributions")
	}
	width := m + 1
	lcs := make([]uint32, (n+1)*width)
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if oldLines[i] == newLines[j] {
				lcs[i*width+j] = lcs[(i+1)*width+j+1] + 1
			} else {
				lcs[i*width+j] = max(lcs[(i+1)*width+j], lcs[i*width+j+1])
			}
		}
	}
	blocks := []changedByteBlock{}
	oldPos, newPos := prefix, prefix
	block := changedByteBlock{oldStart: oldPos, newStart: newPos}
	flush := func() {
		if block.oldStart == oldPos && block.newStart == newPos {
			return
		}
		p, s := commonByteEdges(before[block.oldStart:oldPos], after[block.newStart:newPos])
		blocks = append(blocks, changedByteBlock{block.oldStart + p, oldPos - s, block.newStart + p, newPos - s})
	}
	for i, j := 0, 0; i < n || j < m; {
		if i < n && j < m && oldLines[i] == newLines[j] {
			flush()
			oldPos += len(oldLines[i])
			newPos += len(newLines[j])
			i++
			j++
			block = changedByteBlock{oldStart: oldPos, newStart: newPos}
		} else if i < n && (j == m || lcs[(i+1)*width+j] >= lcs[i*width+j+1]) {
			oldPos += len(oldLines[i])
			i++
		} else {
			newPos += len(newLines[j])
			j++
		}
	}
	flush()
	return blocks, nil
}

func commonByteEdges(before, after string) (prefix, suffix int) {
	for prefix < len(before) && prefix < len(after) && before[prefix] == after[prefix] {
		prefix++
	}
	for suffix < len(before)-prefix && suffix < len(after)-prefix && before[len(before)-suffix-1] == after[len(after)-suffix-1] {
		suffix++
	}
	return
}

func lineTokens(text string) []string {
	tokens := strings.SplitAfter(text, "\n")
	if len(tokens) > 0 && tokens[len(tokens)-1] == "" {
		tokens = tokens[:len(tokens)-1]
	}
	return tokens
}

// The legacy helper intentionally provides no coordinates for ambiguous old
// multi-item edits. New candidates call verifiedEditLineRanges and receive an
// actionable diagnostic instead of silently losing their attribution.
func exactEditLineRanges(before, after string, edits []model.Edit) []model.EditLineRange {
	const unlinked = "__historical_unlinked_edit__"
	linked := append([]model.Edit(nil), edits...)
	for i := range linked {
		if len(linked[i].ItemIDs) == 0 {
			linked[i].ItemIDs = []string{unlinked}
		}
	}
	ranges, err := verifiedEditLineRanges(before, after, linked)
	if err != nil {
		return nil
	}
	result := []model.EditLineRange{}
	for _, span := range ranges {
		if len(span.ItemIDs) == 1 && span.ItemIDs[0] == unlinked {
			continue
		}
		result = append(result, span)
	}
	return result
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
