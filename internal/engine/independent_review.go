package engine

import "onebyone/internal/model"

func passedIndependentReview(candidate *model.CandidateRecord, baseHash, candidateHash string, plan model.RepairPlan) bool {
	if candidate == nil || candidate.Review == nil {
		return false
	}
	r := candidate.Review
	if r.ID == "" || r.Verdict != "passed" || r.FinishedAt == "" || r.CandidateID != candidate.Result.CandidateID || r.BaseHash != baseHash || r.CandidateHash != candidateHash || r.PlanRevision != candidate.Request.PlanRevision || r.PlanRevision != plan.Revision || len(r.Issues) != 0 {
		return false
	}
	assessed := map[string]bool{}
	for _, item := range r.Assessments {
		if item.RuleID == "" || assessed[item.RuleID] || item.Reason == "" || (item.Status != "satisfied" && item.Status != "not_applicable") {
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
