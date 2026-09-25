package engine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func independentReviewFinal(w http.ResponseWriter, value any) {
	encoded, _ := json.Marshal(value)
	engineRepairResponse(w, map[string]any{"type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": string(encoded)}}})
}

func independentReviewReply(t *testing.T, w http.ResponseWriter, original string, passed bool) {
	t.Helper()
	verdict, commonStatus := "needs_changes", "violated"
	modified := "Modern.Save()\nModern.Load()\n"
	summary := "REVIEW_FIRST_ONLY: unrelated flush behavior was removed."
	issues := []any{map[string]string{"ruleId": "R001", "location": "End of workflow", "lineBasis": "before", "excerpt": "flush()", "reason": "REVIEW_FIRST_ONLY: the original workflow flushed after both calls.", "requestedChange": "Retain flush() after the supported SDK calls."}}
	if passed {
		verdict, commonStatus = "passed", "satisfied"
		modified += "flush()\n"
		summary = "Supported SDK calls preserve the existing flush behavior."
		issues = []any{}
	}
	independentReviewFinal(w, map[string]any{
		"baseHash": digest([]byte(original)), "candidateHash": digest([]byte(modified)), "verdict": verdict, "summary": summary,
		"assessments": []any{map[string]string{"ruleId": "R001", "status": commonStatus, "reason": "Checked the original workflow's flush behavior."}, map[string]string{"ruleId": "R019", "status": "satisfied", "reason": "Both calls use the supported SDK."}},
		"issues":      issues,
	})
}

// Internal proposer fixtures still have to furnish matching review provenance
// when selecting a candidate through the production adoption gate.
func independentReviewAcceptedFixture(candidate model.CandidateValidation, baseHash string) model.IndependentReview {
	return model.IndependentReview{
		ID: strings.Repeat("a", 32), CandidateID: candidate.CandidateID, BaseHash: baseHash, CandidateHash: candidate.CandidateHash, PlanRevision: candidate.PlanRevision,
		Verdict: "passed", Summary: "Fixture review passed.", StartedAt: now(), FinishedAt: now(),
		Assessments: []model.ReviewAssessment{{RuleID: "R001", Status: "satisfied", Reason: "Unrelated behavior preserved."}, {RuleID: "R019", Status: "satisfied", Reason: "Both calls use the supported SDK."}}, Issues: []model.ReviewIssue{},
	}
}

func independentReviewPlan() model.PlanUpdate {
	plan := engineRepairPlan()
	plan.RuleDecisions[0].Reason = "EDITOR_ONLY_PLAN_MARKER: unrelated flushing should stay unchanged"
	return model.PlanUpdate{ExpectedRevision: 0, RuleDecisions: plan.RuleDecisions, Items: plan.Items}
}

func independentReviewCandidate(original string, complete, preserveFlush bool) model.CandidateRequest {
	request := model.CandidateRequest{PlanRevision: 1, BaseHash: digest([]byte(original)), AddressedItemIDs: []string{"P1", "P2"}, Edits: []model.Edit{{OldText: "Legacy.Save()", NewText: "Modern.Save()", ItemIDs: []string{"P1", "P2"}}}}
	if complete {
		request.Edits[0].ItemIDs = []string{"P1"}
		request.Edits = append(request.Edits, model.Edit{OldText: "Legacy.Load()", NewText: "Modern.Load()", ItemIDs: []string{"P2"}})
	}
	if !preserveFlush {
		request.Edits = append(request.Edits, model.Edit{OldText: "flush()\n", NewText: "", ItemIDs: []string{"P1"}})
	}
	return request
}

// This uses the real agent, provider codec, validation worktree and commit path.
// Only the HTTP model responses are replaced; review cannot be bypassed by a
// successful injected final proposal.
func TestIndependentReviewRepairsSemanticFailureInSameEditorConversation(t *testing.T) {
	original := "Legacy.Save()\nLegacy.Load()\nflush()\n"
	s, cfg := fixture(t, map[string]string{"A.txt": original})
	writeTest(t, filepath.Join(cfg.RulesPath, "R001", "rule.json"), fixtureRuleJSON(t, "Preserve behavior", "", "Preserve unrelated code and formatting.", "", "", "COMMON_RULE_REVIEW_MARKER: existing flush calls must be retained.", ""))
	if _, err := s.Scan(); err != nil {
		t.Fatal(err)
	}
	counter := filepath.Join(t.TempDir(), "checks.txt")
	cfg.CheckCommands = []model.Command{{Name: "count checks", Executable: os.Args[0], Args: []string{"-test.run=^TestRepairCheckpointCheckCounterProcess$", "--", "repair-check-counter", counter}}}
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var requests, editorRequests, reviewRequests, editorSessions atomic.Int32
	var rejectedCandidateID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestNumber := int(requests.Add(1))
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		var envelope struct {
			Model string                       `json:"model"`
			Tools []json.RawMessage            `json:"tools"`
			Input []map[string]json.RawMessage `json:"input"`
		}
		if err = json.Unmarshal(body, &envelope); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		queue, err := store.LoadQueue(cfg.QueuePath)
		if err != nil || len(queue) != 1 || len(queue[0].History) != 1 {
			t.Errorf("request did not have a single durable attempt: %+v, %v", queue, err)
			w.WriteHeader(500)
			return
		}
		checkpoint, err := loadRepairCheckpoint(cfg, queue[0].History[0])
		if err != nil || checkpoint == nil || !checkpoint.State.RequestPending || checkpoint.State.Usage.Turns != requestNumber {
			t.Errorf("request was not reserved in the shared checkpoint: %+v, %v", checkpoint, err)
			w.WriteHeader(500)
			return
		}
		if envelope.Model != cfg.Deployment {
			t.Errorf("editor/reviewer did not use the selected deployment: %q", envelope.Model)
		}
		if len(envelope.Tools) == 0 {
			reviewNumber := int(reviewRequests.Add(1))
			if editorRequests.Load() != int32(reviewNumber*2+3) {
				t.Errorf("review ran before the mechanical gate or at the wrong stage: editor=%d review=%d", editorRequests.Load(), reviewNumber)
			}
			if len(envelope.Input) != 1 || string(envelope.Input[0]["role"]) != `"user"` {
				t.Errorf("review reused editor/provider history: %s", body)
			}
			for _, leaked := range []string{"EDITOR_ONLY_PLAN_MARKER", "REVIEW_FIRST_ONLY", "function_call_output", "previousFailure", "repairState", "ruleDecisions"} {
				if strings.Contains(string(body), leaked) {
					t.Errorf("review context leaked %q: %s", leaked, body)
				}
			}
			for _, required := range []string{"A.txt", "Legacy.Save()", "Modern.Save()", "Legacy.Load()", "Modern.Load()", "COMMON_RULE_REVIEW_MARKER", "R001", "R019", "Replace Legacy.Save with Modern.Save."} {
				if !strings.Contains(string(body), required) {
					t.Errorf("review did not receive original/candidate/full required rules: missing %q", required)
				}
			}
			worktree := s.Snapshot().Worktree
			current, readErr := os.ReadFile(filepath.Join(worktree, "A.txt"))
			if readErr != nil || string(current) != original {
				t.Errorf("review began before validation restored the worktree: %q, %v", current, readErr)
			}
			independentReviewReply(t, w, original, reviewNumber == 2)
			return
		}
		turn := int(editorRequests.Add(1))
		switch turn {
		case 1:
			engineRepairResponse(w, engineRepairCall("read", "read_rule", map[string]string{"id": "R019"}))
		case 2:
			engineRepairResponse(w, engineRepairCall("plan", "update_state", independentReviewPlan()))
		case 3:
			engineRepairResponse(w, engineRepairCall("bad-symbols", "validate_candidate", independentReviewCandidate(original, false, true)))
		case 4:
			if reviewRequests.Load() != 0 || checkpoint.State.LastCandidate == nil || checkpoint.State.LastCandidate.Result.Passed {
				t.Error("mechanical failure was reviewed or accepted")
			}
			engineRepairResponse(w, engineRepairCall("bad-semantics", "validate_candidate", independentReviewCandidate(original, true, false)))
		case 5:
			last := checkpoint.State.LastCandidate
			if reviewRequests.Load() != 0 || last == nil || !last.Result.Passed {
				t.Errorf("review ran before the editor finished the mechanically valid candidate: %+v", last)
				w.WriteHeader(500)
				return
			}
			rejectedCandidateID = last.Result.CandidateID
			independentReviewFinal(w, map[string]string{"outcome": "modified", "candidateId": last.Result.CandidateID, "note": "EDITOR_ONLY_PLAN_MARKER: both changes are finished."})
		case 6:
			last := checkpoint.State.LastCandidate
			if reviewRequests.Load() != 1 || last == nil || last.Result.CandidateID != rejectedCandidateID || !strings.Contains(string(body), "REVIEW_FIRST_ONLY") {
				t.Errorf("review findings did not return to the same editor conversation: %+v", last)
			}
			engineRepairResponse(w, engineRepairCall("good-semantics", "validate_candidate", independentReviewCandidate(original, true, true)))
		case 7:
			last := checkpoint.State.LastCandidate
			if reviewRequests.Load() != 1 || last == nil || !last.Result.Passed || last.Result.CandidateID == rejectedCandidateID {
				t.Errorf("repaired candidate did not receive mechanical revalidation: %+v", last)
				w.WriteHeader(500)
				return
			}
			independentReviewFinal(w, map[string]string{"outcome": "modified", "candidateId": last.Result.CandidateID, "note": "Updated SDK calls while preserving the flush."})
		default:
			t.Errorf("unexpected editor request %d", turn)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		editorSessions.Add(1)
		in.Config.Endpoint = srv.URL
		return agent.Run(ctx, in)
	}
	state := runTest(t, s, 1)
	task := state.Tasks[0]
	if state.LastError != "" || task.Status != "done" || editorSessions.Load() != 1 || requests.Load() != 9 || reviewRequests.Load() != 2 || len(task.History) != 1 || task.Attempts != 1 {
		t.Fatalf("independent review did not stay inside one repair attempt: %+v; sessions=%d requests=%d reviews=%d", state, editorSessions.Load(), requests.Load(), reviewRequests.Load())
	}
	checkpoint, err := loadRepairCheckpoint(cfg, task.History[0])
	if err != nil || checkpoint.State.ValidationCount != 3 || checkpoint.State.ReviewCount != 2 || checkpoint.State.RequestPending || checkpoint.State.Usage.Turns != 9 || task.History[0].Usage.InputTokens != 900 || len(task.History[0].Reviews) != 2 {
		t.Fatalf("review reset/dropped the shared validation/request budget: %+v, %v", checkpoint, err)
	}
	if got := readTest(t, counter); got != "check\ncheck\ncheck\n" {
		t.Fatalf("expected baseline plus two mechanically passing candidates, without duplicate adoption checks: %q", got)
	}
	if got := readTest(t, filepath.Join(state.Worktree, "A.txt")); got != "Modern.Save()\nModern.Load()\nflush()\n" {
		t.Fatalf("wrong reviewed candidate committed: %q", got)
	}
	if got := readTest(t, filepath.Join(cfg.Root, "A.txt")); got != original {
		t.Fatalf("the original checkout was modified: %q", got)
	}
}

func TestIndependentReviewUnknownTransportStopsWithoutCommitOrAutomaticReplay(t *testing.T) {
	original := "Legacy.Save()\nLegacy.Load()\nflush()\n"
	s, cfg := fixture(t, map[string]string{"A.txt": original})
	cfg.MaxCostUSD, cfg.InputPricePerMillion, cfg.OutputPricePerMillion = 100, 1, 1
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var requests, reviewRequests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := requests.Add(1)
		var body struct {
			Tools []json.RawMessage `json:"tools"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if len(body.Tools) == 0 {
			reviewRequests.Add(1)
			queue, err := store.LoadQueue(cfg.QueuePath)
			if err != nil || len(queue) != 1 || len(queue[0].History) != 1 {
				t.Errorf("review request has no durable attempt: %+v, %v", queue, err)
				w.WriteHeader(500)
				return
			}
			checkpoint, err := loadRepairCheckpoint(cfg, queue[0].History[0])
			if err != nil || checkpoint == nil || !checkpoint.State.RequestPending || checkpoint.State.Usage.Turns != 5 || checkpoint.State.LastCandidate == nil {
				t.Errorf("review was sent before its durable request reservation: %+v, %v", checkpoint, err)
			}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close() // Server may have billed; the client cannot know.
			return
		}
		switch turn {
		case 1:
			engineRepairResponse(w, engineRepairCall("read", "read_rule", map[string]string{"id": "R019"}))
		case 2:
			engineRepairResponse(w, engineRepairCall("plan", "update_state", independentReviewPlan()))
		case 3:
			engineRepairResponse(w, engineRepairCall("valid", "validate_candidate", independentReviewCandidate(original, true, true)))
		case 4:
			queue, err := store.LoadQueue(cfg.QueuePath)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			checkpoint, err := loadRepairCheckpoint(cfg, queue[0].History[0])
			if err != nil || checkpoint.State.LastCandidate == nil {
				t.Errorf("candidate not persisted: %+v, %v", checkpoint, err)
				w.WriteHeader(500)
				return
			}
			independentReviewFinal(w, map[string]string{"outcome": "modified", "candidateId": checkpoint.State.LastCandidate.Result.CandidateID, "note": "Calls updated."})
		default:
			t.Errorf("uncertain review was replayed automatically, request %d", turn)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		in.Config.Endpoint = srv.URL
		return agent.Run(ctx, in)
	}
	state := runTest(t, s, 1)
	task := state.Tasks[0]
	if task.Status != "needs_human" || requests.Load() != 5 || reviewRequests.Load() != 1 || task.History[0].Commit != "" || !task.History[0].Usage.Uncertain {
		t.Fatalf("uncertain review was accepted or retried: %+v, calls=%d", state, requests.Load())
	}
	checkpoint, err := loadRepairCheckpoint(cfg, task.History[0])
	if err != nil || checkpoint == nil || !checkpoint.State.RequestPending || !checkpoint.State.Usage.Uncertain || checkpoint.State.Usage.Turns != 5 || checkpoint.State.Usage.InputTokens != 400 || checkpoint.State.ReviewCount != 1 || len(task.History[0].Reviews) != 1 {
		t.Fatalf("uncertain reviewer accounting was not durable: %+v, %v", checkpoint, err)
	}
	if got := readTest(t, filepath.Join(state.Worktree, "A.txt")); got != original {
		t.Fatalf("unreviewed candidate was adopted: %q", got)
	}
	if err := s.Start(1); err == nil {
		s.Stop()
		s.Wait()
		t.Fatal("ordinary Start unexpectedly resumed a held review")
	}
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	state = runTest(t, s, 1)
	if state.Tasks[0].Status != "needs_human" || requests.Load() != 5 {
		t.Fatalf("explicit resume ignored unknown costs or reset the budget: %+v, calls=%d", state, requests.Load())
	}
}

func TestIndependentReviewRequiredByRunnerForValidatedCandidate(t *testing.T) {
	original := "Legacy.Save()\nLegacy.Load()\nflush()\n"
	s, _ := fixture(t, map[string]string{"A.txt": original})
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		state := *in.RepairState
		state.Plan = engineRepairPlan()
		state.ReadRuleIDs = []string{"R019"}
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		request := independentReviewCandidate(original, true, true)
		result, err := in.ValidateCandidate(ctx, request)
		if err != nil {
			return model.Proposal{}, err
		}
		if !result.Passed {
			t.Errorf("test candidate failed mechanical checks: %+v", result)
		}
		state.LastCandidate = &model.CandidateRecord{Request: request, Result: result}
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		return model.Proposal{Outcome: "modified", CandidateID: result.CandidateID, Edits: request.Edits, RulesApplied: []string{"R019"}, Note: "Attempt to bypass independent review."}, nil
	}
	state := runTest(t, s, 1)
	if task := state.Tasks[0]; task.Status != "needs_human" || task.History[0].Commit != "" {
		t.Fatalf("runner accepted a merely mechanically validated candidate: %+v", state)
	}
	if got := readTest(t, filepath.Join(state.Worktree, "A.txt")); got != original {
		t.Fatalf("unreviewed candidate changed the worktree: %q", got)
	}
}

func TestIndependentReviewExplicitResumeAfterStopRestartsOnlyIsolatedReview(t *testing.T) {
	original := "Legacy.Save()\nLegacy.Load()\nflush()\n"
	s, cfg := fixture(t, map[string]string{"A.txt": original})
	var requests, editorRequests, reviewRequests, sessions atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body struct {
			Tools []json.RawMessage            `json:"tools"`
			Input []map[string]json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		if len(body.Tools) == 0 {
			reviewNumber := reviewRequests.Add(1)
			if len(body.Input) != 1 {
				t.Errorf("resumed review received provider history: %+v", body.Input)
			}
			if reviewNumber == 1 {
				// Cancel while the review POST is in flight, then restart the app.
				s.Stop()
				connection, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = connection.Close()
				}
				return
			}
			if reviewNumber != 2 || editorRequests.Load() != 4 {
				t.Errorf("resume replayed editor work or exceeded the reserved review: editor=%d review=%d", editorRequests.Load(), reviewNumber)
			}
			independentReviewReply(t, w, original, true)
			return
		}
		switch turn := editorRequests.Add(1); turn {
		case 1:
			engineRepairResponse(w, engineRepairCall("read", "read_rule", map[string]string{"id": "R019"}))
		case 2:
			engineRepairResponse(w, engineRepairCall("plan", "update_state", independentReviewPlan()))
		case 3:
			engineRepairResponse(w, engineRepairCall("valid", "validate_candidate", independentReviewCandidate(original, true, true)))
		case 4:
			queue, err := store.LoadQueue(cfg.QueuePath)
			if err != nil {
				t.Error(err)
				w.WriteHeader(500)
				return
			}
			checkpoint, err := loadRepairCheckpoint(cfg, queue[0].History[0])
			if err != nil || checkpoint == nil || checkpoint.State.LastCandidate == nil {
				t.Errorf("candidate not persisted before review: %+v, %v", checkpoint, err)
				w.WriteHeader(500)
				return
			}
			independentReviewFinal(w, map[string]string{"outcome": "modified", "candidateId": checkpoint.State.LastCandidate.Result.CandidateID, "note": "Calls updated with flush retained."})
		default:
			t.Errorf("review resume unnecessarily restarted editor request %d", turn)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	propose := func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		sessions.Add(1)
		in.Config.Endpoint = srv.URL
		return agent.Run(ctx, in)
	}
	s.propose = propose
	first := runTest(t, s, 1)
	if first.Tasks[0].Status != "needs_human" || !first.Tasks[0].History[0].Usage.Uncertain || requests.Load() != 5 {
		t.Fatalf("review stop did not preserve a held uncertain request: %+v, requests=%d", first, requests.Load())
	}
	firstAttempt := first.Tasks[0].History[0]
	checkpoint, err := loadRepairCheckpoint(cfg, firstAttempt)
	if err != nil || checkpoint == nil || checkpoint.State.LastCandidate == nil || !checkpoint.State.LastCandidate.ReviewRequested || checkpoint.State.ReviewCount != 1 || !checkpoint.State.RequestPending {
		t.Fatalf("review phase was not durably resumable: %+v, %v", checkpoint, err)
	}
	settingsPath := s.configPath
	s.Close()
	s = New(settingsPath)
	t.Cleanup(s.Close)
	s.propose = propose
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	second := runTest(t, s, 1)
	task := second.Tasks[0]
	if second.LastError != "" || task.Status != "done" || requests.Load() != 6 || editorRequests.Load() != 4 || reviewRequests.Load() != 2 || sessions.Load() != 2 || len(task.History) != 1 || task.History[0].ID != firstAttempt.ID {
		t.Fatalf("explicit resume did not finish only the saved candidate's review: %+v, requests=%d editors=%d reviews=%d sessions=%d", second, requests.Load(), editorRequests.Load(), reviewRequests.Load(), sessions.Load())
	}
	history := task.History[0]
	if history.Usage.Turns != 6 || history.Usage.InputTokens != 500 || !history.Usage.Uncertain || len(history.Reviews) != 2 || history.Reviews[0].Verdict != "error" || history.Reviews[1].Verdict != "passed" {
		t.Fatalf("review resume reset or double-counted known/unknown usage: %+v", history)
	}
	for _, review := range history.Reviews {
		artifact := filepath.Join(cfg.QueuePath+".artifacts", history.ID+".review-"+review.ID+".json")
		if _, err := os.Stat(artifact); err != nil {
			t.Fatalf("review artifact was not preserved: %v", err)
		}
	}
	if got := readTest(t, filepath.Join(second.Worktree, "A.txt")); got != "Modern.Save()\nModern.Load()\nflush()\n" {
		t.Fatalf("resume adopted different bytes from the reviewed candidate: %q", got)
	}
}
