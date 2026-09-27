package engine

import (
	"strings"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func passedNoChangeReview(candidate *model.CandidateRecord, baseHash string, plan model.RepairPlan) bool {
	if candidate == nil || !candidate.NoChange || !candidate.Result.Passed || candidate.Result.CandidateID == "" || baseHash == "" || plan.Revision < 1 || len(candidate.Request.Edits) != 0 || len(candidate.Request.AddressedItemIDs) != 0 || len(plan.Items) != 0 || candidate.Request.BaseHash != baseHash || candidate.Result.CandidateHash != baseHash || candidate.Result.PlanRevision != plan.Revision {
		return false
	}
	seen := map[string]bool{}
	for _, decision := range plan.RuleDecisions {
		if decision.RuleID == "" || seen[decision.RuleID] || decision.Decision != "no_change" || strings.TrimSpace(decision.Reason) == "" {
			return false
		}
		seen[decision.RuleID] = true
	}
	if candidate.Review == nil || len(candidate.Review.Assessments) != len(seen) {
		return false
	}
	return passedIndependentReview(candidate, baseHash, baseHash, plan)
}

func passedIndependentReview(candidate *model.CandidateRecord, baseHash, candidateHash string, plan model.RepairPlan) bool {
	if candidate == nil || candidate.Review == nil {
		return false
	}
	r := candidate.Review
	if r.ID == "" || r.FinishedAt == "" || r.CandidateID != candidate.Result.CandidateID || r.BaseHash != baseHash || r.CandidateHash != candidateHash || r.PlanRevision != candidate.Request.PlanRevision || r.PlanRevision != plan.Revision {
		return false
	}
	holds, err := agent.CandidateHolds(plan)
	if err != nil {
		return false
	}
	partial := len(holds) > 0
	if partial {
		if candidate.NoChange || r.Verdict != "passed_with_holds" || agent.CheckPartialReview(*r, plan) != nil || agent.CheckCandidatePlan(plan, candidate.Request) != nil {
			return false
		}
	} else if r.Verdict != "passed" || len(r.Issues) != 0 || len(r.HoldAssessments) != 0 {
		return false
	}
	assessed := map[string]bool{}
	for _, item := range r.Assessments {
		if item.RuleID == "" || assessed[item.RuleID] || strings.TrimSpace(item.Reason) == "" || (item.Status != "satisfied" && item.Status != "not_applicable" && !(partial && item.Status == "needs_human")) {
			return false
		}
		assessed[item.RuleID] = true
	}
	if len(plan.RuleDecisions) == 0 {
		return false
	}
	for _, decision := range plan.RuleDecisions {
		if !assessed[decision.RuleID] {
			return false
		}
	}
	return true
}
