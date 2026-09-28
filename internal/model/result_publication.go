package model

// ResultPublicationPreview is a snapshot of the adopted cumulative result.
// Diffs are loaded separately for the selected file to keep large queues small.
type ResultPublicationPreview struct {
	WorkspaceID          string                  `json:"workspaceId"`
	Revision             string                  `json:"revision"`
	BaseCommit           string                  `json:"baseCommit"`
	SourceCommit         string                  `json:"sourceCommit"`
	SuggestedBranch      string                  `json:"suggestedBranch"`
	Message              string                  `json:"message"`
	MessageFileThreshold int                     `json:"messageFileThreshold"`
	Files                []ResultPublicationFile `json:"files"`
	ReportFiles          []ResultPublicationFile `json:"reportFiles"`
	Publications         []ResultPublication     `json:"publications"`
}

type ResultPublicationFile struct {
	File         string   `json:"file"`
	LinkPath     string   `json:"linkPath"`
	RulesApplied []string `json:"rulesApplied"`
	Summary      string   `json:"summary"`
	Diff         string   `json:"diff"`
}

type PublishResultsRequest struct {
	WorkspaceID   string `json:"workspaceId"`
	Revision      string `json:"revision"`
	Branch        string `json:"branch"`
	Title         string `json:"title"`
	Message       string `json:"message"`
	MessageAsFile bool   `json:"messageAsFile"`
}

type ResultPublication struct {
	Branch       string `json:"branch"`
	Commit       string `json:"commit"`
	BaseCommit   string `json:"baseCommit"`
	SourceCommit string `json:"sourceCommit"`
	CreatedAt    string `json:"createdAt"`
	FileCount    int    `json:"fileCount"`
	Title        string `json:"title"`
	Message      string `json:"message"`
	ReportPath   string `json:"reportPath,omitempty"`
	ReportBlob   string `json:"reportBlob,omitempty"`
}
