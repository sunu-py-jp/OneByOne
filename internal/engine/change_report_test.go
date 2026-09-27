package engine

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/model"
)

func changeReportFixture() (model.Attempt, *repairCheckpoint) {
	h := model.Attempt{ID: "report-attempt", BaseCommit: "base", InputHash: "before", OutputHash: "after", Outcome: "done", Commit: "commit", Note: "対応完了"}
	plan := model.RepairPlan{Revision: 1, RuleDecisions: []model.PlanDecision{{RuleID: "R019", Decision: "modify", Reason: "保存時の解放漏れ"}}, Items: []model.PlanItem{{ID: "save", RuleID: "R019", Location: "saveRecord", Risk: "接続が解放されず枯渇する", Change: "保存後に接続を解放する", Expected: "失敗時も一度だけ解放される", Status: "proposed"}}}
	c := &repairCheckpoint{Version: 1, AttemptID: h.ID, File: "A.txt", BaseCommit: h.BaseCommit, InputHash: h.InputHash, RuleTitles: map[string]string{"R019": "接続のライフサイクル"}, State: model.RepairState{Version: 1, Plan: plan}}
	c.State.LastCandidate = &model.CandidateRecord{Request: model.CandidateRequest{BaseHash: h.InputHash, PlanRevision: 1, AddressedItemIDs: []string{"save"}}, Result: model.CandidateValidation{AttributionVersion: model.LineAttributionVersion, CandidateID: "candidate", CandidateHash: h.OutputHash, PlanRevision: 1, Passed: true, Checks: []model.Check{{Name: "機械検証", Status: "passed"}}}, Review: &model.IndependentReview{ID: "review", CandidateID: "candidate", BaseHash: h.InputHash, CandidateHash: h.OutputHash, PlanRevision: 1, Verdict: "passed", FinishedAt: "finished", Assessments: []model.ReviewAssessment{{RuleID: "R019", Status: "satisfied", Reason: "正常系・異常系で解放される"}}}}
	return h, c
}

func TestChangeReportFixedRequiresAdoptedMatchingCandidate(t *testing.T) {
	h, c := changeReportFixture()
	rows := buildChangeReport(h, c)
	if len(rows) != 1 || rows[0].Status != "fixed" || rows[0].Risk != c.State.Plan.Items[0].Risk || rows[0].RuleTitle != "接続のライフサイクル" {
		t.Fatalf("adopted item report incomplete: %+v", rows)
	}
	for _, tc := range []struct {
		name string
		edit func(*model.Attempt, *repairCheckpoint)
	}{
		{"not committed", func(h *model.Attempt, c *repairCheckpoint) { h.Commit = "" }},
		{"held file", func(h *model.Attempt, c *repairCheckpoint) { h.Outcome = "needs_human" }},
		{"validated only", func(h *model.Attempt, c *repairCheckpoint) { h.Outcome = "validated" }},
		{"different final bytes", func(h *model.Attempt, c *repairCheckpoint) { h.OutputHash = "other" }},
		{"different plan", func(h *model.Attempt, c *repairCheckpoint) { c.State.Plan.Revision++ }},
		{"unaddressed item", func(h *model.Attempt, c *repairCheckpoint) { c.State.LastCandidate.Request.AddressedItemIDs = nil }},
		{"mechanical rejection", func(h *model.Attempt, c *repairCheckpoint) { c.State.LastCandidate.Result.Passed = false }},
		{"failed check", func(h *model.Attempt, c *repairCheckpoint) { c.State.LastCandidate.Result.Checks[0].Status = "failed" }},
		{"missing independent review", func(h *model.Attempt, c *repairCheckpoint) { c.State.LastCandidate.Review = nil }},
		{"different reviewed candidate", func(h *model.Attempt, c *repairCheckpoint) { c.State.LastCandidate.Review.CandidateID = "other" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c := changeReportFixture()
			tc.edit(&h, c)
			for _, row := range buildChangeReport(h, c) {
				if row.Status == "fixed" {
					t.Fatalf("unadopted claim reported fixed: %+v", row)
				}
			}
		})
	}
}

func TestChangeReportHeldFileDistinguishesBlockedAndUnadoptedItems(t *testing.T) {
	h, c := changeReportFixture()
	h.Outcome, h.Commit, h.Note = "needs_human", "", "契約の確認が必要なためファイル全体を保留"
	c.State.Plan.Items = append(c.State.Plan.Items, model.PlanItem{ID: "ownership", RuleID: "R019", Location: "loadRecord", Risk: "共有接続の二重解放", Change: "所有者を確認して解放する", Expected: "所有者だけが解放する", Status: "blocked", HoldReason: "呼び出し元の接続所有権が不明"})
	c.State.Plan.RuleDecisions = append(c.State.Plan.RuleDecisions, model.PlanDecision{RuleID: "R025", Decision: "blocked", Reason: "戻り値の契約が不明"})
	rows := buildChangeReport(h, c)
	if len(rows) != 3 || rows[0].Status != "not_applied" || rows[1].Status != "needs_human" || rows[1].Reason != "呼び出し元の接続所有権が不明" || rows[2].Status != "needs_human" || rows[2].RuleID != "R025" {
		t.Fatalf("held and unadopted work was conflated: %+v", rows)
	}
	if rows[2].Risk != "" {
		t.Fatal("report invented a risk absent from the old rule decision")
	}
}

func partialChangeReportFixture() (model.Attempt, *repairCheckpoint, reportSources) {
	h, c := changeReportFixture()
	h.Outcome, h.Partial, h.Note = "needs_human", true, "確認済みの修正を保存しました。共有接続の所有権は要確認です"
	c.State.Plan.Items = append(c.State.Plan.Items, model.PlanItem{ID: "ownership", RuleID: "R019", Location: "loadRecord", Risk: "共有接続の二重解放", Change: "所有者を確認して解放する", Expected: "所有者だけが解放する", Status: "blocked", HoldReason: "呼び出し元の接続所有権が不明", SourceLocations: []model.SourceLocation{{StartLine: 2, EndLine: 2, Excerpt: "releaseShared();"}}})
	c.State.LastCandidate.Request.Edits = []model.Edit{{OldText: "save();", NewText: "saveAndRelease();", ItemIDs: []string{"save"}}}
	c.State.LastCandidate.Result.EditRanges = []model.EditLineRange{{ItemIDs: []string{"save"}, ChangeLineRange: model.ChangeLineRange{BeforeStart: 1, BeforeEnd: 1, AfterStart: 1, AfterEnd: 1}}}
	r := c.State.LastCandidate.Review
	r.Verdict = "passed_with_holds"
	r.Assessments[0].Status, r.Assessments[0].Reason = "needs_human", "独立した保存接続の修正は適合し、共有接続の処理は未変更です"
	r.HoldAssessments = []model.ReviewHoldAssessment{{ItemID: "ownership", Status: "preserved", Reason: "保留範囲を変更せず、安全な保存接続の修正と独立しています"}}
	return h, c, reportSources{before: "save();\nreleaseShared();\n", after: "saveAndRelease();\nreleaseShared();\n", beforeValid: true, afterValid: true}
}

func TestChangeReportPartialAdoptionSeparatesFixedAndHeldWithinSameRule(t *testing.T) {
	h, c, sources := partialChangeReportFixture()
	rows := buildChangeReport(h, c, sources)
	if len(rows) != 2 || rows[0].Status != "fixed" || rows[1].Status != "needs_human" {
		t.Fatalf("partial adoption did not preserve fixed and held items (or duplicated reviewer scopes): %+v", rows)
	}
	if rows[0].RuleID != rows[1].RuleID || rows[1].Reason != "呼び出し元の接続所有権が不明" {
		t.Fatalf("same-rule safe/held scopes were conflated: %+v", rows)
	}
	if len(rows[0].LineRanges) != 1 || rows[0].LineRanges[0].AfterStart != 1 || len(rows[1].LineRanges) != 1 || rows[1].LineRanges[0].BeforeStart != 2 || rows[1].LineRanges[0].AfterStart != 0 {
		t.Fatalf("verified fixed and held source positions were lost: %+v", rows)
	}
}

func TestChangeReportPartialAdoptionRequiresCompleteAcceptanceEvidence(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*model.Attempt, *repairCheckpoint)
	}{
		{"not partial", func(h *model.Attempt, _ *repairCheckpoint) { h.Partial = false }},
		{"not committed", func(h *model.Attempt, _ *repairCheckpoint) { h.Commit = "" }},
		{"different output", func(h *model.Attempt, _ *repairCheckpoint) { h.OutputHash = "other" }},
		{"review rejected", func(_ *model.Attempt, c *repairCheckpoint) { c.State.LastCandidate.Review.Verdict = "needs_human" }},
		{"hold not assessed", func(_ *model.Attempt, c *repairCheckpoint) { c.State.LastCandidate.Review.HoldAssessments = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, c, sources := partialChangeReportFixture()
			tc.edit(&h, c)
			for _, row := range buildChangeReport(h, c, sources) {
				if row.Status == "fixed" {
					t.Fatalf("unverified subset was reported as fixed: %+v", row)
				}
			}
		})
	}
}

func TestChangeReportMixedReviewIssuesKeepTheirOwnDisposition(t *testing.T) {
	h, c := changeReportFixture()
	h.Outcome, h.Commit = "needs_human", ""
	r := c.State.LastCandidate.Review
	r.Verdict = "needs_changes"
	r.Issues = []model.ReviewIssue{
		{RuleID: "R019", Kind: "needs_human", Location: "shared", Reason: "所有権が不明"},
		{RuleID: "R019", Kind: "needs_changes", Location: "owned", Reason: "解放順が逆"},
	}
	rows := buildChangeReport(h, c)
	if len(rows) != 3 || rows[1].Status != "needs_human" || rows[2].Status != "not_applied" {
		t.Fatalf("review issues inherited the wrong aggregate disposition: %+v", rows)
	}
}

func TestChangeReportIncludesIndependentReviewHoldEvidence(t *testing.T) {
	h, c := changeReportFixture()
	h.Outcome, h.Commit = "needs_human", ""
	r := c.State.LastCandidate.Review
	r.Verdict, r.Summary = "needs_human", "所有権が不明"
	r.Assessments[0].Status, r.Assessments[0].Reason = "needs_human", "呼び出し元の契約を確認してください"
	r.Issues = []model.ReviewIssue{{RuleID: "R019", Location: "saveRecord", LineBasis: "after", Excerpt: "release()", Reason: "共有接続の解放となる可能性がある", RequestedChange: "接続の所有権を確認する"}}
	rows := buildChangeReport(h, c)
	if len(rows) != 2 || rows[0].Status != "not_applied" || rows[1].Status != "needs_human" || rows[1].Risk != r.Issues[0].Reason || rows[1].Location != "saveRecord" {
		t.Fatalf("review hold evidence missing: %+v", rows)
	}
	r.CandidateID = "superseded-candidate"
	rows = buildChangeReport(h, c)
	if len(rows) != 1 || rows[0].Status != "not_applied" {
		t.Fatalf("superseded review was attached to current work: %+v", rows)
	}
}

func TestChangeReportNoChangeClaimRequiresSuccessfulCompletion(t *testing.T) {
	h, c := changeReportFixture()
	c.State.Plan.Items = nil
	c.State.Plan.RuleDecisions[0].Decision = "no_change"
	h.Outcome, h.Commit = "failed", ""
	for _, row := range buildChangeReport(h, c) {
		if row.Status == "unchanged" {
			t.Fatal("failed self-reported no_change was promoted to verified unchanged")
		}
	}
	h.Outcome, h.OutputHash = "skipped", h.InputHash
	candidate := c.State.LastCandidate
	candidate.NoChange = true
	candidate.Request.Edits, candidate.Request.AddressedItemIDs = nil, nil
	candidate.Result.CandidateHash, candidate.Review.CandidateHash = h.InputHash, h.InputHash
	rows := buildChangeReport(h, c)
	if len(rows) != 1 || rows[0].Status != "unchanged" {
		t.Fatalf("independently reviewed unchanged source was not reported: %+v", rows)
	}
	candidate.Review = nil
	if rows := buildChangeReport(h, c); len(rows) != 0 {
		t.Fatalf("unverified old skipped claim was promoted: %+v", rows)
	}
}

func TestChangeReportEarlyHoldRetainsOnlyKnownReason(t *testing.T) {
	h := model.Attempt{ID: "early", Outcome: "needs_human", Note: "必要な呼び出し元を読み込めません"}
	rows := buildChangeReport(h, nil)
	if len(rows) != 1 || rows[0].Status != "needs_human" || rows[0].Reason != h.Note || rows[0].Risk != "" || rows[0].RuleID != "" {
		t.Fatalf("early hold was lost or invented evidence: %+v", rows)
	}
}

func TestChangeReportBackfillUsesOwnAttemptAndKeepsHistoricalTitles(t *testing.T) {
	cfg := model.Config{QueuePath: filepath.Join(t.TempDir(), "queue.jsonl"), Root: t.TempDir()}
	writeTest(t, filepath.Join(cfg.Root, "A.txt"), []byte("before"))
	h, c := changeReportFixture()
	path, err := saveRepairCheckpoint(cfg, c)
	if err != nil {
		t.Fatal(err)
	}
	h.RepairPath = path
	old := model.ChangeReportItem{ID: "old", RuleTitle: "変更前のルール名", Status: "not_applied"}
	tasks := []model.Task{{File: c.File, History: []model.Attempt{{ID: "old", Changes: []model.ChangeReportItem{old}}, h}}}
	backfillChangeReports(cfg, tasks)
	if len(tasks[0].History[1].Changes) != 1 || tasks[0].History[1].Changes[0].Status != "fixed" || !reflect.DeepEqual(tasks[0].History[0].Changes[0], old) {
		t.Fatalf("history snapshots were replaced or lost: %+v", tasks)
	}
	copy := copyTask(tasks[0])
	copy.History[1].Changes[0].Status = "needs_human"
	if tasks[0].History[1].Changes[0].Status != "fixed" {
		t.Fatal("copied report aliases live history")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	tasks[0].History[1].Changes = nil
	backfillChangeReports(cfg, tasks)
	if len(tasks[0].History[1].Changes) != 0 {
		t.Fatal("missing checkpoint invented report details")
	}
}

func TestExportReportEmbedsChangeAndRiskSnapshots(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	h, c := changeReportFixture()
	path, err := saveRepairCheckpoint(s.Snapshot().Config, c)
	if err != nil {
		t.Fatal(err)
	}
	h.RepairPath = path
	s.mu.Lock()
	s.state.Tasks[0].History = []model.Attempt{h}
	s.mu.Unlock()
	want := filepath.Join(t.TempDir(), "report.json")
	if _, err := s.ExportReport(want); err != nil {
		t.Fatal(err)
	}
	var report model.State
	body := readTest(t, want)
	if err := json.Unmarshal([]byte(body), &report); err != nil {
		t.Fatal(err)
	}
	rows := report.Tasks[0].History[0].Changes
	if len(rows) != 1 || rows[0].Risk != c.State.Plan.Items[0].Risk || rows[0].Status != "fixed" || strings.Contains(body, cfg.Credential) {
		t.Fatalf("export missing safe embedded report: %+v", rows)
	}
}
