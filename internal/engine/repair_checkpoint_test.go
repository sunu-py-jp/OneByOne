package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func engineRepairPlan() model.RepairPlan {
	return model.RepairPlan{Revision: 1,
		RuleDecisions: []model.PlanDecision{{RuleID: "R001", Decision: "no_change", Reason: "Unrelated behavior already preserved"}, {RuleID: "R019", Decision: "modify", Reason: "Both SDK calls require updating"}},
		Items:         []model.PlanItem{{ID: "P1", RuleID: "R019", Location: "Legacy.Save()", Change: "Use Modern.Save()", Expected: "Save uses supported API", Status: "proposed"}, {ID: "P2", RuleID: "R019", Location: "Legacy.Load()", Change: "Use Modern.Load()", Expected: "Load uses supported API", Status: "proposed"}},
	}
}

func engineRepairCall(id, name string, args any) map[string]any {
	b, _ := json.Marshal(args)
	return map[string]any{"type": "function_call", "call_id": id, "name": name, "arguments": string(b)}
}

func engineRepairResponse(w http.ResponseWriter, item any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "completed", "output": []any{item}, "usage": map[string]any{"input_tokens": 100, "output_tokens": 30, "input_tokens_details": map[string]any{"cached_tokens": 20}}})
}

func TestRepairCheckpointValidatesAndRepairsInOneAttemptBeforeCommit(t *testing.T) {
	// Normalized edit coordinates and raw-file provenance must coexist.
	original := "\ufeffLegacy.Save()\r\nLegacy.Load()\r\n"
	s, cfg := fixture(t, map[string]string{"A.txt": original})
	counter := filepath.Join(t.TempDir(), "checks.txt")
	cfg.CheckCommands = []model.Command{{Name: "count checks", Executable: os.Args[0], Args: []string{"-test.run=^TestRepairCheckpointCheckCounterProcess$", "--", "repair-check-counter", counter}}}
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	var proposalCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		turn := int(calls.Add(1))
		queue, err := store.LoadQueue(cfg.QueuePath)
		if err != nil || len(queue) != 1 || len(queue[0].History) != 1 {
			t.Errorf("request reached transport before queue journal: %+v, %v", queue, err)
			w.WriteHeader(500)
			return
		}
		checkpoint, err := loadRepairCheckpoint(cfg, queue[0].History[0])
		if err != nil || checkpoint == nil || !checkpoint.State.RequestPending || checkpoint.State.Usage.Turns != turn || checkpoint.State.RequestID == "" {
			t.Errorf("request reached transport before durable reservation: %+v, %v", checkpoint, err)
			w.WriteHeader(500)
			return
		}
		var body struct {
			Input []map[string]json.RawMessage `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		request := model.CandidateRequest{PlanRevision: 1, BaseHash: digest([]byte(original)), Edits: []model.Edit{{OldText: "Legacy.Save()", NewText: "Modern.Save()", ItemIDs: []string{"P1", "P2"}}}, AddressedItemIDs: []string{"P1", "P2"}}
		switch turn {
		case 1:
			engineRepairResponse(w, engineRepairCall("read", "read_rule", map[string]string{"id": "R019"}))
		case 2:
			plan := engineRepairPlan()
			engineRepairResponse(w, engineRepairCall("plan", "update_state", model.PlanUpdate{ExpectedRevision: 0, RuleDecisions: plan.RuleDecisions, Items: plan.Items}))
		case 3:
			engineRepairResponse(w, engineRepairCall("bad", "validate_candidate", request))
		case 4:
			last := checkpoint.State.LastCandidate
			if last == nil || last.Result.Passed || len(last.Result.Diagnostics) == 0 || last.Result.Diagnostics[0].Line != 2 {
				t.Errorf("failed candidate not available to repair: %+v", last)
			}
			request.Edits[0].ItemIDs = []string{"P1"}
			request.Edits = append(request.Edits, model.Edit{OldText: "Legacy.Load()", NewText: "Modern.Load()", ItemIDs: []string{"P2"}})
			engineRepairResponse(w, engineRepairCall("good", "validate_candidate", request))
		case 5:
			last := checkpoint.State.LastCandidate
			if last == nil || !last.Result.Passed {
				t.Errorf("passed candidate not persisted before final request: %+v", last)
				w.WriteHeader(500)
				return
			}
			final, _ := json.Marshal(map[string]string{"outcome": "modified", "candidateId": last.Result.CandidateID, "note": "Both calls updated after residue feedback"})
			engineRepairResponse(w, map[string]any{"type": "message", "status": "completed", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": string(final)}}})
		case 6:
			independentReviewFinal(w, map[string]any{"baseHash": digest([]byte(original)), "candidateHash": digest([]byte("\ufeffModern.Save()\r\nModern.Load()\r\n")), "verdict": "passed", "summary": "Both API calls were updated while preserving file behavior.", "assessments": []any{map[string]string{"ruleId": "R001", "status": "satisfied", "reason": "Unrelated behavior is preserved."}, map[string]string{"ruleId": "R019", "status": "satisfied", "reason": "Both calls use the supported SDK."}}, "issues": []any{}})
		default:
			t.Errorf("unexpected extra LLM request %d", turn)
			w.WriteHeader(500)
		}
	}))
	defer srv.Close()
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		proposalCalls.Add(1)
		in.Config.Endpoint = srv.URL
		return agent.Run(ctx, in)
	}
	state := runTest(t, s, 1)
	if state.LastError != "" || state.Tasks[0].Status != "done" || calls.Load() != 6 || proposalCalls.Load() != 1 {
		t.Fatalf("repair escaped the single attempt: %+v; calls=%d sessions=%d", state, calls.Load(), proposalCalls.Load())
	}
	task := state.Tasks[0]
	if task.Attempts != 1 || len(task.History) != 1 || task.History[0].Usage.Turns != 6 || task.History[0].Usage.InputTokens != 600 {
		t.Fatalf("wrong durable accounting: %+v", task)
	}
	checkpoint, err := loadRepairCheckpoint(cfg, task.History[0])
	if err != nil || checkpoint.State.ValidationCount != 2 || checkpoint.State.RequestPending {
		t.Fatalf("repair journal not settled: %+v, %v", checkpoint, err)
	}
	if got := readTest(t, counter); got != "check\ncheck\n" {
		t.Fatalf("configured tests should run for baseline and passing candidate, never duplicate during final adoption: %q", got)
	}
	if got := readTest(t, filepath.Join(state.Worktree, "A.txt")); got != "\ufeffModern.Save()\r\nModern.Load()\r\n" {
		t.Fatalf("wrong adopted bytes: %q", got)
	}
	if got := readTest(t, filepath.Join(cfg.Root, "A.txt")); got != original {
		t.Fatalf("user checkout was modified: %q", got)
	}
}

func TestRepairCheckpointExplicitResumeAcrossRestartKeepsAttemptAndCounters(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\nLegacy.Load()\n"})
	cfg.MaxAttempts = 1
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	var firstID string
	calls := 0
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		calls++
		state := *in.RepairState
		if calls == 1 {
			state.Plan = engineRepairPlan()
			state.ReadRuleIDs = []string{"R019"}
			state.Usage = model.Usage{InputTokens: 1234, OutputTokens: 456, Turns: 3, CostUSD: .01}
			state.ToolCalls, state.ValidationCount, state.ElapsedMS = 4, 1, 2500
			if err := in.SaveRepairState(state); err != nil {
				return model.Proposal{}, err
			}
			firstID = s.Snapshot().Tasks[0].History[0].ID
			return model.Proposal{}, errors.New("test stop after planned work")
		}
		if !in.AllowUncertainResume || state.Plan.Revision != 1 || state.ToolCalls != 4 || state.ValidationCount != 1 || state.ElapsedMS != 2500 || state.Usage.Turns != 3 || state.Usage.InputTokens != 1234 {
			t.Errorf("resume discarded plan or budgets: %+v", state)
		}
		request := model.CandidateRequest{PlanRevision: 1, BaseHash: in.BaseHash, AddressedItemIDs: []string{"P1", "P2"}, Edits: []model.Edit{{OldText: "Legacy.Save()", NewText: "Modern.Save()"}, {OldText: "Legacy.Load()", NewText: "Modern.Load()"}}}
		result, err := in.ValidateCandidate(ctx, request)
		if err != nil {
			return model.Proposal{}, err
		}
		state.LastCandidate = &model.CandidateRecord{Request: request, Result: result}
		review := independentReviewAcceptedFixture(result, request.BaseHash)
		state.LastCandidate.Review = &review
		state.Reviews = append(state.Reviews, review)
		state.ReviewCount++
		state.ValidationCount++
		state.ToolCalls++
		state.ElapsedMS += 100
		state.Usage.Turns++
		state.Usage.InputTokens += 100
		state.Usage.CostUSD += .001
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		return model.Proposal{Outcome: "modified", CandidateID: result.CandidateID, Edits: request.Edits, RulesApplied: []string{"R019"}, Note: "continued saved work"}, nil
	}
	first := runTest(t, s, 1)
	if first.Tasks[0].Status != "needs_human" || calls != 1 {
		t.Fatalf("first stop was auto-retried: %+v", first)
	}
	proposer, settingsPath := s.propose, s.configPath
	s.Close()
	s = New(settingsPath)
	t.Cleanup(s.Close)
	s.propose = proposer
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	second := runTest(t, s, 1)
	task := second.Tasks[0]
	if second.LastError != "" || task.Status != "done" || task.Attempts != 1 || len(task.History) != 1 || task.History[0].ID != firstID {
		t.Fatalf("explicit resume created a new attempt: %+v", second)
	}
	if task.History[0].Usage.Turns != 4 || task.History[0].Usage.InputTokens != 1334 || task.History[0].Usage.CostUSD < .0109 {
		t.Fatalf("resume reset or double-counted usage: %+v", task.History[0].Usage)
	}
}

func TestRepairCheckpointChangedProvenanceInvalidatesOnlyPlan(t *testing.T) {
	in := candidateFixture(t, "Legacy.Save()\n")
	c := repairCheckpoint{Version: 1, AttemptID: "attempt", File: in.File, BaseCommit: in.Head, InputHash: in.InputHash, RuleHash: in.Catalog.Hash, SettingsHash: repairSettingsHash(in.Config), State: model.RepairState{Version: 1, Plan: engineRepairPlan(), LastCandidate: &model.CandidateRecord{Result: model.CandidateValidation{Passed: true}}, Usage: model.Usage{Turns: 8, CostUSD: .1}, ElapsedMS: 999, ValidationCount: 2, ToolCalls: 7, ReadBytes: 100, ReadRuleIDs: []string{"R019"}}}
	if resetRepairPlan(&c, in.Config, in.Catalog, in.File, in.Head, in.InputHash) {
		t.Fatal("unchanged provenance invalidated plan")
	}
	changed := in.Config
	changed.TimeoutSeconds++
	if !resetRepairPlan(&c, changed, in.Catalog, in.File, in.Head, in.InputHash) {
		t.Fatal("changed settings retained validated plan")
	}
	if c.State.Plan.Revision != 0 || c.State.LastCandidate != nil || len(c.State.ReadRuleIDs) != 0 {
		t.Fatalf("stale work retained: %+v", c.State)
	}
	if c.State.Usage.Turns != 8 || c.State.Usage.CostUSD != .1 || c.State.ElapsedMS != 999 || c.State.ValidationCount != 2 || c.State.ToolCalls != 7 || c.State.ReadBytes != 100 {
		t.Fatalf("invalidation reset counters: %+v", c.State)
	}
}

func TestRepairCheckpointCrashBeforeNewJournalRetainsOlderFileBudgets(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxCostUSD = 0
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		state := *in.RepairState
		state.Usage = model.Usage{Turns: 4, InputTokens: 700, CostUSD: .05}
		state.ToolCalls, state.ValidationCount, state.ElapsedMS, state.ReadBytes = 7, 2, 2000, 800
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		return successfulProposal(ctx, in)
	}
	first := runTest(t, s, 1)
	if first.Tasks[0].Status != "done" {
		t.Fatalf("setup did not finish: %+v", first)
	}
	// Model the persisted window after a manual rerun appended its new running
	// attempt, but before that attempt attached its first repair-state file.
	head := gitTest(t, first.Worktree, "rev-parse", "HEAD")
	s.mu.Lock()
	task := &s.state.Tasks[0]
	task.Attempts++
	task.Status, task.ResumeRequested = "running", false
	task.History = append(task.History, model.Attempt{ID: uid(), Number: task.Attempts, BaseCommit: head, StartedAt: now()})
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	settingsPath := s.configPath
	s.Close()
	s = New(settingsPath)
	t.Cleanup(s.Close)
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	called := false
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		called = true
		state := *in.RepairState
		if state.ToolCalls != 7 || state.ValidationCount != 2 || state.ElapsedMS != 2000 || state.ReadBytes != 800 || state.Usage.Turns < 4 || state.Usage.InputTokens < 700 || state.Usage.CostUSD < .05 {
			t.Errorf("crash before new checkpoint reset file budgets: %+v", state)
		}
		return model.Proposal{Outcome: "needs_human", Note: "budget retention checked"}, nil
	}
	runTest(t, s, 1)
	if !called {
		t.Fatal("explicit unpriced resume did not reach the retained state")
	}
}

func TestRepairCheckpointChangedSettingsPreservesPriorArtifacts(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\nLegacy.Load()\n"})
	calls := 0
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		calls++
		state := *in.RepairState
		if calls == 1 {
			state.Plan = engineRepairPlan()
			state.ToolCalls, state.ValidationCount, state.ElapsedMS = 3, 1, 1000
			state.Usage = model.Usage{Turns: 3, InputTokens: 100}
			request := model.CandidateRequest{PlanRevision: 1, BaseHash: in.BaseHash, AddressedItemIDs: []string{"P1", "P2"}, Edits: []model.Edit{{OldText: "Legacy.Save()", NewText: "Modern.Save()"}}}
			result, err := in.ValidateCandidate(ctx, request)
			if err != nil {
				return model.Proposal{}, err
			}
			state.LastCandidate = &model.CandidateRecord{Request: request, Result: result}
			if err := in.SaveRepairState(state); err != nil {
				return model.Proposal{}, err
			}
		} else {
			if state.Plan.Revision != 0 || state.LastCandidate != nil || state.ToolCalls != 3 || state.ValidationCount != 1 || state.ElapsedMS != 1000 || state.Usage.Turns != 3 {
				t.Errorf("settings change did not invalidate work while retaining counters: %+v", state)
			}
		}
		return model.Proposal{Outcome: "needs_human", Note: "review needed"}, nil
	}
	first := runTest(t, s, 1)
	if first.Tasks[0].Status != "needs_human" {
		t.Fatalf("first result: %+v", first)
	}
	firstAttempt := first.Tasks[0].History[0]
	before := readTest(t, filepath.Join(cfg.QueuePath+".artifacts", firstAttempt.ID)+".before")
	after := readTest(t, filepath.Join(cfg.QueuePath+".artifacts", firstAttempt.ID)+".after")
	diff := readTest(t, firstAttempt.DiffPath)
	cfg.TimeoutSeconds = cfg.EffectiveTimeoutSeconds() + 1
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	second := runTest(t, s, 1)
	history := second.Tasks[0].History
	if second.LastError != "" || len(history) != 2 || history[1].ID == firstAttempt.ID || history[1].DiffPath != "" || history[1].OutputHash != "" || len(history[1].Checks) != 0 {
		t.Fatalf("new provenance reused stale candidate evidence: %+v", second)
	}
	if readTest(t, filepath.Join(cfg.QueuePath+".artifacts", firstAttempt.ID)+".before") != before || readTest(t, filepath.Join(cfg.QueuePath+".artifacts", firstAttempt.ID)+".after") != after || readTest(t, firstAttempt.DiffPath) != diff {
		t.Fatal("replanning overwrote prior review artifacts")
	}
}

func TestRepairCheckpointCheckCounterProcess(t *testing.T) {
	for i, arg := range os.Args {
		if arg != "repair-check-counter" || i+1 >= len(os.Args) {
			continue
		}
		f, err := os.OpenFile(os.Args[i+1], os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		_, err = f.WriteString("check\n")
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
}

func TestRepairSettingsHashOmitsCredentials(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Credential, cfg.CredentialSet = "first-secret", true
	before := repairSettingsHash(cfg)
	cfg.Credential, cfg.CredentialSet = "different-secret", false
	if after := repairSettingsHash(cfg); after != before || strings.Contains(after, "secret") {
		t.Fatal("credential fingerprint entered repair identity")
	}
}
