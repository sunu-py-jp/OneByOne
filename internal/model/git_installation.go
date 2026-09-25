package model

// GitInstallation describes whether the Git executable visible to this process
// is ready to use. It does not inspect or modify a repository.
type GitInstallation struct {
	Available bool   `json:"available"`
	Status    string `json:"status"`
	Platform  string `json:"platform"`
	Path      string `json:"path"`
	Version   string `json:"version"`
	Message   string `json:"message"`
}
