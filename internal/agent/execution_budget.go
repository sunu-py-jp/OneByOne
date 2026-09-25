package agent

import (
	"errors"
	"math"

	"onebyone/internal/model"
)

// ExecutionBudgetBaseline is captured once per file when the user starts an
// execution. Automatic repair/resume calls in that execution reuse it. Journal
// counters remain lifetime totals for reporting and crash recovery.
type ExecutionBudgetBaseline struct {
	Turns           int
	ElapsedMS       int64
	ToolCalls       int
	ReadBytes       int
	ValidationCount int
	ReviewCount     int
}

func BudgetBaselineFor(state model.RepairState) ExecutionBudgetBaseline {
	return ExecutionBudgetBaseline{
		Turns: state.Usage.Turns, ElapsedMS: state.ElapsedMS,
		ToolCalls: state.ToolCalls, ReadBytes: state.ReadBytes,
		ValidationCount: state.ValidationCount, ReviewCount: state.ReviewCount,
	}
}

func (base ExecutionBudgetBaseline) validate(state model.RepairState) error {
	total := BudgetBaselineFor(state)
	for _, counters := range [][2]int{{base.Turns, total.Turns}, {base.ToolCalls, total.ToolCalls}, {base.ReadBytes, total.ReadBytes}, {base.ValidationCount, total.ValidationCount}, {base.ReviewCount, total.ReviewCount}} {
		if counters[0] < 0 || counters[0] > counters[1] {
			return errors.New("Execution budget baseline is outside the repair-state counters")
		}
	}
	// Configured timeouts cannot exceed one hour. Leave that headroom when
	// reserving an in-flight deadline in the cumulative journal.
	if base.ElapsedMS < 0 || base.ElapsedMS > total.ElapsedMS || total.ElapsedMS > math.MaxInt64-3600000 {
		return errors.New("Execution elapsed-time baseline is outside the repair-state counters")
	}
	return nil
}

func (base ExecutionBudgetBaseline) used(state model.RepairState) ExecutionBudgetBaseline {
	return ExecutionBudgetBaseline{
		Turns: state.Usage.Turns - base.Turns, ElapsedMS: state.ElapsedMS - base.ElapsedMS,
		ToolCalls: state.ToolCalls - base.ToolCalls, ReadBytes: state.ReadBytes - base.ReadBytes,
		ValidationCount: state.ValidationCount - base.ValidationCount, ReviewCount: state.ReviewCount - base.ReviewCount,
	}
}
