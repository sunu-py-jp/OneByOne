package agent

import (
	"errors"
	"fmt"
	"strings"

	"onebyone/internal/model"
)

// CandidateHolds rejects unlocalized/whole-file uncertainty. A rule containing
// both fixable and held locations uses decision=modify with blocked items.
func CandidateHolds(plan model.RepairPlan) ([]model.ReviewHold, error) {
	holds := []model.ReviewHold{}
	localized := map[string]bool{}
	for _, item := range plan.Items {
		if item.Status != "blocked" {
			continue
		}
		if len(item.SourceLocations) == 0 || strings.TrimSpace(item.HoldReason) == "" {
			return nil, fmt.Errorf("item %q is blocked; exact sourceLocations and holdReason are required before partial adoption", item.ID)
		}
		localized[item.RuleID] = true
		holds = append(holds, model.ReviewHold{ItemID: item.ID, RuleID: item.RuleID, SourceLocations: append([]model.SourceLocation(nil), item.SourceLocations...), Reason: item.HoldReason})
	}
	for _, decision := range plan.RuleDecisions {
		if decision.Decision == "blocked" && !localized[decision.RuleID] {
			return nil, fmt.Errorf("rule %q is blocked without a localized hold; whole-file uncertainty requires needs_human", decision.RuleID)
		}
	}
	return holds, nil
}

// CheckCandidateHolds enforces unchanged, complete original held lines. Broad
// replacement context may include those lines when they remain an exact unique
// block in the replacement; merely overlapping context is not a change.
func CheckCandidateHolds(plan model.RepairPlan, request model.CandidateRequest, original string) error {
	holds, err := CandidateHolds(plan)
	if err != nil {
		return err
	}
	if err := validatePlanSourceLocations(plan, original); err != nil {
		return err
	}
	if len(holds) == 0 {
		return nil
	}
	if err := validateExactEdits(request.Edits, original); err != nil {
		return err
	}
	lineStarts := []int{0}
	for i, b := range []byte(original) {
		if b == '\n' {
			lineStarts = append(lineStarts, i+1)
		}
	}
	for _, hold := range holds {
		for _, loc := range hold.SourceLocations {
			start, end := lineStarts[loc.StartLine-1], len(original)
			if loc.EndLine < len(lineStarts) {
				end = lineStarts[loc.EndLine]
			}
			protected := original[start:end]
			for _, edit := range request.Edits {
				at := strings.Index(original, edit.OldText)
				finish := at + len(edit.OldText)
				if finish < start || at > end {
					continue
				}
				// Extend this edit to the full protected lines, preserving unchanged
				// surrounding bytes. This catches partial-line edits and newline removal.
				left, right := min(at, start), max(finish, end)
				before := original[left:right]
				after := original[left:at] + edit.NewText + original[finish:right]
				relative := start - left
				if strings.Count(before, protected) != 1 || strings.Count(after, protected) != 1 {
					return fmt.Errorf("candidate changes or ambiguously relocates held item %q at original lines %d-%d; retain those complete lines", hold.ItemID, loc.StartLine, loc.EndLine)
				}
				pos := strings.Index(after, protected)
				// A preserved block cannot silently absorb a new prefix on its first line.
				if (relative == 0 && left == 0 || left+relative > 0 && original[left+relative-1] == '\n') && pos > 0 && after[pos-1] != '\n' {
					return fmt.Errorf("candidate modifies the first line of held item %q", hold.ItemID)
				}
				if !strings.HasSuffix(protected, "\n") && pos+len(protected) < len(after) && after[pos+len(protected)] != '\n' {
					return fmt.Errorf("candidate modifies the last line of held item %q", hold.ItemID)
				}
			}
		}
	}
	return nil
}

// CheckPartialReview is reusable at persisted-artifact/adoption boundaries.
// The caller separately verifies review identity, candidate hashes and edits.
func CheckPartialReview(review model.IndependentReview, plan model.RepairPlan) error {
	holds, err := CandidateHolds(plan)
	if err != nil {
		return err
	}
	if len(review.Assessments) != len(plan.RuleDecisions) {
		return errors.New("partial review omitted rule assessments")
	}
	known := map[string]bool{}
	for _, d := range plan.RuleDecisions {
		known[d.RuleID] = true
	}
	for _, a := range review.Assessments {
		if !known[a.RuleID] {
			return errors.New("partial review assessed an unknown rule")
		}
	}
	return checkReviewHoldApproval(review, holds)
}

func checkReviewHoldApproval(review model.IndependentReview, holds []model.ReviewHold) error {
	if review.Verdict != "passed_with_holds" || len(holds) == 0 {
		return errors.New("partial adoption requires passed_with_holds and localized holds")
	}
	known := map[string]model.ReviewHold{}
	heldRules := map[string]bool{}
	for _, hold := range holds {
		known[hold.ItemID] = hold
		heldRules[hold.RuleID] = true
	}
	if len(review.HoldAssessments) != len(holds) {
		return errors.New("partial review must assess every held item")
	}
	seen := map[string]bool{}
	for _, assessment := range review.HoldAssessments {
		if _, ok := known[assessment.ItemID]; !ok || seen[assessment.ItemID] || assessment.Status != "preserved" || strings.TrimSpace(assessment.Reason) == "" {
			return errors.New("partial review must explicitly confirm unchanged held scope and independent safety for every held item")
		}
		seen[assessment.ItemID] = true
	}
	assessed := map[string]bool{}
	for _, a := range review.Assessments {
		if assessed[a.RuleID] || strings.TrimSpace(a.Reason) == "" || (a.Status != "satisfied" && a.Status != "not_applicable" && a.Status != "needs_human") {
			return errors.New("partial review contains an invalid or duplicate rule assessment")
		}
		if a.Status == "violated" || a.Status == "needs_human" && !heldRules[a.RuleID] {
			return errors.New("partial review contains a violation or an undeclared human hold")
		}
		if heldRules[a.RuleID] && a.Status != "needs_human" {
			return errors.New("partial review cannot clear the editor's human hold")
		}
		assessed[a.RuleID] = true
	}
	for id := range heldRules {
		if !assessed[id] {
			return errors.New("partial review omitted a held rule")
		}
	}
	for _, issue := range review.Issues {
		if issue.Kind != "needs_human" || issue.LineBasis != "before" || !issueWithinHolds(issue, holds) {
			return errors.New("partial review has an issue outside the declared original held scope")
		}
	}
	return nil
}

func issueWithinHolds(issue model.ReviewIssue, holds []model.ReviewHold) bool {
	for _, hold := range holds {
		if hold.RuleID != issue.RuleID {
			continue
		}
		for _, loc := range hold.SourceLocations {
			if issue.StartLine >= loc.StartLine && issue.EndLine >= issue.StartLine && issue.EndLine <= loc.EndLine {
				within := model.SourceLocation{StartLine: issue.StartLine - loc.StartLine + 1, EndLine: issue.EndLine - loc.StartLine + 1, Excerpt: issue.Excerpt}
				if model.ValidateSourceLocation(loc.Excerpt, within) == nil {
					return true
				}
			}
		}
	}
	return false
}

// A localized review finding is returned to the editor for replanning. We do not
// silently turn a reviewer finding into approval or mutate the editor's plan.
func canReplanReviewHold(review model.IndependentReview, candidate *model.CandidateRecord, original string) bool {
	if candidate.NoChange || len(candidate.Request.Edits) == 0 {
		for _, issue := range review.Issues {
			if issue.Kind == "needs_changes" {
				return true
			}
		}
		return false
	}
	holds := map[string]bool{}
	for _, a := range review.Assessments {
		if a.Status == "needs_human" {
			holds[a.RuleID] = false
		}
	}
	for _, issue := range review.Issues {
		if issue.Kind == "needs_human" {
			holds[issue.RuleID] = false
		}
	}
	if len(holds) == 0 {
		return false
	}
	for _, issue := range review.Issues {
		if issue.Kind != "needs_human" && issue.Kind != "" {
			continue
		}
		if _, ok := holds[issue.RuleID]; !ok {
			return false
		}
		if issue.LineBasis == "after" {
			// Only an exact unique full-line match in the original can be replanned;
			// candidate-only code cannot serve as protected original evidence.
			if strings.Count(original, strings.TrimSuffix(issue.Excerpt, "\n")) != 1 {
				return false
			}
			if _, err := model.ResolveSourceLocation(original, model.SourceLocation{StartLine: issue.StartLine, EndLine: issue.EndLine, Excerpt: issue.Excerpt}); err != nil {
				return false
			}
		} else if issue.LineBasis != "before" {
			return false
		}
		holds[issue.RuleID] = true
	}
	for _, found := range holds {
		if !found {
			return false
		}
	}
	return true
}

func validateReviewHolds(holds []model.ReviewHold, original string, rules map[string]bool) error {
	seen := map[string]bool{}
	for _, h := range holds {
		if !validRuleID(h.ItemID) || seen[h.ItemID] || !rules[h.RuleID] || !validPlanText(h.Reason) || len(h.SourceLocations) == 0 {
			return errors.New("Independent review requires unique, concrete held scopes")
		}
		seen[h.ItemID] = true
		for _, loc := range h.SourceLocations {
			if err := model.ValidateSourceLocation(original, loc); err != nil {
				return fmt.Errorf("Independent review held source: %w", err)
			}
		}
	}
	return nil
}

func validateReviewHoldAssessments(assessments []model.ReviewHoldAssessment, holds []model.ReviewHold) error {
	if len(assessments) != len(holds) {
		return errors.New("Independent review must assess each declared held item")
	}
	known, seen := map[string]bool{}, map[string]bool{}
	for _, h := range holds {
		known[h.ItemID] = true
	}
	for _, a := range assessments {
		if !known[a.ItemID] || seen[a.ItemID] || (a.Status != "preserved" && a.Status != "unsafe") || !validPlanText(a.Reason) {
			return errors.New("Independent review held-item assessment is invalid")
		}
		seen[a.ItemID] = true
	}
	return nil
}
