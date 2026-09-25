package model

type ReviewAssessment struct {
	RuleID string `json:"ruleId"`
	Status string `json:"status"`
	Reason string `json:"reason"`
}

type ReviewIssue struct {
	RuleID          string `json:"ruleId"`
	Location        string `json:"location"`
	LineBasis       string `json:"lineBasis"`
	Excerpt         string `json:"excerpt"`
	Reason          string `json:"reason"`
	RequestedChange string `json:"requestedChange"`
}

// Identity and usage are assigned by the harness, never by the reviewer.
type IndependentReview struct {
	ID            string             `json:"id"`
	CandidateID   string             `json:"candidateId"`
	BaseHash      string             `json:"baseHash"`
	CandidateHash string             `json:"candidateHash"`
	PlanRevision  int                `json:"planRevision"`
	Verdict       string             `json:"verdict"`
	Summary       string             `json:"summary"`
	Assessments   []ReviewAssessment `json:"assessments"`
	Issues        []ReviewIssue      `json:"issues"`
	Usage         Usage              `json:"usage"`
	StartedAt     string             `json:"startedAt"`
	FinishedAt    string             `json:"finishedAt"`
}
