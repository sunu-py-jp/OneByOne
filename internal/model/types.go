package model

import "context"

type Config struct {
	// AcquireToken exists only during a local execution. Never serialize an
	// OAuth token provider into workspaces, reports, or the frontend bridge.
	AcquireToken               func(context.Context) (string, error) `json:"-"`
	LLMConnectionID            string                                `json:"llmConnectionId,omitempty"`
	Provider                   string                                `json:"provider,omitempty"`
	Root                       string                                `json:"root"`
	RulesPath                  string                                `json:"rulesPath"`
	ExcludedRuleIDs            []string                              `json:"excludedRuleIds,omitempty"`
	QueuePath                  string                                `json:"queuePath"`
	RGPath                     string                                `json:"rgPath"`
	Endpoint                   string                                `json:"endpoint,omitempty"`
	Deployment                 string                                `json:"deployment,omitempty"`
	AuthMode                   string                                `json:"authMode,omitempty"`
	Credential                 string                                `json:"credential,omitempty"`
	CredentialSet              bool                                  `json:"credentialSet,omitempty"`
	Concurrency                int                                   `json:"concurrency,omitempty"`
	MaxAttempts                int                                   `json:"maxAttempts"`
	MaxTurns                   int                                   `json:"maxTurns"`
	MaxOutputTokens            int                                   `json:"maxOutputTokens"`
	MaxFileBytes               int                                   `json:"maxFileBytes"`
	TimeoutSeconds             int                                   `json:"timeoutSeconds"`
	MaxCostUSD                 float64                               `json:"maxCostUSD"`
	InputPricePerMillion       float64                               `json:"inputPricePerMillion"`
	CachedInputPricePerMillion float64                               `json:"cachedInputPricePerMillion"`
	OutputPricePerMillion      float64                               `json:"outputPricePerMillion"`
}

type RuleEdit struct {
	ExpectedRevision string `json:"expectedRevision,omitempty"`
	ID               string `json:"id"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	PathPattern      string `json:"pathPattern"`
	ContentPattern   string `json:"contentPattern"`
	Body             string `json:"body"`
}

type RuleDefinition struct {
	ID             string `json:"id" yaml:"id"`
	Name           string `json:"name" yaml:"name"`
	Description    string `json:"description" yaml:"description"`
	PathPattern    string `json:"pathPattern" yaml:"path_pattern"`
	ContentPattern string `json:"contentPattern" yaml:"content_pattern"`
	Body           string `json:"body" yaml:"-"`
}

type RuleEditor struct {
	Revision  string `json:"revision"`
	Rule      Rule   `json:"rule"`
	ReadOnly  bool   `json:"readOnly"`
	LockOwner string `json:"lockOwner,omitempty"`
	LockHost  string `json:"lockHost,omitempty"`
}

type Rule struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Summary        string `json:"summary"`
	PathPattern    string `json:"pathPattern"`
	ContentPattern string `json:"contentPattern"`
	Body           string `json:"body"`
	Always         bool   `json:"always"`
	CandidateCount int    `json:"candidateCount"`
	AppliedCount   int    `json:"appliedCount"`
}

type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`
	Detail     string `json:"detail"`
	DurationMS int64  `json:"durationMs"`
}

type Usage struct {
	Uncertain    bool    `json:"uncertain"`
	InputTokens  int     `json:"inputTokens"`
	CachedTokens int     `json:"cachedTokens"`
	OutputTokens int     `json:"outputTokens"`
	CostUSD      float64 `json:"costUsd"`
	Turns        int     `json:"turns"`
}

type Attempt struct {
	ID           string              `json:"id"`
	ExecutionID  string              `json:"executionId,omitempty"`
	Number       int                 `json:"number"`
	StartedAt    string              `json:"startedAt"`
	FinishedAt   string              `json:"finishedAt"`
	Outcome      string              `json:"outcome"`
	Partial      bool                `json:"partial,omitempty"`
	Note         string              `json:"note"`
	RulesApplied []string            `json:"rulesApplied"`
	Checks       []Check             `json:"checks"`
	Usage        Usage               `json:"usage"`
	DiffPath     string              `json:"diffPath"`
	Commit       string              `json:"commit"`
	BaseCommit   string              `json:"baseCommit"`
	CommitBase   string              `json:"commitBase,omitempty"`
	InputHash    string              `json:"inputHash"`
	OutputHash   string              `json:"outputHash"`
	RepairPath   string              `json:"repairPath,omitempty"`
	Reviews      []IndependentReview `json:"reviews,omitempty"`
	Changes      []ChangeReportItem  `json:"changes,omitempty"`
}

// AdoptedChanges identifies committed output, including a safe subset whose
// unresolved items keep the file in needs_human. It is not an adoption gate.
func (a Attempt) AdoptedChanges() bool {
	return a.Commit != "" && (a.Outcome == "done" || (a.Outcome == "needs_human" && a.Partial))
}

type Task struct {
	CanDiscardChanges   bool            `json:"canDiscardChanges"`
	Discards            []DiscardChange `json:"discards,omitempty"`
	File                string          `json:"file"`
	Excluded            bool            `json:"excluded,omitempty"`
	ExcludedBeforeScope *bool           `json:"excludedBeforeScope,omitempty"`
	Rules               []string        `json:"rules"`
	Status              string          `json:"status"`
	Attempts            int             `json:"attempts"`
	RulesApplied        []string        `json:"rulesApplied"`
	Note                string          `json:"note"`
	InputHash           string          `json:"inputHash"`
	UpdatedAt           string          `json:"updatedAt"`
	History             []Attempt       `json:"history"`
	ResumeRequested     bool            `json:"resumeRequested,omitempty"`
}

// DiscardChange journals an explicit single-file return to the session baseline.
// It is separate from LLM attempts so accounting and original evidence survive.
type DiscardChange struct {
	ID             string `json:"id"`
	StartedAt      string `json:"startedAt"`
	FinishedAt     string `json:"finishedAt,omitempty"`
	State          string `json:"state"`
	BaseCommit     string `json:"baseCommit"`
	RestoreCommit  string `json:"restoreCommit"`
	Commit         string `json:"commit,omitempty"`
	InputHash      string `json:"inputHash"`
	OutputHash     string `json:"outputHash"`
	ThroughAttempt int    `json:"throughAttempt"`
}

type LogEntry struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Message string `json:"message"`
}

type WorkspaceLock struct {
	Owner    string `json:"owner"`
	Host     string `json:"host"`
	OpenedAt string `json:"openedAt"`
}

type Workspace struct {
	ID     string           `json:"id"`
	Name   string           `json:"name"`
	Root   string           `json:"root"`
	Issues []WorkspaceIssue `json:"issues,omitempty"`
}

// WorkspaceIssue carries navigation separately from the human-readable error.
// IDs are stable within a workspace; File is a source-relative task path.
type WorkspaceIssue struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Page    string `json:"page"`
	RuleID  string `json:"ruleId,omitempty"`
	File    string `json:"file,omitempty"`
	Section string `json:"section,omitempty"`
}

// LLMConnection is a named personal connection, shared by this user's workspaces.
// Credential is accepted on save but is never included in a public snapshot.
type LLMConnection struct {
	ID            string `json:"id"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	Endpoint      string `json:"endpoint"`
	Deployment    string `json:"deployment"`
	AuthMode      string `json:"authMode"`
	Credential    string `json:"credential,omitempty"`
	CredentialSet bool   `json:"credentialSet"`
	OAuthTenantID string `json:"oauthTenantId,omitempty"`
	OAuthClientID string `json:"oauthClientId,omitempty"`
	OAuthUsername string `json:"oauthUsername,omitempty"`
	OAuthSignedIn bool   `json:"oauthSignedIn"`
}

type State struct {
	ExecutionRuns           []ExecutionRun    `json:"executionRuns"`
	LLMConnections          []LLMConnection   `json:"llmConnections,omitempty"`
	SelectedLLMConnectionID string            `json:"selectedLLMConnectionId,omitempty"`
	ReadOnly                bool              `json:"readOnly"`
	WorkspaceLock           *WorkspaceLock    `json:"workspaceLock,omitempty"`
	LLMSettingsPath         string            `json:"llmSettingsPath"`
	Workspaces              []Workspace       `json:"workspaces"`
	ActiveWorkspaceID       string            `json:"activeWorkspaceId"`
	Config                  Config            `json:"config"`
	Tasks                   []Task            `json:"tasks"`
	Rules                   []Rule            `json:"rules"`
	Logs                    []LogEntry        `json:"logs"`
	Running                 bool              `json:"running"`
	Phase                   string            `json:"phase"`
	CurrentFile             string            `json:"currentFile"`
	CurrentFiles            []string          `json:"currentFiles"`
	FilePhases              map[string]string `json:"filePhases"`
	Worktree                string            `json:"worktree"`
	Branch                  string            `json:"branch"`
	ScannedCount            int               `json:"scannedCount"`
	ExcludedCount           int               `json:"excludedCount"`
	Usage                   Usage             `json:"usage"`
	LastError               string            `json:"lastError"`
}

// ExecutionRun groups all file attempts started by one press of Execute.
type ExecutionRun struct {
	ID          string `json:"id"`
	StartedAt   string `json:"startedAt"`
	FinishedAt  string `json:"finishedAt,omitempty"`
	Status      string `json:"status"`
	TargetCount int    `json:"targetCount"`
	Concurrency int    `json:"concurrency,omitempty"`
	Error       string `json:"error,omitempty"`
}

type ExecutionRunResult struct {
	Run         ExecutionRun `json:"run"`
	State       State        `json:"state"`
	TargetFiles []string     `json:"targetFiles"`
}

type FileDetail struct {
	Task       Task               `json:"task"`
	Before     string             `json:"before"`
	After      string             `json:"after"`
	Diff       string             `json:"diff"`
	Cumulative bool               `json:"cumulative"`
	Changes    []ChangeReportItem `json:"changes"`
}

type Edit struct {
	ItemIDs      []string          `json:"itemIds,omitempty"`
	OldText      string            `json:"oldText"`
	NewText      string            `json:"newText"`
	Attributions []EditAttribution `json:"attributions"`
}

// EditAttribution identifies one item's exact sub-replacement within an Edit.
// BeforeText must be a unique, nonempty anchor in OldText; insertion proposals
// include unchanged context. AfterText may be empty for deletion.
type EditAttribution struct {
	ItemID     string `json:"itemId"`
	BeforeText string `json:"beforeText"`
	AfterText  string `json:"afterText"`
}

type Proposal struct {
	Outcome      string   `json:"outcome"`
	CandidateID  string   `json:"candidateId,omitempty"`
	Edits        []Edit   `json:"edits"`
	RulesApplied []string `json:"rulesApplied"`
	Note         string   `json:"note"`
	Usage        Usage    `json:"usage"`
}
