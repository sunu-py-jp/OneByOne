package engine

import (
	"fmt"
	"strings"

	"onebyone/internal/model"
)

func changeReportTitles(rules []model.Rule) map[string]string {
	titles := make(map[string]string, len(rules))
	for _, rule := range rules {
		titles[rule.ID] = rule.Title
	}
	return titles
}

// buildChangeReport never authorizes adoption. A plan is only the editor's
// claim, and an accepted candidate still is not fixed until it is committed.
func buildChangeReport(h model.Attempt, c *repairCheckpoint) []model.ChangeReportItem {
	if c == nil || c.AttemptID != h.ID || c.InputHash != h.InputHash || c.BaseCommit != h.BaseCommit {
		return ensureChangeReportRows(h, nil)
	}
	plan, candidate := c.State.Plan, c.State.LastCandidate
	fixed := map[string]bool{}
	matching := candidate != nil && candidate.Request.BaseHash == h.InputHash && candidate.Request.PlanRevision == plan.Revision && candidate.Result.PlanRevision == plan.Revision && candidate.Result.CandidateID != ""
	adopted := h.Outcome == "done" && h.Commit != "" && h.OutputHash != "" && matching && candidate.Result.Passed && candidate.Result.CandidateHash == h.OutputHash && len(candidate.Result.Checks) > 0 && checksPass(candidate.Result.Checks) && passedIndependentReview(candidate, h.InputHash, h.OutputHash, plan)
	if adopted {
		for _, id := range candidate.Request.AddressedItemIDs {
			fixed[id] = true
		}
	}
	var review *model.IndependentReview
	if matching && candidate.Review != nil {
		r := candidate.Review
		if r.CandidateID == candidate.Result.CandidateID && r.CandidateHash == candidate.Result.CandidateHash && r.BaseHash == h.InputHash && r.PlanRevision == plan.Revision {
			review = r
		}
	}
	decisions := map[string]model.PlanDecision{}
	for _, d := range plan.RuleDecisions {
		decisions[d.RuleID] = d
	}
	terminal := h.Outcome != "" && h.Outcome != "running" && h.Outcome != "validated"
	rows := make([]model.ChangeReportItem, 0, len(plan.Items)+len(plan.RuleDecisions))
	for _, item := range plan.Items {
		row := model.ChangeReportItem{ID: item.ID, RuleID: item.RuleID, RuleTitle: c.RuleTitles[item.RuleID], Location: item.Location, Risk: item.Risk, Change: item.Change, Expected: item.Expected, Status: "pending"}
		if matching && candidate.Result.CandidateHash == h.OutputHash && h.OutputHash != "" {
			for _, span := range candidate.Result.EditRanges {
				for _, id := range span.ItemIDs {
					if id == item.ID {
						row.LineRanges = append(row.LineRanges, span.ChangeLineRange)
						break
					}
				}
			}
		}
		switch {
		case item.Status == "blocked" || decisions[item.RuleID].Decision == "blocked":
			row.Status = "needs_human"
			row.Reason = firstReportText(item.HoldReason, decisions[item.RuleID].Reason, h.Note)
		case fixed[item.ID]:
			row.Status, row.Reason = "fixed", "機械検証と独立レビューを通過した修正をコミットしました"
		case terminal:
			row.Status = "not_applied"
			row.Reason = firstReportText(h.Note, "この試行の修正は採用されていません")
		default:
			row.Reason = "修正・検証中です。まだ採用されていません"
		}
		rows = append(rows, row)
	}
	for _, d := range plan.RuleDecisions {
		if d.Decision == "blocked" && !reportHasRule(rows, d.RuleID) {
			rows = append(rows, model.ChangeReportItem{ID: "rule:" + d.RuleID, RuleID: d.RuleID, RuleTitle: c.RuleTitles[d.RuleID], Status: "needs_human", Reason: d.Reason})
		}
		if d.Decision == "no_change" && (adopted || verifiedSkipReport(h)) {
			rows = append(rows, model.ChangeReportItem{ID: "rule:" + d.RuleID, RuleID: d.RuleID, RuleTitle: c.RuleTitles[d.RuleID], Status: "unchanged", Reason: d.Reason})
		}
	}
	if review == nil || review.Verdict == "passed" || review.Verdict == "running" || review.Verdict == "error" {
		return ensureChangeReportRows(h, rows)
	}
	for _, assessment := range review.Assessments {
		if assessment.Status != "needs_human" {
			continue
		}
		// A rule-level assessment cannot identify which of that rule's editor
		// items is held. Preserve its scope instead of inventing that mapping.
		rows = append(rows, model.ChangeReportItem{ID: "review-rule:" + assessment.RuleID, RuleID: assessment.RuleID, RuleTitle: c.RuleTitles[assessment.RuleID], Location: "ルール全体", Status: "needs_human", Reason: "独立レビュー: " + assessment.Reason})
	}
	for i, issue := range review.Issues {
		// Reviewer locations are separate evidence. Do not guess which editor
		// item an issue concerns merely because they use the same rule.
		status := "not_applied"
		if review.Verdict == "needs_human" {
			status = "needs_human"
		}
		rows = append(rows, model.ChangeReportItem{ID: fmt.Sprintf("review:%s:%d", review.ID, i+1), RuleID: issue.RuleID, RuleTitle: c.RuleTitles[issue.RuleID], Location: issue.Location, Risk: issue.Reason, Change: issue.RequestedChange, Status: status, Reason: "独立レビュー: " + issue.Reason})
	}
	return ensureChangeReportRows(h, rows)
}

func ensureChangeReportRows(h model.Attempt, rows []model.ChangeReportItem) []model.ChangeReportItem {
	if len(rows) > 0 || strings.TrimSpace(h.Note) == "" {
		return rows
	}
	status := ""
	switch h.Outcome {
	case "needs_human", "interrupted":
		status = "needs_human"
	case "failed":
		status = "not_applied"
	}
	if status == "" {
		return rows
	}
	return []model.ChangeReportItem{{ID: "file:" + h.ID, Location: "ファイル全体（具体箇所は未特定）", Status: status, Reason: h.Note}}
}

func reportHasRule(rows []model.ChangeReportItem, ruleID string) bool {
	for _, row := range rows {
		if row.RuleID == ruleID {
			return true
		}
	}
	return false
}

func firstReportText(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func verifiedSkipReport(h model.Attempt) bool {
	if h.Outcome != "skipped" || !checksPass(h.Checks) {
		return false
	}
	for _, check := range h.Checks {
		if check.Status == "passed" {
			return true
		}
	}
	return false
}

// Older queues contain only the checkpoint path. Read their own immutable
// attempt metadata on selection/export; a missing record is not an invented
// repair, risk, or rule title. Existing snapshots retain their historical title.
func backfillChangeReports(cfg model.Config, tasks []model.Task) {
	for i := range tasks {
		for j := range tasks[i].History {
			h := &tasks[i].History[j]
			if h.Changes != nil {
				continue
			}
			c, err := loadRepairCheckpoint(cfg, *h)
			if err == nil && c != nil && c.File == tasks[i].File {
				h.Changes = buildChangeReport(*h, c)
			} else {
				h.Changes = ensureChangeReportRows(*h, nil)
			}
		}
	}
}
