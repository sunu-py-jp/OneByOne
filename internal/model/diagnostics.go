package model

// SourceDiagnostic is imported evidence, not a claim about the current build.
// RuleIDs is assigned by target extraction and is not an importable property.
type SourceDiagnostic struct {
	File    string   `json:"file"`
	Message string   `json:"message"`
	Line    int      `json:"line,omitempty"`
	Code    string   `json:"code,omitempty"`
	RuleIDs []string `json:"ruleIds,omitempty"`
}

type DiagnosticSummary struct {
	Name      string `json:"name"`
	Count     int    `json:"count"`
	FileCount int    `json:"fileCount"`
}
