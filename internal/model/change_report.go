package model

// ChangeReportItem combines a recorded plan with the runner's adoption result.
// Status is derived by the runner; an LLM's proposed claim is never fixed.
type ChangeReportItem struct {
	ID         string            `json:"id"`
	RuleID     string            `json:"ruleId"`
	RuleTitle  string            `json:"ruleTitle"`
	Location   string            `json:"location"`
	Risk       string            `json:"risk"`
	Change     string            `json:"change"`
	Expected   string            `json:"expected"`
	Status     string            `json:"status"`
	Reason     string            `json:"reason"`
	LineRanges []ChangeLineRange `json:"lineRanges,omitempty"`
	// SourceAttemptID identifies the attempt which supplied this display row.
	// Detail responses add it without rewriting the saved attempt report.
	SourceAttemptID string `json:"sourceAttemptId,omitempty"`
}

// ChangeLineRange is computed from an exact edit, never from a model's prose
// location. Coordinates are 1-based inclusive; 0/0 means no text on that side.
type ChangeLineRange struct {
	BeforeStart int `json:"beforeStart"`
	BeforeEnd   int `json:"beforeEnd"`
	AfterStart  int `json:"afterStart"`
	AfterEnd    int `json:"afterEnd"`
}

type EditLineRange struct {
	ItemIDs []string `json:"itemIds"`
	ChangeLineRange
}
