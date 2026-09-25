package agent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"onebyone/internal/model"
)

// The review is invoked after the editor submits its final candidate. Reviewer
// messages are never part of the editor's history; only a rejection is returned.
func reviewFinal(ctx context.Context, in Input, state *model.RepairState, final model.Proposal, checkpoint, reserveElapsed func() error) (model.Proposal, bool, error) {
	if final.Outcome != "modified" {
		return final, true, nil
	}
	candidate := state.LastCandidate
	if candidate == nil || !candidate.Result.Passed || candidate.Result.CandidateID != final.CandidateID || candidate.Request.PlanRevision != state.Plan.Revision || candidate.Result.PlanRevision != state.Plan.Revision || candidate.Request.BaseHash != in.BaseHash {
		return model.Proposal{}, false, errors.New("Independent review requires the current mechanically validated candidate")
	}
	if candidate.Review != nil && candidate.Review.Verdict != "running" && candidate.Review.Verdict != "error" {
		return reviewedOutcome(final, candidate)
	}
	limit := in.Config.MaxAttempts
	if limit > 0 && in.BudgetBaseline.used(*state).ReviewCount >= limit {
		return model.Proposal{}, false, errors.New("今回の実行で独立レビューの回数上限に到達しました。再実行すると回数は0から始まります")
	}
	if in.Config.MaxTurns > 0 && in.BudgetBaseline.used(*state).Turns >= in.Config.MaxTurns {
		return model.Proposal{}, false, fmt.Errorf("独立レビューを実行するための残りターンがありません: %w", TurnLimitError(in.BudgetBaseline.used(*state).Turns, in.Config.MaxTurns))
	}
	after, err := reviewCandidateText(in.Content, candidate.Request.Edits)
	if err != nil {
		return model.Proposal{}, false, err
	}
	rules, err := reviewRules(in, state.Plan)
	if err != nil {
		return model.Proposal{}, false, err
	}
	candidate.ReviewRequested, candidate.ReviewNote = true, final.Note
	if err := checkpoint(); err != nil {
		return model.Proposal{}, false, err
	}
	reviewID, err := repairRequestID()
	if err != nil {
		return model.Proposal{}, false, &fatalError{err}
	}
	record := model.IndependentReview{ID: reviewID, CandidateID: candidate.Result.CandidateID, BaseHash: in.BaseHash, CandidateHash: candidate.Result.CandidateHash, PlanRevision: state.Plan.Revision, Verdict: "running", StartedAt: time.Now().UTC().Format(time.RFC3339Nano), Assessments: []model.ReviewAssessment{}, Issues: []model.ReviewIssue{}}
	index := -1
	publish := func() {
		copy := record
		candidate.Review = &copy
		if index < 0 {
			state.Reviews = append(state.Reviews, copy)
			index = len(state.Reviews) - 1
		} else {
			state.Reviews[index] = copy
		}
	}
	reviewer := in.ReviewCandidate
	if reviewer == nil {
		reviewer = Review
	}
	if in.Log != nil {
		in.Log("独立レビューを開始します")
	}
	result, reviewErr := reviewer(ctx, ReviewInput{Config: in.Config, File: in.File, Before: in.Content, After: after, BaseHash: in.BaseHash, CandidateHash: candidate.Result.CandidateHash, Rules: rules, Usage: state.Usage, TurnBaseline: in.BudgetBaseline.Turns, Log: in.Log,
		BeforeRequest: func(requestID string) error {
			state.ReviewCount++
			state.Usage.Turns++
			state.RequestPending, state.RequestID, state.RequestKind = true, requestID, "review"
			publish()
			return reserveElapsed()
		},
		AfterRequest: func(usage model.Usage, requestErr error) error {
			state.Usage.InputTokens += usage.InputTokens
			state.Usage.CachedTokens += usage.CachedTokens
			state.Usage.OutputTokens += usage.OutputTokens
			state.Usage.CostUSD += usage.CostUSD
			state.RequestPending = IsUsageUnknown(requestErr)
			state.Usage.Uncertain = state.Usage.Uncertain || state.RequestPending || usage.Uncertain
			record.Usage = usage
			publish()
			return checkpoint()
		},
	})
	record.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if reviewErr == nil && (result.BaseHash != record.BaseHash || result.CandidateHash != record.CandidateHash) {
		reviewErr = &fatalError{errors.New("Independent review returned mismatched source/candidate hashes")}
	}
	if reviewErr != nil {
		record.Verdict, record.Summary = "error", bounded(reviewErr.Error(), 4096)
		publish()
		if err := checkpoint(); err != nil {
			return model.Proposal{}, false, errors.Join(reviewErr, err)
		}
		return model.Proposal{}, false, reviewErr
	}
	record.Verdict, record.Summary, record.Assessments, record.Issues = result.Verdict, result.Summary, result.Assessments, result.Issues
	publish()
	if err := checkpoint(); err != nil {
		return model.Proposal{}, false, err
	}
	if in.Log != nil {
		in.Log("独立レビュー: " + record.Verdict)
	}
	return reviewedOutcome(final, candidate)
}

// Plan updates and replacement candidates cannot erase an unresolved review.
// Ordinary no-change decisions remain possible before any candidate is reviewed.
func hasUnresolvedReview(state model.RepairState) bool {
	if len(state.Reviews) > 0 {
		return state.Reviews[len(state.Reviews)-1].Verdict != "passed"
	}
	return state.LastCandidate != nil && state.LastCandidate.Review != nil && state.LastCandidate.Review.Verdict != "passed"
}

func reviewedOutcome(final model.Proposal, candidate *model.CandidateRecord) (model.Proposal, bool, error) {
	r := candidate.Review
	if r == nil || r.ID == "" || r.CandidateID != candidate.Result.CandidateID || r.BaseHash != candidate.Request.BaseHash || r.CandidateHash != candidate.Result.CandidateHash || r.PlanRevision != candidate.Request.PlanRevision {
		return model.Proposal{}, false, errors.New("Independent review does not match the current candidate")
	}
	switch r.Verdict {
	case "passed":
		return final, true, nil
	case "needs_changes":
		return final, false, nil
	case "needs_human":
		return model.Proposal{Outcome: "needs_human", Note: "独立レビューで判断保留: " + r.Summary}, true, nil
	default:
		return model.Proposal{}, false, errors.New("Independent review has not passed")
	}
}

func reviewFeedback(state model.RepairState) string {
	if state.LastCandidate == nil || state.LastCandidate.Review == nil {
		return "Independent review is required before adoption."
	}
	r := state.LastCandidate.Review
	// Never feed the reviewer its predecessor's findings. Only the editor sees this.
	return "Independent review requests changes. Inspect each issue, update the plan if necessary, validate a NEW complete original-based candidate, and submit that candidate. You cannot reuse this rejected candidate.\n" + string(raw(map[string]any{"verdict": r.Verdict, "summary": r.Summary, "assessments": r.Assessments, "issues": r.Issues}))
}

func reviewRules(in Input, plan model.RepairPlan) ([]ReviewRule, error) {
	wanted := map[string]bool{}
	for _, id := range in.CandidateRules {
		wanted[id] = true
	}
	for _, decision := range plan.RuleDecisions {
		wanted[decision.RuleID] = true
	}
	for _, rule := range in.Rules {
		if rule.Always {
			wanted[rule.ID] = true
		}
	}
	rules := []ReviewRule{}
	for _, rule := range in.Rules {
		if !wanted[rule.ID] {
			continue
		}
		body, err := in.ReadRule(rule.ID)
		if err != nil {
			return nil, &fatalError{fmt.Errorf("Cannot load review rule %s: %w", rule.ID, err)}
		}
		rules = append(rules, ReviewRule{ID: rule.ID, Title: rule.Title, Body: body, Always: rule.Always})
		delete(wanted, rule.ID)
	}
	if len(wanted) != 0 || len(rules) == 0 {
		return nil, errors.New("Required review rules are missing from the catalog")
	}
	return rules, nil
}

func reviewCandidateText(original string, edits []model.Edit) (string, error) {
	if err := validateExactEdits(edits, original); err != nil {
		return "", err
	}
	type editSpan struct {
		at   int
		edit model.Edit
	}
	spans := make([]editSpan, 0, len(edits))
	for _, edit := range edits {
		spans = append(spans, editSpan{strings.Index(original, edit.OldText), edit})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].at > spans[j].at })
	result := original
	for _, span := range spans {
		result = result[:span.at] + span.edit.NewText + result[span.at+len(span.edit.OldText):]
	}
	return result, nil
}
