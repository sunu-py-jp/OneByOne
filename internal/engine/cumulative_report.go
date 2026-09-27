package engine

import (
	"fmt"
	"strings"

	"onebyone/internal/model"
)

// cumulativeChangeReports describes the changes which still exist between the
// initial file and its accepted contents. Attempt reports remain immutable: the
// returned IDs and line coordinates belong to this cumulative view only.
func cumulativeChangeReports(task model.Task, before, after string, readAttempt func(model.Attempt) (string, string, error)) ([]model.ChangeReportItem, error) {
	before, after = normalizedReportText(before), normalizedReportText(after)
	history := repairHistory(task)
	rows := []model.ChangeReportItem{}
	comparison := cumulativeLineMapping(before, after)
	fixedRules := map[string]bool{}
	for _, h := range history {
		if before == after || !h.AdoptedChanges() {
			continue
		}
		var oldMap, newMap cumulativeLines
		mapped := false
		if readAttempt != nil {
			if oldText, newText, err := readAttempt(h); err == nil {
				oldMap = cumulativeLineMapping(normalizedReportText(oldText), before)
				newMap = cumulativeLineMapping(normalizedReportText(newText), after)
				mapped = true
			}
		}
		for index, recorded := range h.Changes {
			if recorded.Status != "fixed" {
				continue
			}
			row := cumulativeReportRow(h, index, recorded)
			if strings.TrimSpace(row.Location) != "" {
				row.Location = "修正時の位置: " + row.Location
			}
			row.LineRanges = nil
			verified := recorded.AttributionVersion == model.LineAttributionVersion
			allRemoved := verified && len(recorded.LineRanges) > 0 && mapped
			if mapped && verified {
				for _, span := range recorded.LineRanges {
					ranges, removed := cumulativeReportRange(span, oldMap, newMap, comparison)
					row.LineRanges = append(row.LineRanges, ranges...)
					allRemoved = allRemoved && removed
				}
			}
			if allRemoved {
				continue
			}
			rows = append(rows, row)
			fixedRules[row.RuleID] = true
		}
	}
	// Failed proposals are not part of the accepted file. Only the latest
	// attempt's unresolved work belongs beside the accumulated accepted fixes;
	// earlier failures disappear once a later attempt resolves them.
	if len(history) > 0 {
		h := history[len(history)-1]
		var heldMappings *heldLineMappings
		if readAttempt != nil {
			for _, recorded := range h.Changes {
				if recorded.Status == "needs_human" && recorded.AttributionVersion == model.LineAttributionVersion && len(recorded.LineRanges) > 0 {
					if sourceBefore, sourceAfter, err := readAttempt(h); err == nil {
						heldMappings = newHeldLineMappings(normalizedReportText(sourceBefore), normalizedReportText(sourceAfter), before, after)
					}
					break
				}
			}
		}
		for index, recorded := range h.Changes {
			if recorded.Status == "fixed" {
				continue
			}
			// A rule-wide "already compliant" decision from a no-op retry must
			// not contradict the fixes which made that rule compliant. Distinct
			// item-level decisions are never merged merely by matching rule ID.
			if recorded.Status == "unchanged" && fixedRules[recorded.RuleID] && strings.HasPrefix(recorded.ID, "rule:") {
				continue
			}
			row := cumulativeReportRow(h, index, recorded)
			row.LineRanges = nil
			// Held locations are evidence about source which still requires a
			// decision, not adopted changes. Rebase them only through exact,
			// unambiguous unchanged lines of the attempt's original input.
			if recorded.Status == "needs_human" && recorded.AttributionVersion == model.LineAttributionVersion && heldMappings != nil {
				row.LineRanges = heldMappings.ranges(recorded.LineRanges)
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func cumulativeReportRow(h model.Attempt, index int, recorded model.ChangeReportItem) model.ChangeReportItem {
	row := recorded
	// Include the row position as well: independently planned attempts
	// routinely reuse item IDs.
	row.ID = fmt.Sprintf("attempt:%s:%d:%s", h.ID, index, recorded.ID)
	row.SourceAttemptID = h.ID
	return row
}

// cumulativeLines maps only lines whose unchanged alignment is unambiguous.
// Missing keys are unknown, not an invitation to reuse old line numbers.
type cumulativeLines struct {
	sourceLines int
	forward     map[int]int
	reverse     map[int]int
	removed     map[int]bool
	added       map[int]bool
}

const cumulativeLineCellLimit = 2_000_000

func cumulativeLineMapping(before, after string) cumulativeLines {
	m := cumulativeLines{forward: map[int]int{}, reverse: map[int]int{}, removed: map[int]bool{}, added: map[int]bool{}}
	a, b := cumulativeTextLines(before), cumulativeTextLines(after)
	m.sourceLines = len(a)
	if before == after {
		for i := range a {
			m.forward[i+1], m.reverse[i+1] = i+1, i+1
		}
		return m
	}
	n, k := len(a), len(b)
	if n == 0 || k == 0 {
		for i := range a {
			m.removed[i+1] = true
		}
		for j := range b {
			m.added[j+1] = true
		}
		return m
	}
	// This is display provenance, not a migration gate. Bound its work and
	// memory for very large files. Absence is still certain; alignment is not.
	if n > cumulativeLineCellLimit/k {
		oldLines, newLines := map[string]bool{}, map[string]bool{}
		for _, line := range a {
			oldLines[line] = true
		}
		for _, line := range b {
			newLines[line] = true
		}
		for i, line := range a {
			m.removed[i+1] = !newLines[line]
		}
		for j, line := range b {
			m.added[j+1] = !oldLines[line]
		}
		return m
	}
	width := k + 1
	prefix, suffix := make([]int32, (n+1)*width), make([]int32, (n+1)*width)
	for i := 0; i < n; i++ {
		for j := 0; j < k; j++ {
			if a[i] == b[j] {
				prefix[(i+1)*width+j+1] = prefix[i*width+j] + 1
			} else {
				prefix[(i+1)*width+j+1] = max(prefix[i*width+j+1], prefix[(i+1)*width+j])
			}
		}
	}
	for i := n - 1; i >= 0; i-- {
		for j := k - 1; j >= 0; j-- {
			if a[i] == b[j] {
				suffix[i*width+j] = suffix[(i+1)*width+j+1] + 1
			} else {
				suffix[i*width+j] = max(suffix[(i+1)*width+j], suffix[i*width+j+1])
			}
		}
	}
	longest := prefix[n*width+k]
	possibleNew := make([]bool, k)
	for i := 0; i < n; i++ {
		match, matches := -1, 0
		for j := 0; j < k; j++ {
			if a[i] == b[j] && prefix[i*width+j]+1+suffix[(i+1)*width+j+1] == longest {
				match, matches, possibleNew[j] = j, matches+1, true
			}
		}
		if matches == 0 {
			m.removed[i+1] = true
			continue
		}
		if matches != 1 {
			continue
		}
		// A single possible partner is not enough: another optimal alignment
		// may omit this line entirely. Only a forced match is a safe mapping.
		forced := true
		for j := 0; j <= k; j++ {
			if prefix[i*width+j]+suffix[(i+1)*width+j] == longest {
				forced = false
				break
			}
		}
		if forced {
			m.forward[i+1], m.reverse[match+1] = match+1, i+1
		}
	}
	for j, possible := range possibleNew {
		if !possible {
			m.added[j+1] = true
		}
	}
	return m
}

func cumulativeTextLines(text string) []string {
	if text == "" {
		return nil
	}
	// Keep each newline in the comparison so a final-newline edit is visible.
	lines := strings.SplitAfter(text, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

// A side which is definitely absent from the baseline/latest file has no
// coordinates (e.g. a second repair of text added by the first attempt).
func cumulativeMappedRange(start, end int, mapping cumulativeLines) ([]int, bool) {
	if start == 0 && end == 0 {
		return nil, true
	}
	if start <= 0 || end < start || end > mapping.sourceLines {
		return nil, false
	}
	lines := []int{}
	for line := start; line <= end; line++ {
		if mapped, ok := mapping.forward[line]; ok {
			lines = append(lines, mapped)
		} else if !mapping.removed[line] {
			return nil, false
		}
	}
	return lines, true
}

func cumulativeReportRange(span model.ChangeLineRange, oldMap, newMap, comparison cumulativeLines) ([]model.ChangeLineRange, bool) {
	if span == (model.ChangeLineRange{}) {
		return nil, false
	}
	oldLines, oldKnown := cumulativeMappedRange(span.BeforeStart, span.BeforeEnd, oldMap)
	newLines, newKnown := cumulativeMappedRange(span.AfterStart, span.AfterEnd, newMap)
	if !oldKnown || !newKnown {
		return nil, false
	}
	changedOld, oldCertain := cumulativeChangedLines(oldLines, comparison.removed, comparison.forward)
	changedNew, newCertain := cumulativeChangedLines(newLines, comparison.added, comparison.reverse)
	spans := []model.ChangeLineRange{}
	for _, line := range changedOld {
		spans = append(spans, model.ChangeLineRange{BeforeStart: line, BeforeEnd: line})
	}
	for _, line := range changedNew {
		spans = append(spans, model.ChangeLineRange{AfterStart: line, AfterEnd: line})
	}
	return spans, oldCertain && newCertain && len(spans) == 0
}

func cumulativeChangedLines(lines []int, changed map[int]bool, unchanged map[int]int) ([]int, bool) {
	selected := []int{}
	certain := true
	for _, line := range lines {
		if changed[line] {
			selected = append(selected, line)
		} else if _, ok := unchanged[line]; !ok {
			certain = false
		}
	}
	return selected, certain
}

func normalizedReportText(text string) string {
	return strings.ReplaceAll(strings.TrimPrefix(text, "\ufeff"), "\r\n", "\n")
}

type heldLineMappings struct {
	toBaseline, toAccepted, candidateToOriginal cumulativeLines
	candidateAvailable                          bool
}

func newHeldLineMappings(original, candidate, baseline, accepted string) *heldLineMappings {
	return &heldLineMappings{
		toBaseline:          cumulativeLineMapping(original, baseline),
		toAccepted:          cumulativeLineMapping(original, accepted),
		candidateToOriginal: cumulativeLineMapping(candidate, original),
		candidateAvailable:  candidate != "",
	}
}

func (m *heldLineMappings) ranges(spans []model.ChangeLineRange) []model.ChangeLineRange {
	ranges := []model.ChangeLineRange{}
	seen := map[model.ChangeLineRange]bool{}
	appendRange := func(span model.ChangeLineRange) {
		if !seen[span] {
			ranges = append(ranges, span)
			seen[span] = true
		}
	}
	for _, span := range spans {
		originalSets := [][]int{}
		if original, ok := exactSourceLines(span.BeforeStart, span.BeforeEnd, m.toBaseline.sourceLines); ok {
			originalSets = append(originalSets, original)
		}
		if m.candidateAvailable {
			if candidate, ok := exactSourceLines(span.AfterStart, span.AfterEnd, m.candidateToOriginal.sourceLines); ok {
				if original, ok := unchangedMappedLines(candidate, m.candidateToOriginal); ok {
					originalSets = append(originalSets, original)
				}
			}
		}
		for _, original := range originalSets {
			if lines, ok := unchangedMappedLines(original, m.toBaseline); ok {
				for _, group := range contiguousReportLines(lines) {
					appendRange(model.ChangeLineRange{BeforeStart: group[0], BeforeEnd: group[1]})
				}
			}
			if lines, ok := unchangedMappedLines(original, m.toAccepted); ok {
				for _, group := range contiguousReportLines(lines) {
					appendRange(model.ChangeLineRange{AfterStart: group[0], AfterEnd: group[1]})
				}
			}
		}
	}
	return ranges
}

func exactSourceLines(start, end, total int) ([]int, bool) {
	if start < 1 || end < start || end > total {
		return nil, false
	}
	lines := make([]int, 0, end-start+1)
	for line := start; line <= end; line++ {
		lines = append(lines, line)
	}
	return lines, true
}

func unchangedMappedLines(lines []int, mapping cumulativeLines) ([]int, bool) {
	mapped := make([]int, 0, len(lines))
	for _, line := range lines {
		position, ok := mapping.forward[line]
		if !ok {
			return nil, false
		}
		mapped = append(mapped, position)
	}
	return mapped, len(mapped) > 0
}

func contiguousReportLines(lines []int) [][2]int {
	groups := [][2]int{}
	for _, line := range lines {
		if len(groups) > 0 && groups[len(groups)-1][1]+1 == line {
			groups[len(groups)-1][1] = line
		} else {
			groups = append(groups, [2]int{line, line})
		}
	}
	return groups
}
