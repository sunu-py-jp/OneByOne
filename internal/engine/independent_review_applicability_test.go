package engine

import (
	"testing"

	"onebyone/internal/model"
)

func TestPassedIndependentReviewAcceptsExplicitInapplicableCommonRule(t *testing.T) {
	for _, scenario := range []string{"assessed", "omitted", "empty-reason", "held", "duplicate"} {
		t.Run(scenario, func(t *testing.T) {
			h, checkpoint := changeReportFixture()
			checkpoint.State.Plan.RuleDecisions = append(checkpoint.State.Plan.RuleDecisions, model.PlanDecision{RuleID: "R001", Decision: "no_change", Reason: "var宣言は存在しません"})
			candidate := checkpoint.State.LastCandidate
			common := model.ReviewAssessment{RuleID: "R001", Status: "not_applicable", Reason: "変更前・変更後にvar宣言は存在しません"}
			switch scenario {
			case "empty-reason":
				common.Reason = ""
			case "held":
				common.Status = "needs_human"
			}
			if scenario != "omitted" {
				candidate.Review.Assessments = append(candidate.Review.Assessments, common)
			}
			if scenario == "duplicate" {
				candidate.Review.Assessments = append(candidate.Review.Assessments, common)
			}
			got := passedIndependentReview(candidate, h.InputHash, h.OutputHash, checkpoint.State.Plan)
			if got != (scenario == "assessed") {
				t.Fatalf("adoption gate common assessment %s = %v", scenario, got)
			}
		})
	}
}
