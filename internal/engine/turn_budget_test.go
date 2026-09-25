package engine

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func seedHistoricalTurnUsage(t *testing.T, s *Service) []model.Attempt {
	t.Helper()
	history := []model.Attempt{
		{ID: "first", Number: 1, Outcome: "failed", Usage: model.Usage{Turns: 11, InputTokens: 100}},
		{ID: "second", Number: 2, Outcome: "failed", Usage: model.Usage{Turns: 11, InputTokens: 200}},
	}
	s.mu.Lock()
	s.state.Tasks[0].Attempts = len(history)
	s.state.Tasks[0].History = append([]model.Attempt{}, history...)
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	return copyTask(model.Task{History: history}).History
}

func TestTurnBudgetHistoricalUsageDoesNotConsumeNewExecution(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxTurns = 20
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	history := seedHistoricalTurnUsage(t, s)
	calls := 0
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		calls++
		state := *in.RepairState
		if state.Usage.Turns != 21+calls || state.Usage.InputTokens != 300 || in.BudgetBaseline != agent.BudgetBaselineFor(state) {
			t.Errorf("new execution inherited used turns or lost history: state=%+v baseline=%+v", state, in.BudgetBaseline)
		}
		state.Usage.Turns++
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		return model.Proposal{Outcome: "needs_human", Note: "fixture pause"}, nil
	}
	first := runTest(t, s, 1)
	if first.LastError != "" || calls != 1 || first.Tasks[0].Attempts != 3 || sumUsage(first.Tasks[0].History).Turns != 23 {
		t.Fatalf("historical 22 turns blocked a fresh 20-turn run: calls=%d state=%+v", calls, first)
	}
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatalf("retry still requires a larger turn limit: %v", err)
	}
	second := runTest(t, s, 1)
	if second.LastError != "" || calls != 2 || sumUsage(second.Tasks[0].History).Turns != 24 || !reflect.DeepEqual(history, copyTask(second.Tasks[0]).History[:2]) {
		t.Fatalf("re-execution lost history or did not reset its allowance: calls=%d state=%+v", calls, second)
	}
}

func TestTurnBudgetRestartRetainsPlanAndAttemptWithFreshRuntimeAllowance(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\nLegacy.Load()\n"})
	cfg.MaxTurns = 3
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	var saved model.RepairState
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		calls++
		state := *in.RepairState
		if in.BudgetBaseline != agent.BudgetBaselineFor(state) {
			t.Errorf("execution did not begin at zero: state=%+v baseline=%+v", state, in.BudgetBaseline)
		}
		if calls == 1 {
			state.Plan = engineRepairPlan()
			state.ReadRuleIDs = []string{"R019"}
			state.Usage = model.Usage{Turns: 3, InputTokens: 100}
			state.ToolCalls, state.ElapsedMS, state.ReadBytes = 32, int64(cfg.EffectiveTimeoutSeconds())*1000, 512<<10
			state.ValidationCount, state.ReviewCount = cfg.EffectiveMaxAttempts(), cfg.EffectiveMaxAttempts()
			state.LastCandidate = &model.CandidateRecord{Request: model.CandidateRequest{PlanRevision: 1, BaseHash: in.BaseHash}, Result: model.CandidateValidation{CandidateID: "saved-candidate", PlanRevision: 1}}
			saved = state
		} else {
			if !reflect.DeepEqual(state, saved) {
				t.Errorf("restart discarded saved plan/candidate/counters: got=%+v want=%+v", state, saved)
			}
			state.Usage.Turns++
			state.Usage.InputTokens += 50
		}
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		return model.Proposal{Outcome: "needs_human", Note: "fixture pause"}, nil
	}
	first := runTest(t, s, 1).Tasks[0]
	if _, err := s.RetryTasks([]string{first.File}); err != nil {
		t.Fatal(err)
	}
	second := runTest(t, s, 1).Tasks[0]
	if calls != 2 || second.Attempts != 1 || len(second.History) != 1 || second.History[0].ID != first.History[0].ID || second.History[0].Usage.Turns != 4 || second.History[0].Usage.InputTokens != 150 {
		t.Fatalf("restart replaced saved work or erased accounting: calls=%d task=%+v", calls, second)
	}
}

func TestTurnBudgetAutomaticRetriesShareAllowanceUntilNextStart(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	cfg.MaxAttempts, cfg.MaxTurns = 2, 2
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	calls := 0
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		state := *in.RepairState
		expectedBaseline := calls / 2 * 2
		if state.Usage.Turns != calls || in.BudgetBaseline.Turns != expectedBaseline || state.Usage.Turns-in.BudgetBaseline.Turns != calls%2 {
			t.Errorf("automatic retry reset its budget: call=%d state=%+v baseline=%+v", calls, state, in.BudgetBaseline)
		}
		calls++
		state.Usage.Turns++
		if err := in.SaveRepairState(state); err != nil {
			return model.Proposal{}, err
		}
		// The legacy-symbol gate rejects this claim and triggers an automatic retry.
		return model.Proposal{Outcome: "skipped", Note: "force mechanical rejection"}, nil
	}
	first := runTest(t, s, 1)
	if first.LastError != "" || calls != 2 || first.Tasks[0].Status != "needs_human" || sumUsage(first.Tasks[0].History).Turns != 2 || first.Usage.Turns != 2 {
		t.Fatalf("first execution did not enforce its own attempt limit: calls=%d state=%+v", calls, first)
	}
	if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
		t.Fatal(err)
	}
	second := runTest(t, s, 1)
	if second.LastError != "" || calls != 4 || second.Tasks[0].Status != "needs_human" || sumUsage(second.Tasks[0].History).Turns != 4 || second.Usage.Turns != 4 {
		t.Fatalf("historical attempts consumed the next allowance: calls=%d state=%+v", calls, second)
	}
	for _, state := range []model.State{first, second} {
		found := false
		for _, workspace := range state.Workspaces {
			if workspace.ID != state.ActiveWorkspaceID {
				continue
			}
			for _, issue := range workspace.Issues {
				found = found || strings.Contains(issue.Message, state.Tasks[0].Note)
			}
		}
		if !found {
			t.Fatalf("workspace issue did not reflect final attempt-limit status: %+v", state.Workspaces)
		}
	}
}

func TestTurnBudgetBaselineUsesGreaterQueueOrCheckpointUsage(t *testing.T) {
	for _, ahead := range []string{"checkpoint", "queue"} {
		t.Run(ahead, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
			calls := 0
			s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
				calls++
				state := *in.RepairState
				if calls == 1 {
					state.Usage = model.Usage{Turns: 3, InputTokens: 300}
				} else {
					if state.Usage.Turns != 5 || state.Usage.InputTokens != 500 || in.BudgetBaseline.Turns != 5 {
						t.Errorf("baseline did not reconcile usage: state=%+v baseline=%+v", state, in.BudgetBaseline)
					}
					state.Usage.Turns++
				}
				if err := in.SaveRepairState(state); err != nil {
					return model.Proposal{}, err
				}
				return model.Proposal{Outcome: "needs_human", Note: "fixture pause"}, nil
			}
			first := runTest(t, s, 1).Tasks[0]
			if ahead == "checkpoint" {
				checkpoint, err := loadRepairCheckpoint(cfg, first.History[0])
				if err != nil || checkpoint == nil {
					t.Fatalf("load checkpoint: %+v %v", checkpoint, err)
				}
				checkpoint.State.Usage = model.Usage{Turns: 5, InputTokens: 500}
				if _, err := saveRepairCheckpoint(cfg, checkpoint); err != nil {
					t.Fatal(err)
				}
			} else {
				s.mu.Lock()
				s.state.Tasks[0].History[0].Usage = model.Usage{Turns: 5, InputTokens: 500}
				s.mu.Unlock()
				if err := s.persist(); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := s.RetryTasks([]string{"A.txt"}); err != nil {
				t.Fatal(err)
			}
			second := runTest(t, s, 1)
			if second.LastError != "" || calls != 2 || sumUsage(second.Tasks[0].History).Turns != 6 || sumUsage(second.Tasks[0].History).InputTokens != 500 {
				t.Fatalf("resume lost newer usage or counted twice: calls=%d state=%+v", calls, second)
			}
		})
	}
}

func TestTurnBudgetSettingsMatchOnlyIgnoresTurnLimit(t *testing.T) {
	cfg := DefaultConfig()
	current, previous := repairSettingsHash(cfg), fullRepairSettingsHash(cfg)
	cfg.MaxTurns = 32
	for _, stored := range []string{current, previous} {
		if !repairSettingsMatch(stored, cfg) {
			t.Fatal("turn adjustment discarded saved settings")
		}
		changed := cfg
		changed.TimeoutSeconds++
		if repairSettingsMatch(stored, changed) {
			t.Fatal("execution settings change reused saved work")
		}
		changed = cfg
		changed.Deployment = "different-model"
		if repairSettingsMatch(stored, changed) {
			t.Fatal("model change reused saved work")
		}
	}
}

func TestTurnBudgetErrorExplainsFreshExecutionWithoutNewQueue(t *testing.T) {
	message := agent.TurnLimitError(32, 32).Error()
	if !strings.Contains(message, "再実行") || !strings.Contains(message, "0") || strings.Contains(message, "別のキュー保存先") || strings.Contains(message, "累計使用数より増やして") {
		t.Fatalf("recovery still requires a cumulative cap or replacement queue: %s", message)
	}
}
