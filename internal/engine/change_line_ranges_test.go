package engine

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
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
		{"inline-insertion-has-both-lines", "head\ncall();\n", []model.Edit{{OldText: "call();", NewText: "await call();", ItemIDs: []string{"P1", "P2"}, Attributions: []model.EditAttribution{{ItemID: "P1", BeforeText: "call();", AfterText: "await call();"}, {ItemID: "P2", BeforeText: "call();", AfterText: "await call();"}}}}, []model.EditLineRange{{ItemIDs: []string{"P1", "P2"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2}}}},
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
	request.AddressedItemIDs = []string{"P01", "P02"}
	request.Edits[0].Attributions = []model.EditAttribution{
		{ItemID: "P01", BeforeText: "Legacy.Save()", AfterText: "Modern.Save()\nnotify()"},
		{ItemID: "P02", BeforeText: "Legacy.Save()", AfterText: "Modern.Save()\nnotify()"},
	}
	result, err := validateCandidate(context.Background(), in, request)
	if err != nil || !result.Passed || len(result.EditRanges) != 1 || result.AttributionVersion != model.LineAttributionVersion {
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
	c.State.LastCandidate.Result.AttributionVersion = model.LineAttributionVersion
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

func TestVerifiedEditLineRangesSeparatesAPIAndFlushItems(t *testing.T) {
	before := "// header\nconst writer = ReportWriter.open(name);\nwriter.add(invoice);\nnotifySaved(invoice.id);\nwriter.finish();\n// tail\n"
	old := "const writer = ReportWriter.open(name);\nwriter.add(invoice);\nnotifySaved(invoice.id);\nwriter.finish();"
	newText := "const writer = reports.open(name);\nwriter.write(invoice);\nawait writer.flush();\nnotifySaved(invoice.id);\nwriter.close();"
	edit := model.Edit{OldText: old, NewText: newText, ItemIDs: []string{"api", "flush"}, Attributions: []model.EditAttribution{
		{ItemID: "api", BeforeText: "ReportWriter.open(name)", AfterText: "reports.open(name)"},
		{ItemID: "api", BeforeText: "writer.add(invoice)", AfterText: "writer.write(invoice)"},
		{ItemID: "api", BeforeText: "writer.finish()", AfterText: "writer.close()"},
		{ItemID: "flush", BeforeText: "notifySaved(invoice.id);", AfterText: "await writer.flush();\nnotifySaved(invoice.id);"},
	}}
	after, err := applyEdits(before, []model.Edit{edit})
	if err != nil {
		t.Fatal(err)
	}
	got, err := verifiedEditLineRanges(before, after, []model.Edit{edit})
	want := []model.EditLineRange{
		{ItemIDs: []string{"api"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2}},
		{ItemIDs: []string{"api"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 3, BeforeEnd: 3, AfterStart: 3, AfterEnd: 3}},
		{ItemIDs: []string{"flush"}, ChangeLineRange: model.ChangeLineRange{AfterStart: 4, AfterEnd: 4}},
		{ItemIDs: []string{"api"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 5, BeforeEnd: 5, AfterStart: 6, AfterEnd: 6}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("item-specific positions = %+v, %v; want %+v", got, err, want)
	}
}

func TestVerifiedEditLineRangesSplitsUnchangedMiddleLines(t *testing.T) {
	before := "header\noldA();\nkeep();\noldB();\ntail\n"
	edit := model.Edit{OldText: before, NewText: "header\nnewA();\nkeep();\nnewB();\ntail\n", ItemIDs: []string{"structure"}}
	got, err := verifiedEditLineRanges(before, edit.NewText, []model.Edit{edit})
	want := []model.EditLineRange{
		{ItemIDs: []string{"structure"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2, AfterStart: 2, AfterEnd: 2}},
		{ItemIDs: []string{"structure"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 4, BeforeEnd: 4, AfterStart: 4, AfterEnd: 4}},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("separate structural blocks = %+v, %v; want %+v", got, err, want)
	}
}

func TestVerifiedEditLineRangesRejectsUnverifiableAttributions(t *testing.T) {
	base := func() model.Edit {
		return model.Edit{OldText: "oldA();\nkeep();\noldB();\n", NewText: "newA();\nkeep();\nnewB();\n", ItemIDs: []string{"api", "other"}, Attributions: []model.EditAttribution{
			{ItemID: "api", BeforeText: "oldA()", AfterText: "newA()"},
			{ItemID: "other", BeforeText: "oldB()", AfterText: "newB()"},
		}}
	}
	for _, test := range []struct {
		name    string
		mutate  func(*model.Edit)
		message string
	}{
		{"missing", func(e *model.Edit) { e.Attributions = nil }, "require item-specific"},
		{"unknown-item", func(e *model.Edit) { e.Attributions[0].ItemID = "invented" }, "unknown itemID"},
		{"uncovered-item", func(e *model.Edit) { e.ItemIDs = append(e.ItemIDs, "missing") }, "no fragment"},
		{"invented-source", func(e *model.Edit) { e.Attributions[0].BeforeText = "not in source" }, "uniquely match"},
		{"ambiguous-anchor", func(e *model.Edit) { e.Attributions[0].BeforeText = "old" }, "uniquely match"},
		{"empty-anchor", func(e *model.Edit) { e.Attributions[0].BeforeText = "" }, "nonempty beforeText"},
		{"noop", func(e *model.Edit) { e.Attributions[0].AfterText = e.Attributions[0].BeforeText }, "real change"},
		{"duplicate-item-pair", func(e *model.Edit) { e.Attributions = append(e.Attributions, e.Attributions[0]) }, "duplicate fragment"},
		{"incomplete-output", func(e *model.Edit) { e.NewText += "unattributed();\n" }, "reproduce all"},
		{"false-output", func(e *model.Edit) { e.Attributions[0].AfterText = "invented()" }, "reproduce all"},
		{"partial-overlap", func(e *model.Edit) {
			e.Attributions[1] = model.EditAttribution{ItemID: "other", BeforeText: "oldA();\nkeep();\noldB();", AfterText: "newA();\nkeep();\nnewB();"}
		}, "partially overlap"},
		{"disconnected-multi-item", func(e *model.Edit) {
			e.Attributions = []model.EditAttribution{{ItemID: "api", BeforeText: e.OldText, AfterText: e.NewText}, {ItemID: "other", BeforeText: e.OldText, AfterText: e.NewText}}
		}, "separate changes"},
		{"duplicate-item-id", func(e *model.Edit) { e.ItemIDs = []string{"api", "api"} }, "unique"},
		{"no-item-id", func(e *model.Edit) { e.ItemIDs = nil }, "identify its plan items"},
	} {
		t.Run(test.name, func(t *testing.T) {
			edit := base()
			test.mutate(&edit)
			if got, err := verifiedEditLineRanges(edit.OldText, edit.NewText, []model.Edit{edit}); err == nil || !strings.Contains(err.Error(), test.message) || got != nil {
				t.Fatalf("invalid attribution accepted: %+v, %v; expected %q", got, err, test.message)
			}
		})
	}
}

func TestVerifiedEditLineRangesBoundsDiffWork(t *testing.T) {
	before, after := strings.Repeat("old\n", 1100), strings.Repeat("new\n", 1100)
	edit := model.Edit{OldText: before, NewText: after, ItemIDs: []string{"rewrite"}}
	if _, err := verifiedEditLineRanges(before, after, []model.Edit{edit}); err == nil || !strings.Contains(err.Error(), "split") {
		t.Fatalf("unbounded diff accepted: %v", err)
	}
	// Pure insertions have no quadratic comparison and may be large.
	edit.OldText, edit.NewText = "anchor\n", "anchor\n"+after
	if _, err := verifiedEditLineRanges(edit.OldText, edit.NewText, []model.Edit{edit}); err != nil {
		t.Fatalf("large pure insertion should remain cheap: %v", err)
	}
}
