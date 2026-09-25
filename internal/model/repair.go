package model

const RepairStateVersion = 1

// Plan decisions and item statuses are model claims, never verification results.
type PlanDecision struct {
	RuleID   string `json:"ruleId"`
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type PlanItem struct {
	ID         string `json:"id"`
	RuleID     string `json:"ruleId"`
	Location   string `json:"location"`
	Risk       string `json:"risk,omitempty"`
	Change     string `json:"change"`
	Expected   string `json:"expected"`
	Status     string `json:"status"`
	HoldReason string `json:"holdReason,omitempty"`
}

type RepairPlan struct {
	Revision      int            `json:"revision"`
	RuleDecisions []PlanDecision `json:"ruleDecisions"`
	Items         []PlanItem     `json:"items"`
}

type PlanUpdate struct {
	ExpectedRevision int            `json:"expectedRevision"`
	RuleDecisions    []PlanDecision `json:"ruleDecisions"`
	Items            []PlanItem     `json:"items"`
}

// Edits always address the immutable original file, including after a failed
// validation. AddressedItemIDs describes the whole replacement candidate.
type CandidateRequest struct {
	PlanRevision     int      `json:"planRevision"`
	BaseHash         string   `json:"baseHash"`
	Edits            []Edit   `json:"edits"`
	AddressedItemIDs []string `json:"addressedItemIds"`
}

type CandidateDiagnostic struct {
	Check     string `json:"check"`
	Message   string `json:"message"`
	LineBasis string `json:"lineBasis"`
	Excerpt   string `json:"excerpt"`
	Line      int    `json:"line"`
	ItemID    string `json:"itemId,omitempty"`
}

type CandidateValidation struct {
	CandidateID      string                `json:"candidateId"`
	CandidateHash    string                `json:"candidateHash"`
	PlanRevision     int                   `json:"planRevision"`
	Passed           bool                  `json:"passed"`
	Checks           []Check               `json:"checks"`
	Diagnostics      []CandidateDiagnostic `json:"diagnostics"`
	RemainingItemIDs []string              `json:"remainingItemIds"`
	EditRanges       []EditLineRange       `json:"editRanges,omitempty"`
}

type CandidateRecord struct {
	Request         CandidateRequest    `json:"request"`
	Result          CandidateValidation `json:"result"`
	ReviewRequested bool                `json:"reviewRequested,omitempty"`
	ReviewNote      string              `json:"reviewNote,omitempty"`
	Review          *IndependentReview  `json:"review,omitempty"`
}

// RepairState is a bounded checkpoint, not a model-provider conversation. The
// runner owns persistence, provenance hashes and accounting across resumptions.
type RepairState struct {
	Version         int                 `json:"version"`
	Plan            RepairPlan          `json:"plan"`
	LastCandidate   *CandidateRecord    `json:"lastCandidate"`
	Usage           Usage               `json:"usage"`
	ElapsedMS       int64               `json:"elapsedMs"`
	ToolCalls       int                 `json:"toolCalls"`
	ReadBytes       int                 `json:"readBytes"`
	ValidationCount int                 `json:"validationCount"`
	RequestPending  bool                `json:"requestPending"`
	RequestID       string              `json:"requestId"`
	ReadRuleIDs     []string            `json:"readRuleIds"`
	ReviewCount     int                 `json:"reviewCount"`
	Reviews         []IndependentReview `json:"reviews,omitempty"`
	RequestKind     string              `json:"requestKind,omitempty"`
}
