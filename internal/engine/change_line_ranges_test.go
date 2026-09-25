package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"onebyone/internal/model"
)

func TestExactEditLineRanges(t *testing.T) {
	for _, test := range []struct {
		name   string
		before string
		edits  []model.Edit
		want   []model.EditLineRange
	}{
		{"anchors-trimmed", "head\nold\ntail\n", []model.Edit{{OldText: "head\nold\ntail", NewText: "head\nnew\ntail", ItemIDs: []string{"P1"}}}, []model.EditLineRange{{ItemIDs: []string{"P1"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2}}}},
		{"insert-whole-lines-and-shift-later-edit", "first\nlast\n", []model.Edit{{OldText: "last", NewText: "final", ItemIDs: []string{"P2"}}, {OldText: "first\n", NewText: "first\ninserted\nmore\n", ItemIDs: []string{"P1"}}}, []model.EditLineRange{{ItemIDs: []string{"P1"}, ChangeLineRange: model.ChangeLineRange{AfterStart: 2, AfterEnd: 3}}, {ItemIDs: []string{"P2"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 4, AfterEnd: 4}}}},
		{"delete-whole-lines", "head\ndelete\nmore\ntail\n", []model.Edit{{OldText: "delete\nmore\n", NewText: "", ItemIDs: []string{"P1"}}}, []model.EditLineRange{{ItemIDs: []string{"P1"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 3}}}},
		{"inline-insertion-has-both-lines", "head\ncall();\n", []model.Edit{{OldText: "call();", NewText: "await call();", ItemIDs: []string{"P1", "P2"}}}, []model.EditLineRange{{ItemIDs: []string{"P1", "P2"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2}}}},
		{"inline-deletion-has-both-lines", "head\nawait call();\n", []model.Edit{{OldText: "await call();", NewText: "call();", ItemIDs: []string{"P1"}}}, []model.EditLineRange{{ItemIDs: []string{"P1"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2}}}},
		{"two-edits-on-same-line", "oldA(); oldB();\n", []model.Edit{{OldText: "oldB", NewText: "newB", ItemIDs: []string{"P2"}}, {OldText: "oldA", NewText: "newA", ItemIDs: []string{"P1"}}}, []model.EditLineRange{{ItemIDs: []string{"P1"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 1, BeforeEnd: 1, AfterStart: 1, AfterEnd: 1}}, {ItemIDs: []string{"P2"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 1, BeforeEnd: 1, AfterStart: 1, AfterEnd: 1}}}},
		{"unicode-without-final-newline", "日本語\n旧コード", []model.Edit{{OldText: "旧コード", NewText: "新コード", ItemIDs: []string{"P1"}}}, []model.EditLineRange{{ItemIDs: []string{"P1"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2}}}},
		{"legacy-edits-without-links", "old\n", []model.Edit{{OldText: "old", NewText: "new"}}, []model.EditLineRange{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			after, err := applyEdits(test.before, test.edits)
			if err != nil {
				t.Fatal(err)
			}
			got := exactEditLineRanges(test.before, after, test.edits)
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ranges = %+v; want %+v", got, test.want)
			}
		})
	}
}

func TestCandidateValidationPersistsVerifiedLineAttribution(t *testing.T) {
	in := candidateFixture(t, "\ufeffheader\r\nLegacy.Save()\r\ntail\r\n")
	request := candidateRequest(in, "Legacy.Save()", "Modern.Save()\nnotify()")
	request.Edits[0].ItemIDs = []string{"P01", "P02"}
	result, err := validateCandidate(context.Background(), in, request)
	if err != nil || !result.Passed || len(result.EditRanges) != 1 {
		t.Fatalf("candidate: %+v %v", result, err)
	}
	want := model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 3}
	if result.EditRanges[0].ChangeLineRange != want || !reflect.DeepEqual(result.EditRanges[0].ItemIDs, request.Edits[0].ItemIDs) {
		t.Fatalf("wrong actual line attribution: %+v", result.EditRanges)
	}
	var stored model.CandidateValidation
	if err := json.Unmarshal([]byte(readTest(t, in.Artifact+".candidate-"+result.CandidateID+".validation.json")), &stored); err != nil || !reflect.DeepEqual(stored.EditRanges, result.EditRanges) {
		t.Fatalf("persisted validation lost attribution: %+v %v", stored, err)
	}
	assertCandidateClean(t, in)
}

func TestExactEditLineRangesRejectsInconsistentSourceWithoutPanic(t *testing.T) {
	for _, test := range []struct {
		name, before, after string
		edits               []model.Edit
	}{
		{"short-after", "prefix\nold\n", "", []model.Edit{{OldText: "old", NewText: "new", ItemIDs: []string{"P1"}}}},
		{"different-after", "old\ntail\n", "new\nchanged outside edit\n", []model.Edit{{OldText: "old", NewText: "new", ItemIDs: []string{"P1"}}}},
		{"overlapping-edits", "first\nsecond\n", "replacement", []model.Edit{{OldText: "first\nsecond", NewText: "whole", ItemIDs: []string{"P1"}}, {OldText: "second", NewText: "other", ItemIDs: []string{"P2"}}}},
		{"ambiguous-anchor", "old\nold\n", "new\nold\n", []model.Edit{{OldText: "old", NewText: "new", ItemIDs: []string{"P1"}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := exactEditLineRanges(test.before, test.after, test.edits); got != nil {
				t.Fatalf("inconsistent source produced attribution: %+v", got)
			}
		})
	}
}

func TestChangeReportLineAttributionUsesMatchingCandidateOnly(t *testing.T) {
	h, c := changeReportFixture()
	span := model.ChangeLineRange{BeforeStart: 4, BeforeEnd: 4, AfterStart: 5, AfterEnd: 6}
	c.State.LastCandidate.Result.EditRanges = []model.EditLineRange{{ItemIDs: []string{"save"}, ChangeLineRange: span}, {ItemIDs: []string{"other-item"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 99, BeforeEnd: 99}}}
	c.State.Plan.Items[0].Location = "invented line 1000"
	rows := buildChangeReport(h, c)
	if len(rows[0].LineRanges) != 1 || rows[0].LineRanges[0] != span {
		t.Fatalf("report used prose or another item's range: %+v", rows)
	}
	c.State.LastCandidate.Result.CandidateHash = "different-candidate"
	if got := buildChangeReport(h, c); len(got[0].LineRanges) != 0 {
		t.Fatalf("stale candidate range leaked into report: %+v", got)
	}
	c.State.LastCandidate.Result.EditRanges = nil
	if got := buildChangeReport(h, c); len(got[0].LineRanges) != 0 {
		t.Fatal("invented coordinates for old report")
	}
	task := model.Task{History: []model.Attempt{{Changes: rows}}}
	copy := copyTask(task)
	copy.History[0].Changes[0].LineRanges[0].BeforeStart = 999
	if task.History[0].Changes[0].LineRanges[0] != span {
		t.Fatal("copied report ranges alias live state")
	}
}
