package engine

import "onebyone/internal/model"

func copyTask(t model.Task) model.Task {
	t.Discards = append([]model.DiscardChange(nil), t.Discards...)
	t.Rules = append([]string{}, t.Rules...)
	t.RulesApplied = append([]string{}, t.RulesApplied...)
	t.History = append([]model.Attempt{}, t.History...)
	for i := range t.History {
		t.History[i].RulesApplied = append([]string{}, t.History[i].RulesApplied...)
		t.History[i].Checks = append([]model.Check{}, t.History[i].Checks...)
		t.History[i].Reviews = copyReviews(t.History[i].Reviews)
		t.History[i].Changes = append([]model.ChangeReportItem(nil), t.History[i].Changes...)
		for j := range t.History[i].Changes {
			t.History[i].Changes[j].LineRanges = append([]model.ChangeLineRange(nil), t.History[i].Changes[j].LineRanges...)
		}
	}
	return t
}

func copyReviews(reviews []model.IndependentReview) []model.IndependentReview {
	out := append([]model.IndependentReview{}, reviews...)
	for i := range out {
		out[i].Assessments = append([]model.ReviewAssessment{}, out[i].Assessments...)
		out[i].Issues = append([]model.ReviewIssue{}, out[i].Issues...)
	}
	return out
}
func copyTasks(tasks []model.Task) []model.Task {
	out := make([]model.Task, len(tasks))
	for i, t := range tasks {
		out[i] = copyTask(t)
	}
	return out
}
