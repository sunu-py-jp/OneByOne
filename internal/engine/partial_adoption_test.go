package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

const partialOriginal = "Legacy.Save()\nLegacy.Load()\n"
const partialAccepted = "Modern.Save()\nLegacy.Load()\n"

// This fixture supplies a review, but uses the production immutable candidate
// validation, adoption gate, queue, worktree and reporting paths.
func partialReviewedProposal(ctx context.Context, in agent.Input) (model.Proposal, error) {
	state := *in.RepairState
	state.Plan = model.RepairPlan{Revision: 1,
		RuleDecisions: []model.PlanDecision{{RuleID: "R001", Decision: "no_change", Reason: "Preserve behavior"}, {RuleID: "R019", Decision: "modify", Reason: "Save is safe; Load needs the external contract"}},
		Items: []model.PlanItem{
			{ID: "P1", RuleID: "R019", Location: "Save call", Change: "Use Modern.Save", Expected: "Supported Save API", Status: "proposed"},
			{ID: "P2", RuleID: "R019", Location: "Load call", Change: "Resolve Load contract", Expected: "Preserve external ownership", Status: "blocked", HoldReason: "外部のLoad契約を確認してください", SourceLocations: []model.SourceLocation{{StartLine: 2, EndLine: 2, Excerpt: "Legacy.Load()"}}},
		}}
	if err := in.SaveRepairState(state); err != nil {
		return model.Proposal{}, err
	}
	request := model.CandidateRequest{PlanRevision: 1, BaseHash: in.BaseHash, AddressedItemIDs: []string{"P1"}, Edits: []model.Edit{{OldText: "Legacy.Save()", NewText: "Modern.Save()", ItemIDs: []string{"P1"}}}}
	result, err := in.ValidateCandidate(ctx, request)
	if err != nil {
		return model.Proposal{}, err
	}
	if !result.Passed {
		return model.Proposal{}, fmt.Errorf("candidate rejected: %+v", result.Checks)
	}
	review := independentReviewAcceptedFixture(result, in.BaseHash)
	review.Verdict, review.Summary = "passed_with_holds", "Saveは独立して安全に修正済み。Loadは原文のまま保留。"
	review.Assessments[1].Status, review.Assessments[1].Reason = "needs_human", review.Summary
	review.HoldAssessments = []model.ReviewHoldAssessment{{ItemID: "P2", Status: "preserved", Reason: "Load stays unchanged and has no dependency on the Save edit"}}
	state.LastCandidate = &model.CandidateRecord{Request: request, Result: result, Review: &review}
	state.Reviews = []model.IndependentReview{review}
	if err := in.SaveRepairState(state); err != nil {
		return model.Proposal{}, err
	}
	return model.Proposal{Outcome: "modified", CandidateID: result.CandidateID, Edits: request.Edits, RulesApplied: []string{"R019"}, Note: "Saveを修正し、Loadは判断保留にしました"}, nil
}

func partialRun(t *testing.T) (*Service, model.Config, model.State) {
	t.Helper()
	s, cfg := fixture(t, map[string]string{"A.txt": partialOriginal})
	s.propose = partialReviewedProposal
	st := runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "needs_human" {
		t.Fatalf("partial run: %+v", st.Tasks[0])
	}
	h := st.Tasks[0].History[0]
	if !h.Partial || !h.AdoptedChanges() || h.Outcome != "needs_human" || unfinishedRepair(st.Tasks[0]) {
		t.Fatalf("partial acceptance not recorded: %+v", h)
	}
	if got := readTest(t, filepath.Join(st.Worktree, "A.txt")); got != partialAccepted {
		t.Fatalf("safe changes were not adopted: %q", got)
	}
	if got := readTest(t, filepath.Join(cfg.Root, "A.txt")); got != partialOriginal {
		t.Fatal("original checkout changed")
	}
	return s, cfg, st
}

func TestPartialAdoptionReportsPublishesAndRetriesWithoutLosingHistory(t *testing.T) {
	s, _, st := partialRun(t)
	defer s.Close()
	first := st.Tasks[0].History[0]
	if len(first.Changes) < 2 || first.Changes[0].Status != "fixed" || first.Changes[1].Status != "needs_human" || len(first.Changes[1].LineRanges) == 0 {
		t.Fatalf("safe and held items not independently recorded: %+v", first.Changes)
	}
	detail, err := s.GetFileDetail("A.txt", -1)
	if err != nil || detail.Before != partialOriginal || detail.After != partialAccepted || !strings.Contains(detail.Diff, "+Modern.Save()") {
		t.Fatalf("cumulative partial diff: %+v %v", detail, err)
	}
	preview, err := s.GetResultPublicationPreview()
	if err != nil || len(preview.Files) != 1 || len(preview.Files[0].RulesApplied) != 1 || !strings.Contains(preview.Message, "うち一部修正済み：1") || !strings.Contains(preview.Message, "外部のLoad契約") {
		t.Fatalf("publication loses partial status: %+v %v", preview, err)
	}
	if _, err = s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	preview, err = s.GetResultPublicationPreview()
	if err != nil || !strings.Contains(preview.Message, "うち一部修正済み：1") {
		t.Fatalf("requeue cleared unresolved holds: %v %s", err, preview.Message)
	}
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		if in.Content != partialAccepted || in.RepairState.Plan.Revision != 0 {
			t.Errorf("retry did not start a fresh plan over accepted content: %q %+v", in.Content, in.RepairState.Plan)
		}
		return parallelReviewedEdit(ctx, in, "Legacy.Load()", "Modern.Load()")
	}
	st = runTest(t, s, 0)
	if st.Tasks[0].Status != "done" || len(st.Tasks[0].History) != 2 || st.Tasks[0].History[0].Commit != first.Commit || !st.Tasks[0].History[0].Partial {
		t.Fatalf("retry changed historical partial attempt: %+v", st.Tasks[0])
	}
	detail, err = s.GetFileDetail("A.txt", -1)
	if err != nil || detail.After != "Modern.Save()\nModern.Load()\n" {
		t.Fatalf("retry lost safe prior changes: %+v %v", detail, err)
	}
	for _, row := range detail.Changes {
		if row.Status == "needs_human" {
			t.Fatalf("resolved historical hold still shown: %+v", row)
		}
	}
	frozen, err := s.GetExecutionFileDetail(first.ExecutionID, "A.txt", -1)
	if err != nil || frozen.After != partialAccepted || frozen.Task.Status != "needs_human" {
		t.Fatalf("retry overwrote the earlier execution: %+v %v", frozen, err)
	}
	foundHold := false
	for _, row := range frozen.Changes {
		foundHold = foundHold || row.Status == "needs_human" && len(row.LineRanges) > 0
	}
	if !foundHold {
		t.Fatal("earlier execution lost held source locations")
	}
}

func TestPartialAdoptionCanDiscardOnlyAdoptedChanges(t *testing.T) {
	s, _, st := partialRun(t)
	defer s.Close()
	if !canDiscardChanges(st.Tasks[0]) {
		t.Fatal("partial changes cannot be discarded")
	}
	updated, err := s.DiscardFileChanges("A.txt")
	if err != nil || readTest(t, filepath.Join(st.Worktree, "A.txt")) != partialOriginal || canDiscardChanges(updated.Tasks[0]) {
		t.Fatalf("partial discard failed: %v", err)
	}
}

func TestPartialAdoptionInterruptedCommitRecoversAsHeld(t *testing.T) {
	s, _, st := partialRun(t)
	defer s.Close()
	s.mu.Lock()
	s.state.Tasks[0].Status = "running"
	h := &s.state.Tasks[0].History[0]
	h.Commit, h.Outcome, h.FinishedAt, h.Changes = "", "validated", "", nil
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	recovered := s.Snapshot().Tasks[0]
	if recovered.Status != "needs_human" || !recovered.History[0].AdoptedChanges() || recovered.History[0].Commit != st.Tasks[0].History[0].Commit || recovered.History[0].Changes[0].Status != "fixed" {
		t.Fatalf("partial recovery marked complete or lost fixes: %+v", recovered)
	}
}

func TestPartialAdoptionGateRequiresScopedApproval(t *testing.T) {
	s, cfg, st := partialRun(t)
	defer s.Close()
	h := st.Tasks[0].History[0]
	for _, scenario := range []string{"missing-hold", "unsafe-dependency", "cleared-hold", "new-hold", "addressed-blocked", "ordinary-pass"} {
		t.Run(scenario, func(t *testing.T) {
			checkpoint, err := loadRepairCheckpoint(cfg, h)
			if err != nil {
				t.Fatal(err)
			}
			candidate := checkpoint.State.LastCandidate
			switch scenario {
			case "missing-hold":
				candidate.Review.HoldAssessments = nil
			case "unsafe-dependency":
				candidate.Review.HoldAssessments[0].Status = "unsafe"
			case "cleared-hold":
				candidate.Review.Assessments[1].Status = "satisfied"
			case "new-hold":
				candidate.Review.Assessments[0].Status = "needs_human"
			case "addressed-blocked":
				candidate.Request.AddressedItemIDs = append(candidate.Request.AddressedItemIDs, "P2")
			case "ordinary-pass":
				candidate.Review.Verdict = "passed"
			}
			if passedIndependentReview(candidate, h.InputHash, h.OutputHash, checkpoint.State.Plan) {
				t.Fatal("invalid partial approval was accepted")
			}
		})
	}
}

func TestPartialCandidateRejectsRelocatedHeldLines(t *testing.T) {
	before := "first();\nsecond();\nheld();\nlast();\n"
	after := "held();\nfirst();\nsecond();\nlast();\n"
	plan := model.RepairPlan{Items: []model.PlanItem{{ID: "hold", Status: "blocked", HoldReason: "Timing contract unknown", SourceLocations: []model.SourceLocation{{StartLine: 3, EndLine: 3, Excerpt: "held();"}}}}}
	ranges, err := verifiedEditLineRanges(before, after, []model.Edit{{OldText: before, NewText: after, ItemIDs: []string{"safe"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := candidateHeldRangesUnchanged(plan, ranges); err == nil {
		t.Fatalf("held code was moved despite its exact text being preserved: %+v", ranges)
	}
}
