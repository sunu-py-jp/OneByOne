package engine

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func cumulativeReportAttempt(id string, number int, spans ...model.ChangeLineRange) model.Attempt {
	return model.Attempt{ID: id, Number: number, Outcome: "done", Commit: id, Changes: []model.ChangeReportItem{{ID: "item", RuleID: "R001", RuleTitle: "rule", Status: "fixed", Location: "recorded location", Risk: "risk", Change: "repair", LineRanges: spans}}}
}

func cumulativeReportReader(texts map[string][2]string) func(model.Attempt) (string, string, error) {
	return func(h model.Attempt) (string, string, error) {
		text, ok := texts[h.ID]
		if !ok {
			return "", "", errors.New("unavailable")
		}
		return text[0], text[1], nil
	}
}

func TestCumulativeReportPreservesFixAcrossNoOpRetry(t *testing.T) {
	before, after := "start\nold\nend\n", "start\nnew\nend\n"
	h := cumulativeReportAttempt("first", 1, model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2})
	skipped := model.Attempt{ID: "second", Number: 2, Outcome: "skipped", Changes: []model.ChangeReportItem{
		{ID: "rule:R001", RuleID: "R001", Status: "unchanged", Reason: "already compliant"},
		{ID: "different-location", RuleID: "R001", Status: "unchanged", Location: "another method"},
		{ID: "rule:R002", RuleID: "R002", Status: "unchanged"},
	}}
	task := model.Task{History: []model.Attempt{h, skipped}}
	rows, err := cumulativeChangeReports(task, before, after, cumulativeReportReader(map[string][2]string{h.ID: {before, after}}))
	if err != nil || len(rows) != 3 || rows[0].Status != "fixed" || rows[1].Location != "another method" || rows[2].RuleID != "R002" {
		t.Fatalf("no-op retry lost adopted work or merged distinct locations: %+v, %v", rows, err)
	}
	if len(rows[0].LineRanges) == 0 || h.Changes[0].ID != "item" || len(h.Changes[0].LineRanges) != 1 {
		t.Fatal("lost provenance or mutated history")
	}
	if rows[0].SourceAttemptID != h.ID || rows[1].SourceAttemptID != skipped.ID || rows[2].SourceAttemptID != skipped.ID {
		t.Fatalf("a no-op retry reassigned earlier fixes to the latest attempt: %+v", rows)
	}
	if h.Changes[0].SourceAttemptID != "" || skipped.Changes[0].SourceAttemptID != "" || skipped.Changes[1].SourceAttemptID != "" {
		t.Fatal("display provenance must not be written into attempt history")
	}
}

func TestCumulativeReportMapsEarlierCoordinatesThroughInsertions(t *testing.T) {
	base := "header\nold\ntail\n"
	first := "header\nnew\ntail\n"
	latest := "intro\nheader\nnew\ntail\n"
	h1 := cumulativeReportAttempt("first", 1, model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2})
	h2 := cumulativeReportAttempt("second", 2, model.ChangeLineRange{AfterStart: 1, AfterEnd: 1})
	rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h1, h2}}, base, latest, cumulativeReportReader(map[string][2]string{h1.ID: {base, first}, h2.ID: {first, latest}}))
	if err != nil || len(rows) != 2 || rows[0].ID == rows[1].ID {
		t.Fatalf("different attempt items sharing rule/ID were merged: %+v %v", rows, err)
	}
	if rows[0].SourceAttemptID != h1.ID || rows[1].SourceAttemptID != h2.ID {
		t.Fatalf("successive fixes lost their own source attempts: %+v", rows)
	}
	want := []model.ChangeLineRange{{BeforeStart: 2, BeforeEnd: 2}, {AfterStart: 3, AfterEnd: 3}}
	if !reflect.DeepEqual(rows[0].LineRanges, want) {
		t.Fatalf("older coordinates were not rebased: got %+v want %+v", rows[0].LineRanges, want)
	}
}

func TestCumulativeReportMapsLaterBeforeCoordinatesBackToInitialFile(t *testing.T) {
	base := "header\nold\ntail\n"
	first := "intro\nheader\nold\ntail\n"
	latest := "intro\nheader\nnew\ntail\n"
	h1 := cumulativeReportAttempt("first", 1, model.ChangeLineRange{AfterStart: 1, AfterEnd: 1})
	h2 := cumulativeReportAttempt("second", 2, model.ChangeLineRange{BeforeStart: 3, BeforeEnd: 3, AfterStart: 3, AfterEnd: 3})
	rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h1, h2}}, base, latest, cumulativeReportReader(map[string][2]string{h1.ID: {base, first}, h2.ID: {first, latest}}))
	want := []model.ChangeLineRange{{BeforeStart: 2, BeforeEnd: 2}, {AfterStart: 3, AfterEnd: 3}}
	if err != nil || len(rows) != 2 || !reflect.DeepEqual(rows[1].LineRanges, want) {
		t.Fatalf("later before coordinates were reused: %+v %v", rows, err)
	}
}

func TestCumulativeReportRemovesUndoneChangesWhileKeepingOtherFixes(t *testing.T) {
	base := "header\nold\ntail\n"
	first := "header\nnew\ntail\n"
	latest := "intro\nheader\nold\ntail\n"
	h1 := cumulativeReportAttempt("first", 1, model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2})
	h2 := cumulativeReportAttempt("second", 2, model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 3, AfterEnd: 3})
	h2.Changes = append(h2.Changes, model.ChangeReportItem{ID: "intro", RuleID: "R002", Status: "fixed", Change: "add intro", LineRanges: []model.ChangeLineRange{{AfterStart: 1, AfterEnd: 1}}})
	rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h1, h2}}, base, latest, cumulativeReportReader(map[string][2]string{h1.ID: {base, first}, h2.ID: {first, latest}}))
	if err != nil || len(rows) != 1 || rows[0].RuleID != "R002" {
		t.Fatalf("reverted content was still counted as a current fix: %+v %v", rows, err)
	}
}

func TestCumulativeReportRemovesDeletedEarlierInsertion(t *testing.T) {
	base, first, latest := "head\n", "head\nadded\n", "head\nother\n"
	h1 := cumulativeReportAttempt("first", 1, model.ChangeLineRange{AfterStart: 2, AfterEnd: 2})
	h2 := cumulativeReportAttempt("second", 2, model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2}, model.ChangeLineRange{AfterStart: 2, AfterEnd: 2})
	rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h1, h2}}, base, latest, cumulativeReportReader(map[string][2]string{h1.ID: {base, first}, h2.ID: {first, latest}}))
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].ID, "second") {
		t.Fatalf("deleted insertion survived cumulative report: %+v %v", rows, err)
	}
}

func TestCumulativeReportSameContentHasNoFixedRows(t *testing.T) {
	h := cumulativeReportAttempt("first", 1, model.ChangeLineRange{BeforeStart: 1, BeforeEnd: 1, AfterStart: 1, AfterEnd: 1})
	rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h}}, "same\n", "same\n", nil)
	if err != nil || len(rows) != 0 {
		t.Fatalf("no diff must not have current fixed rows: %+v %v", rows, err)
	}
}

func TestCumulativeReportRetainsSuccessiveRepairsOfSameLine(t *testing.T) {
	h1 := cumulativeReportAttempt("first", 1, model.ChangeLineRange{BeforeStart: 1, BeforeEnd: 1, AfterStart: 1, AfterEnd: 1})
	h2 := cumulativeReportAttempt("second", 2, model.ChangeLineRange{BeforeStart: 1, BeforeEnd: 1, AfterStart: 1, AfterEnd: 1})
	rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h1, h2}}, "A\n", "C\n", cumulativeReportReader(map[string][2]string{h1.ID: {"A\n", "B\n"}, h2.ID: {"B\n", "C\n"}}))
	if err != nil || len(rows) != 2 || !reflect.DeepEqual(rows[0].LineRanges, []model.ChangeLineRange{{BeforeStart: 1, BeforeEnd: 1}}) || !reflect.DeepEqual(rows[1].LineRanges, []model.ChangeLineRange{{AfterStart: 1, AfterEnd: 1}}) {
		t.Fatalf("successive accepted contributions were erased or used intermediate coordinates: %+v %v", rows, err)
	}
}

func TestCumulativeReportInvalidHistoricalRangeHasNoCoordinates(t *testing.T) {
	for _, span := range []model.ChangeLineRange{
		{},
		{BeforeStart: 1, BeforeEnd: int(^uint(0) >> 1)},
		{BeforeStart: -1, BeforeEnd: 1},
		{BeforeStart: 2, BeforeEnd: 1},
	} {
		h := cumulativeReportAttempt("first", 1, span)
		rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h}}, "A\n", "B\n", cumulativeReportReader(map[string][2]string{h.ID: {"A\n", "B\n"}}))
		if err != nil || len(rows) != 1 || len(rows[0].LineRanges) != 0 {
			t.Fatalf("invalid historical range must not be used: %+v %v", rows, err)
		}
	}
}

func TestCumulativeReportKeepsOnlyLatestUnresolvedProposal(t *testing.T) {
	h1 := model.Attempt{ID: "failure", Number: 1, Outcome: "failed", Changes: []model.ChangeReportItem{{ID: "item", RuleID: "R001", Status: "not_applied"}}}
	h2 := cumulativeReportAttempt("success", 2, model.ChangeLineRange{BeforeStart: 1, BeforeEnd: 1, AfterStart: 1, AfterEnd: 1})
	h3 := model.Attempt{ID: "hold", Number: 3, Outcome: "needs_human", Changes: []model.ChangeReportItem{{ID: "item", RuleID: "R001", Status: "needs_human", Risk: "new issue", LineRanges: []model.ChangeLineRange{{AfterStart: 99, AfterEnd: 99}}}}}
	rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h1, h2, h3}}, "old\n", "new\n", cumulativeReportReader(map[string][2]string{h2.ID: {"old\n", "new\n"}}))
	if err != nil || len(rows) != 2 || rows[0].Status != "fixed" || rows[1].Status != "needs_human" || len(rows[1].LineRanges) > 0 {
		t.Fatalf("latest unresolved evidence must not erase fixed work or claim accepted coordinates: %+v %v", rows, err)
	}
	if rows[0].SourceAttemptID != h2.ID || rows[1].SourceAttemptID != h3.ID {
		t.Fatalf("a rejected latest proposal reassigned the accepted fix: %+v", rows)
	}
	rows, err = cumulativeChangeReports(model.Task{History: []model.Attempt{h1, h2}}, "old\n", "new\n", cumulativeReportReader(map[string][2]string{h2.ID: {"old\n", "new\n"}}))
	if err != nil || len(rows) != 1 || rows[0].Status != "fixed" {
		t.Fatalf("success retained an obsolete failure: %+v %v", rows, err)
	}
}

func TestCumulativeReportExcludesDiscardedAttempt(t *testing.T) {
	h1 := cumulativeReportAttempt("discarded", 1)
	h2 := cumulativeReportAttempt("retained", 2)
	task := model.Task{History: []model.Attempt{h1, h2}, Discards: []model.DiscardChange{{State: "done", ThroughAttempt: 1}}}
	rows, err := cumulativeChangeReports(task, "old\n", "new\n", nil)
	if err != nil || len(rows) != 1 || !strings.Contains(rows[0].ID, "retained") {
		t.Fatalf("discarded fixes survived: %+v %v", rows, err)
	}
}

func TestCumulativeReportFailedRetryPreservesEarlierFixProvenance(t *testing.T) {
	fixed := cumulativeReportAttempt("accepted", 1)
	failed := model.Attempt{ID: "rejected", Number: 2, Outcome: "failed", Changes: []model.ChangeReportItem{{ID: "item", RuleID: "R001", Status: "not_applied", Change: "unaccepted repair"}}}
	task := model.Task{History: []model.Attempt{fixed, failed}}
	want := copyTask(task)
	rows, err := cumulativeChangeReports(task, "before\n", "after\n", nil)
	if err != nil || len(rows) != 2 || rows[0].SourceAttemptID != fixed.ID || rows[1].SourceAttemptID != failed.ID || rows[0].Status != "fixed" || rows[1].Status != "not_applied" {
		t.Fatalf("a failed retry changed the source of an earlier accepted fix: %+v %v", rows, err)
	}
	if !reflect.DeepEqual(copyTask(task), want) {
		t.Fatal("cumulative display provenance mutated recorded attempt reports")
	}
}

func TestCumulativeReportAmbiguousOrUnavailableLocationsDoNotInventBadges(t *testing.T) {
	h := cumulativeReportAttempt("first", 1, model.ChangeLineRange{AfterStart: 2, AfterEnd: 2})
	for _, reader := range []func(model.Attempt) (string, string, error){nil, cumulativeReportReader(map[string][2]string{"first": {"head\n", "head\nrepeat\nrepeat\n"}})} {
		rows, err := cumulativeChangeReports(model.Task{History: []model.Attempt{h}}, "head\n", "head\nrepeat\n", reader)
		if err != nil || len(rows) != 1 || len(rows[0].LineRanges) != 0 || rows[0].Location != "修正時の位置: recorded location" {
			t.Fatalf("ambiguous history must retain prose without invented position: %+v %v", rows, err)
		}
	}
}

func TestCumulativeLineMappingForcedMatchesAndAmbiguousDeletion(t *testing.T) {
	m := cumulativeLineMapping("a\nx\nx\nb\n", "a\nx\nb\n")
	if m.forward[1] != 1 || m.forward[4] != 3 || m.forward[2] != 0 || m.forward[3] != 0 || m.removed[2] || m.removed[3] {
		t.Fatalf("duplicate line alignment must be unknown: %+v", m)
	}
	m = cumulativeLineMapping("a\n{\nx\n}\nb\n", "a\n{\ny\n}\nb\n")
	if m.forward[2] != 2 || m.forward[4] != 4 || !m.removed[3] || !m.added[3] {
		t.Fatalf("context should establish forced matches: %+v", m)
	}
}

func TestCumulativeLineMappingBoundedFallback(t *testing.T) {
	var text strings.Builder
	for i := 0; i < 1500; i++ {
		fmt.Fprintf(&text, "line %d\n", i)
	}
	before, after := text.String(), "new\n"+text.String()
	m := cumulativeLineMapping(before, after)
	if len(m.forward) != 0 || !m.added[1] {
		t.Fatalf("bounded fallback must keep only provable absence: %+v", m)
	}
	m = cumulativeLineMapping(before, before)
	if len(m.forward) != 1500 {
		t.Fatal("identical large file needs no costly mapping")
	}
}
