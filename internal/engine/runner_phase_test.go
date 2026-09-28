package engine

import (
	"context"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func TestReviewFilePhasePersistsBetweenRequestsAndClearsAfterReview(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	checks := 0
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		state := *in.RepairState
		// A superseded review remains in history. It must never keep a newer
		// candidate or the editor in the reviewing phase.
		state.Reviews = []model.IndependentReview{{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", CandidateID: "old", Verdict: "running"}}
		check := func(name, want string) bool {
			checks++
			if err := in.SaveRepairState(state); err != nil {
				t.Errorf("%s: persist repair: %v", name, err)
				return false
			}
			// Exercise the same snapshot API the UI polls, not just a phase helper.
			snapshot, err := s.GetState()
			if err != nil {
				t.Errorf("%s: get state: %v", name, err)
				return false
			}
			if got := snapshot.FilePhases[in.File]; got != want {
				t.Errorf("%s: phase = %q, want %q", name, got, want)
			}
			return true
		}
		finish := model.Proposal{Outcome: "needs_human", Note: "phase fixture finished"}
		state.LastCandidate = &model.CandidateRecord{Result: model.CandidateValidation{CandidateID: "current"}, Review: &model.IndependentReview{Verdict: "running"}}
		state.RequestKind, state.RequestPending = "review", true
		if !check("review request", "reviewing") {
			return finish, nil
		}
		state.RequestPending = false
		if !check("review response parsing", "reviewing") || !check("review tool processing", "reviewing") {
			return finish, nil
		}
		for _, verdict := range []string{"passed", "needs_changes", "needs_human", "error"} {
			state.LastCandidate.Review = &model.IndependentReview{Verdict: verdict}
			if !check("review ended: "+verdict, "running") {
				return finish, nil
			}
		}
		state.LastCandidate.Review = &model.IndependentReview{Verdict: "running"}
		if !check("next review begins", "reviewing") {
			return finish, nil
		}
		state.LastCandidate = nil
		if !check("candidate canceled, old review retained", "running") {
			return finish, nil
		}
		state.LastCandidate = &model.CandidateRecord{Result: model.CandidateValidation{CandidateID: "replacement"}}
		if !check("replacement has no review", "running") {
			return finish, nil
		}
		state.RequestKind, state.RequestPending = "editor", true
		if !check("editor request", "running") {
			return finish, nil
		}
		state.RequestKind = "review"
		if !check("pending review before review record", "reviewing") {
			return finish, nil
		}
		state.RequestPending = false
		check("pending review canceled", "running")
		return finish, nil
	}
	state := runTest(t, s, 0)
	if checks != 13 {
		t.Fatalf("phase checks = %d, want 13", checks)
	}
	if state.Running || state.Phase != "idle" || len(state.FilePhases) != 0 || len(state.CurrentFiles) != 0 || state.CurrentFile != "" {
		t.Fatalf("finished run retained active phases: %+v", state.FilePhases)
	}
	if state.LastError != "" || state.Tasks[0].Status != "needs_human" {
		t.Fatalf("fixture did not finish normally: %q / %s", state.LastError, state.Tasks[0].Status)
	}
}
