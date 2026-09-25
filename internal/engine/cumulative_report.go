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
	history := repairHistory(task)
	rows := []model.ChangeReportItem{}
	comparison := cumulativeLineMapping(before, after)
	fixedRules := map[string]bool{}
	for _, h := range history {
		if before == after || h.Outcome != "done" || h.Commit == "" {
			continue
		}
		var oldMap, newMap cumulativeLines
		mapped := false
		if readAttempt != nil {
			if oldText, newText, err := readAttempt(h); err == nil {
				oldMap = cumulativeLineMapping(oldText, before)
				newMap = cumulativeLineMapping(newText, after)
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
			allRemoved := len(recorded.LineRanges) > 0 && mapped
			if mapped {
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
			row.LineRanges = nil // An unaccepted candidate has no cumulative coordinates.
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func cumulativeReportRow(h model.Attempt, index int, recorded model.ChangeReportItem) model.ChangeReportItem {
	row := recorded
	// Include the row position as well: old reports can have empty or duplicate
	// item IDs, and independently planned attempts routinely reuse item IDs.
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
