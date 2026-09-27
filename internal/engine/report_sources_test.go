package engine

import (
	"path/filepath"
	"reflect"
	"testing"

	"onebyone/internal/model"
)

func reportSourceFixture(t *testing.T, before, after string) (model.Config, model.Attempt, *repairCheckpoint) {
	t.Helper()
	cfg := model.Config{QueuePath: filepath.Join(t.TempDir(), "queue.jsonl"), Root: t.TempDir()}
	h, c := changeReportFixture()
	h.InputHash, h.OutputHash = digest([]byte(before)), digest([]byte(after))
	h.Outcome, h.Commit = "needs_human", ""
	c.InputHash = h.InputHash
	candidate := c.State.LastCandidate
	candidate.Request.BaseHash = h.InputHash
	candidate.Result.CandidateHash = h.OutputHash
	candidate.Review.BaseHash, candidate.Review.CandidateHash = h.InputHash, h.OutputHash
	candidate.Review.Verdict = "needs_human"
	candidate.Review.Assessments[0].Status = "needs_human"
	statePath, err := repairPath(cfg, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(statePath)
	writeTest(t, filepath.Join(dir, h.ID+".before"), []byte(before))
	writeTest(t, filepath.Join(dir, h.ID+".candidate-"+candidate.Result.CandidateID+".after"), []byte(after))
	return cfg, h, c
}

func TestRecordedHeldPlanLocationsNeedExactImmutableSource(t *testing.T) {
	for _, original := range []string{"start\n  shared.release();\nend\n", "\ufeffstart\r\n  shared.release();\r\nend\r\n"} {
		t.Run(original, func(t *testing.T) {
			cfg, h, c := reportSourceFixture(t, original, "")
			c.State.LastCandidate = nil
			h.OutputHash = ""
			item := &c.State.Plan.Items[0]
			item.Status, item.HoldReason = "blocked", "所有権不明"
			item.SourceLocations = []model.SourceLocation{{StartLine: 2, EndLine: 2, Excerpt: "  shared.release();"}}
			rows := buildRecordedChangeReport(cfg, h, c)
			want := []model.ChangeLineRange{{BeforeStart: 2, BeforeEnd: 2}}
			if len(rows) != 1 || rows[0].Status != "needs_human" || rows[0].AttributionVersion != model.LineAttributionVersion || !reflect.DeepEqual(rows[0].LineRanges, want) {
				t.Fatalf("held item without a candidate lost original positions: %+v", rows)
			}
			// Persisted history is validated strictly, never relocated from an
			// excerpt which happens to occur somewhere else in the input.
			item.SourceLocations[0].StartLine, item.SourceLocations[0].EndLine = 1, 1
			rows = buildRecordedChangeReport(cfg, h, c)
			if len(rows[0].LineRanges) != 0 || rows[0].AttributionVersion != 0 {
				t.Fatalf("incorrect historical coordinates were silently relocated: %+v", rows)
			}
			item.SourceLocations = nil
			item.Location = "line 2: shared.release()"
			if rows = buildRecordedChangeReport(cfg, h, c); len(rows[0].LineRanges) != 0 {
				t.Fatal("freeform legacy location became line evidence")
			}
		})
	}
}

func TestRecordedReviewPositionsUseTheirDeclaredCandidateSide(t *testing.T) {
	before, after := "head\nshared.release();\ntail\n", "head\nlog();\nshared.release();\ntail\n"
	for _, side := range []string{"before", "after"} {
		t.Run(side, func(t *testing.T) {
			cfg, h, c := reportSourceFixture(t, before, after)
			line := 2
			want := model.ChangeLineRange{BeforeStart: 2, BeforeEnd: 2}
			if side == "after" {
				line, want = 3, model.ChangeLineRange{AfterStart: 3, AfterEnd: 3}
			}
			c.State.LastCandidate.Review.Issues = []model.ReviewIssue{{RuleID: "R019", LineBasis: side, StartLine: line, EndLine: line, Excerpt: "shared.release();", Reason: "所有権不明", RequestedChange: "契約を確認する"}}
			rows := buildRecordedChangeReport(cfg, h, c)
			if len(rows) != 2 || rows[1].Status != "needs_human" || rows[1].AttributionVersion != model.LineAttributionVersion || !reflect.DeepEqual(rows[1].LineRanges, []model.ChangeLineRange{want}) {
				t.Fatalf("review issue mapped to wrong source or duplicate generic row: %+v", rows)
			}
		})
	}
}

func TestRecordedSourceRejectsStaleAndMissingArtifacts(t *testing.T) {
	for _, mutation := range []string{"input hash", "candidate hash", "different output", "missing candidate", "unsafe candidate", "checkpoint identity", "bad excerpt", "bad range"} {
		t.Run(mutation, func(t *testing.T) {
			cfg, h, c := reportSourceFixture(t, "head\nold\n", "head\nnew\n")
			candidate := c.State.LastCandidate
			candidate.Review.Issues = []model.ReviewIssue{{RuleID: "R019", LineBasis: "after", StartLine: 2, EndLine: 2, Excerpt: "new", Reason: "check ownership"}}
			switch mutation {
			case "input hash":
				c.InputHash, h.InputHash, candidate.Request.BaseHash, candidate.Review.BaseHash = "bad", "bad", "bad", "bad"
			case "candidate hash":
				candidate.Result.CandidateHash, candidate.Review.CandidateHash, h.OutputHash = "bad", "bad", "bad"
			case "different output":
				h.OutputHash = "newer candidate"
			case "missing candidate":
				candidate.Result.CandidateID, candidate.Review.CandidateID = "missing", "missing"
			case "unsafe candidate":
				candidate.Result.CandidateID, candidate.Review.CandidateID = "../candidate", "../candidate"
			case "checkpoint identity":
				c.AttemptID = "another-attempt"
			case "bad excerpt":
				candidate.Review.Issues[0].Excerpt = "head"
			case "bad range":
				candidate.Review.Issues[0].EndLine = 99
			}
			for _, row := range buildRecordedChangeReport(cfg, h, c) {
				if len(row.LineRanges) != 0 {
					t.Fatalf("unverified evidence received a source badge: %+v", row)
				}
			}
		})
	}
}

func TestRecordedNoChangeReviewHoldHasSourceWithoutOutputJournal(t *testing.T) {
	original := "start\nshared.release();\nend\n"
	cfg, h, c := reportSourceFixture(t, original, original)
	h.OutputHash = "" // No validate_candidate call runs for a no-change review.
	c.State.Plan.Items = nil
	c.State.Plan.RuleDecisions[0].Decision = "no_change"
	candidate := c.State.LastCandidate
	candidate.NoChange = true
	candidate.Review.Issues = []model.ReviewIssue{{RuleID: "R019", LineBasis: "after", StartLine: 2, EndLine: 2, Excerpt: "shared.release();", Reason: "所有権不明", RequestedChange: "契約を確認する"}}
	rows := buildRecordedChangeReport(cfg, h, c)
	if len(rows) != 1 || rows[0].Status != "needs_human" || rows[0].AttributionVersion != model.LineAttributionVersion || !reflect.DeepEqual(rows[0].LineRanges, []model.ChangeLineRange{{AfterStart: 2, AfterEnd: 2}}) {
		t.Fatalf("no-change review hold lost exact candidate-original location: %+v", rows)
	}
	candidate.Result.CandidateHash, candidate.Review.CandidateHash = "different", "different"
	if rows = buildRecordedChangeReport(cfg, h, c); len(rows) != 1 || len(rows[0].LineRanges) != 0 {
		t.Fatalf("no-change source exception accepted a different hash: %+v", rows)
	}
}

func TestRecordedReviewKeepsGenericHoldsOnlyWithoutSpecificIssues(t *testing.T) {
	cfg, h, c := reportSourceFixture(t, "old\n", "new\n")
	review := c.State.LastCandidate.Review
	review.Assessments = append(review.Assessments, model.ReviewAssessment{RuleID: "R020", Status: "needs_human", Reason: "別のルールの契約不明"})
	review.Issues = []model.ReviewIssue{{RuleID: "R019", LineBasis: "before", StartLine: 1, EndLine: 1, Excerpt: "old", Reason: "元の呼び出しの契約不明"}}
	rows := buildRecordedChangeReport(cfg, h, c)
	if len(rows) != 3 || rows[1].ID != "review-rule:R020" || rows[2].RuleID != "R019" || len(rows[2].LineRanges) != 1 {
		t.Fatalf("specific issue suppressed an unrelated rule or kept a duplicate: %+v", rows)
	}
}
