package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func TestNoChangeReviewHoldKeepsAfterLocationAcrossReloadAndExecutionSnapshot(t *testing.T) {
	original := "Legacy.Save()\nshared.release();\n"
	s, cfg := fixture(t, map[string]string{"A.txt": original})
	var rules []model.Rule
	editorRequests, reviewRequests := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if len(envelope.Tools) == 0 {
			reviewRequests++
			assessments := []model.ReviewAssessment{}
			for _, rule := range rules {
				status := "satisfied"
				if rule.ID == "R001" {
					status = "needs_human"
				}
				assessments = append(assessments, model.ReviewAssessment{RuleID: rule.ID, Status: status, Reason: "契約を確認しました。"})
			}
			independentReviewFinal(w, map[string]any{
				"baseHash": digest([]byte(original)), "candidateHash": digest([]byte(original)), "verdict": "needs_human", "summary": "共有接続の所有権を確認してください。", "assessments": assessments,
				"issues": []model.ReviewIssue{{RuleID: "R001", Location: "shared.release()", LineBasis: "after", StartLine: 2, EndLine: 2, Excerpt: "shared.release();", Reason: "共有接続の所有権が不明です。", RequestedChange: "呼び出し元の所有権を確認する。"}},
			})
			return
		}
		editorRequests++
		switch editorRequests {
		case 1:
			ids := []string{}
			for _, rule := range rules {
				ids = append(ids, rule.ID)
			}
			engineRepairResponse(w, engineRepairCall("read", "read_rules", map[string]any{"ids": ids}))
		case 2:
			decisions := []model.PlanDecision{}
			for _, rule := range rules {
				decisions = append(decisions, model.PlanDecision{RuleID: rule.ID, Decision: "no_change", Reason: "変更不要です。"})
			}
			engineRepairResponse(w, engineRepairCall("plan", "update_state", model.PlanUpdate{RuleDecisions: decisions, Items: []model.PlanItem{}}))
		case 3:
			independentReviewFinal(w, map[string]string{"outcome": "skipped", "candidateId": "", "note": "変更不要です。"})
		default:
			t.Errorf("held review returned to editor unexpectedly: %d", editorRequests)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		rules = in.Rules
		in.Config.Endpoint = srv.URL
		return agent.Run(ctx, in)
	}
	state := runTest(t, s, 1)
	if state.LastError != "" || state.Tasks[0].Status != "needs_human" || reviewRequests != 1 || editorRequests != 3 {
		t.Fatalf("no-change review did not hold: %+v, editor=%d review=%d", state, editorRequests, reviewRequests)
	}
	h := state.Tasks[0].History[0]
	wantAttempt := []model.ChangeLineRange{{AfterStart: 2, AfterEnd: 2}}
	if h.OutputHash != "" || h.Commit != "" || len(h.Changes) != 1 || !reflect.DeepEqual(h.Changes[0].LineRanges, wantAttempt) || h.Changes[0].AttributionVersion != model.LineAttributionVersion {
		t.Fatalf("review hold before candidate journal lost its position or created a commit: %+v", h)
	}
	if err := s.load(cfg.QueuePath); err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{-1, 0} {
		want := wantAttempt
		if index < 0 {
			want = []model.ChangeLineRange{{BeforeStart: 2, BeforeEnd: 2}, {AfterStart: 2, AfterEnd: 2}}
		}
		for _, frozen := range []bool{false, true} {
			var detail model.FileDetail
			var err error
			if frozen {
				detail, err = s.GetExecutionFileDetail(state.ExecutionRuns[0].ID, "A.txt", index)
			} else {
				detail, err = s.GetFileDetail("A.txt", index)
			}
			if err != nil || detail.Before != original || detail.After != original || len(detail.Changes) != 1 || !reflect.DeepEqual(detail.Changes[0].LineRanges, want) || detail.Changes[0].SourceAttemptID != h.ID {
				t.Fatalf("held review location lost after reload index=%d frozen=%v: %+v %v", index, frozen, detail, err)
			}
		}
	}
	// A resumed attempt may overwrite its mutable checkpoint/artifacts. An
	// older execution must keep the source used by that execution's reviewer.
	checkpointPath, err := repairPath(cfg, h.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(checkpointPath); err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(cfg.QueuePath+".artifacts", h.ID+".after"), []byte("different candidate after resuming\n"))
	for _, index := range []int{-1, 0} {
		detail, err := s.GetExecutionFileDetail(state.ExecutionRuns[0].ID, "A.txt", index)
		if err != nil || detail.Before != original || detail.After != original || len(detail.Changes) != 1 || len(detail.Changes[0].LineRanges) == 0 {
			t.Fatalf("live repair checkpoint changed the frozen held location: %+v %v", detail, err)
		}
	}
}

func TestNoChangeRequiresIndependentReviewBeforeCompletingWithoutCommit(t *testing.T) {
	original := "Legacy.Save()\n"
	s, cfg := fixture(t, map[string]string{"A.txt": original})
	var rules []model.Rule
	editorRequests, reviewRequests := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var envelope struct {
			Tools []json.RawMessage `json:"tools"`
			Input []struct {
				Content string `json:"content"`
			} `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&envelope); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if len(envelope.Tools) == 0 {
			reviewRequests++
			var input struct {
				Before, After, BaseHash, CandidateHash string
				Rules                                  []agent.ReviewRule
			}
			if len(envelope.Input) != 1 || json.Unmarshal([]byte(envelope.Input[0].Content), &input) != nil || input.Before != original || input.After != original || input.BaseHash != digest([]byte(original)) || input.CandidateHash != input.BaseHash || len(input.Rules) != len(rules) {
				t.Errorf("review did not receive the unchanged source: %+v", input)
			}
			assessments := []model.ReviewAssessment{}
			for _, rule := range rules {
				assessments = append(assessments, model.ReviewAssessment{RuleID: rule.ID, Status: "not_applicable", Reason: "テスト用の独立した判断。"})
			}
			independentReviewFinal(w, map[string]any{"baseHash": input.BaseHash, "candidateHash": input.CandidateHash, "verdict": "passed", "summary": "変更不要を独立に確認しました。", "assessments": assessments, "issues": []model.ReviewIssue{}})
			return
		}
		editorRequests++
		switch editorRequests {
		case 1:
			ids := []string{}
			for _, rule := range rules {
				ids = append(ids, rule.ID)
			}
			engineRepairResponse(w, engineRepairCall("read", "read_rules", map[string]any{"ids": ids}))
		case 2:
			decisions := []model.PlanDecision{}
			for _, rule := range rules {
				decisions = append(decisions, model.PlanDecision{RuleID: rule.ID, Decision: "no_change", Reason: "変更不要と判断しました。"})
			}
			engineRepairResponse(w, engineRepairCall("plan", "update_state", model.PlanUpdate{RuleDecisions: decisions, Items: []model.PlanItem{}}))
		case 3:
			independentReviewFinal(w, map[string]string{"outcome": "skipped", "candidateId": "", "note": "変更不要です。"})
		default:
			t.Errorf("unexpected editor request %d", editorRequests)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		rules = in.Rules
		in.Config.Endpoint = srv.URL
		return agent.Run(ctx, in)
	}
	state := runTest(t, s, 1)
	if state.LastError != "" || len(state.Tasks) != 1 || state.Tasks[0].Status != "skipped" || editorRequests != 3 || reviewRequests != 1 {
		t.Fatalf("unchanged source did not complete through independent review: %+v editor=%d review=%d", state, editorRequests, reviewRequests)
	}
	h := state.Tasks[0].History[0]
	if len(h.Reviews) != 1 || h.Commit != "" || h.InputHash != h.OutputHash || len(h.Changes) != len(rules) || h.Usage.Turns != 4 {
		t.Fatalf("unchanged review lost evidence or committed: %+v", h)
	}
	if gitTest(t, state.Worktree, "rev-parse", "HEAD") != gitTest(t, cfg.Root, "rev-parse", "HEAD") || readTest(t, filepath.Join(state.Worktree, "A.txt")) != original {
		t.Fatal("unchanged review changed the source or created a commit")
	}
}

func TestNoChangeReviewCannotHideConcurrentWorktreeChanges(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		out, err := reviewedNoChangeProposal(t, in, "変更不要です。")
		if err != nil {
			t.Fatal(err)
		}
		writeTest(t, filepath.Join(s.Snapshot().Worktree, "A.txt"), []byte("concurrent user edit\n"))
		return out, err
	}
	state := runTest(t, s, 1)
	if state.Tasks[0].Status != "needs_human" || !strings.Contains(state.Tasks[0].Note, "レビュー後に作業コピーが変更") || state.Tasks[0].History[0].Commit != "" || readTest(t, filepath.Join(state.Worktree, "A.txt")) != "concurrent user edit\n" {
		t.Fatalf("unchanged review adopted or discarded unknown changes: %+v", state)
	}
}

func TestNoChangeApprovalRequiresMatchingPlanAndImmutableSource(t *testing.T) {
	base := "abc"
	plan := model.RepairPlan{Revision: 2, RuleDecisions: []model.PlanDecision{{RuleID: "R001", Decision: "no_change", Reason: "Already compliant"}}}
	for _, mutation := range []string{"none", "edit", "hash", "revision", "decision", "rule-omitted", "rule-duplicated", "review-hash", "no-marker", "no-identity"} {
		t.Run(mutation, func(t *testing.T) {
			p := plan
			p.RuleDecisions = append([]model.PlanDecision{}, plan.RuleDecisions...)
			c := model.CandidateRecord{NoChange: true, Request: model.CandidateRequest{BaseHash: base, PlanRevision: 2}, Result: model.CandidateValidation{CandidateID: "candidate", CandidateHash: base, PlanRevision: 2, Passed: true}, Review: &model.IndependentReview{ID: "review", CandidateID: "candidate", BaseHash: base, CandidateHash: base, PlanRevision: 2, Verdict: "passed", FinishedAt: now(), Assessments: []model.ReviewAssessment{{RuleID: "R001", Status: "satisfied", Reason: "Already compliant"}}}}
			switch mutation {
			case "edit":
				c.Request.Edits = []model.Edit{{OldText: "a", NewText: "b"}}
			case "hash":
				c.Result.CandidateHash = "different"
			case "revision":
				p.Revision++
			case "decision":
				p.RuleDecisions[0].Decision = "modify"
			case "rule-omitted":
				c.Review.Assessments = nil
			case "rule-duplicated":
				p.RuleDecisions = append(p.RuleDecisions, p.RuleDecisions[0])
			case "review-hash":
				c.Review.BaseHash = "different"
			case "no-marker":
				c.NoChange = false
			case "no-identity":
				c.Result.CandidateID, c.Review.CandidateID = "", ""
			}
			if passedNoChangeReview(&c, base, p) != (mutation == "none") {
				t.Fatalf("wrong unchanged approval result for %s", mutation)
			}
		})
	}
}
