package model

// TargetFile describes an existing regular file in the original source folder.
type TargetFile struct {
	File string `json:"file"`
	Size int64  `json:"size"`
}

type TargetFileList struct {
	WorkspaceID string       `json:"workspaceId"`
	Root        string       `json:"root"`
	Files       []TargetFile `json:"files"`
	Truncated   bool         `json:"truncated"`
	Limit       int          `json:"limit"`
}

type TargetFileContent struct {
	WorkspaceID       string `json:"workspaceId"`
	Root              string `json:"root"`
	File              string `json:"file"`
	Size              int64  `json:"size"`
	Content           string `json:"content"`
	UnavailableReason string `json:"unavailableReason"`
}
