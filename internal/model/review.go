package model

type ReviewAssessment struct {
	RuleID string `json:"ruleId"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type ReviewIssue struct {
	Kind            string `json:"kind,omitempty"`
	RuleID          string `json:"ruleId"`
	Location        string `json:"location"`
	LineBasis       string `json:"lineBasis"`
	Excerpt         string `json:"excerpt"`
	StartLine       int    `json:"startLine"`
	EndLine         int    `json:"endLine"`
	Reason          string `json:"reason"`
	RequestedChange string `json:"requestedChange"`
}

// ReviewHold is the only editor judgment supplied to the independent reviewer:
// an explicit scope which must remain unresolved and unchanged.
type ReviewHold struct {
	ItemID          string           `json:"itemId"`
	RuleID          string           `json:"ruleId"`
	SourceLocations []SourceLocation `json:"sourceLocations"`
	Reason          string           `json:"reason"`
}

type ReviewHoldAssessment struct {
	ItemID string `json:"itemId"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// Identity and usage are assigned by the harness, never by the reviewer.
type IndependentReview struct {
	ID              string                 `json:"id"`
	CandidateID     string                 `json:"candidateId"`
	BaseHash        string                 `json:"baseHash"`
	CandidateHash   string                 `json:"candidateHash"`
	PlanRevision    int                    `json:"planRevision"`
	Verdict         string                 `json:"verdict"`
	Summary         string                 `json:"summary"`
	Assessments     []ReviewAssessment     `json:"assessments"`
	Issues          []ReviewIssue          `json:"issues"`
	HoldAssessments []ReviewHoldAssessment `json:"holdAssessments,omitempty"`
	Usage           Usage                  `json:"usage"`
	StartedAt       string                 `json:"startedAt"`
	FinishedAt      string                 `json:"finishedAt"`
}
