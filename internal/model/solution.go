package model

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SolutionSettings uses VS2022 MSBuild; zero MaxIterations means until stopped.
type SolutionSettings struct {
	Path                string `json:"path"`
	MSBuildPath         string `json:"msbuildPath"`
	Configuration       string `json:"configuration"`
	Platform            string `json:"platform"`
	MaxIterations       int    `json:"maxIterations"`
	BuildTimeoutSeconds int    `json:"buildTimeoutSeconds"`
}

func DefaultSolutionSettings() SolutionSettings {
	return SolutionSettings{Configuration: "Debug", Platform: "Any CPU", MaxIterations: 20, BuildTimeoutSeconds: 300}
}

func (c Config) SolutionMode() bool { return c.Mode == "solution" }

func NormalizeSolutionConfig(c Config) (Config, error) {
	if c.Mode != "" && c.Mode != "files" && c.Mode != "solution" {
		return c, fmt.Errorf("ワークスペースの処理モードが不正です")
	}
	if c.Solution == nil {
		if !c.SolutionMode() {
			return c, nil
		}
		settings := DefaultSolutionSettings()
		c.Solution = &settings
	}
	s := *c.Solution
	s.Path = strings.TrimSpace(strings.ReplaceAll(s.Path, "\\", "/"))
	if s.Path != "" && (!filepath.IsLocal(filepath.FromSlash(s.Path)) || strings.ContainsAny(s.Path, ":\x00\r\n") || strings.HasPrefix(s.Path, "-") || !strings.EqualFold(filepath.Ext(s.Path), ".sln")) {
		return c, fmt.Errorf("ソリューションは対象フォルダ内の .sln 相対パスで指定してください")
	}
	for _, p := range strings.Split(s.Path, "/") {
		if p == ".." {
			return c, fmt.Errorf("ソリューションパスに親フォルダは指定できません")
		}
	}
	if s.Configuration == "" {
		s.Configuration = "Debug"
	}
	if s.Platform == "" {
		s.Platform = "Any CPU"
	}
	for _, value := range []string{s.Configuration, s.Platform} {
		if strings.ContainsAny(value, ";\r\n\x00\"%") {
			return c, fmt.Errorf("ビルド構成・プラットフォームに無効な文字があります")
		}
	}
	if strings.ContainsAny(s.MSBuildPath, "\x00\r\n\"") {
		return c, fmt.Errorf("MSBuild のパスが不正です")
	}
	if s.MaxIterations < 0 {
		return c, fmt.Errorf("修正回数の上限は0以上です（0は停止するまで）")
	}
	if s.BuildTimeoutSeconds == 0 {
		s.BuildTimeoutSeconds = 300
	}
	if s.BuildTimeoutSeconds < 1 || s.BuildTimeoutSeconds > 86400 {
		return c, fmt.Errorf("ビルドのタイムアウトは1〜86400秒で指定してください")
	}
	c.Solution = &s
	return c, nil
}

type SolutionBuild struct {
	Iteration   int                `json:"iteration"`
	StartedAt   string             `json:"startedAt"`
	FinishedAt  string             `json:"finishedAt"`
	Success     bool               `json:"success"`
	ExitCode    int                `json:"exitCode"`
	Output      string             `json:"output"`
	Diagnostics []SourceDiagnostic `json:"diagnostics"`
}

type SolutionRun struct {
	Reviews        []SolutionReview `json:"reviews,omitempty"`
	RequestPending bool             `json:"requestPending,omitempty"`
	ID             string           `json:"id"`
	Status         string           `json:"status"`
	StartedAt      string           `json:"startedAt"`
	FinishedAt     string           `json:"finishedAt,omitempty"`
	Iteration      int              `json:"iteration"`
	Note           string           `json:"note"`
	Builds         []SolutionBuild  `json:"builds"`
	Files          []string         `json:"files"`
	Commit         string           `json:"commit,omitempty"`
	Usage          Usage            `json:"usage"`
}

// SolutionReview binds an independent decision to every original/candidate
// snapshot hash, retaining rejected findings without adopting their patches.
type SolutionReview struct {
	Iteration       int                `json:"iteration"`
	Verdict         string             `json:"verdict"`
	Summary         string             `json:"summary"`
	Assessments     []ReviewAssessment `json:"assessments"`
	OriginalHashes  map[string]string  `json:"originalHashes"`
	CandidateHashes map[string]string  `json:"candidateHashes"`
	Usage           Usage              `json:"usage"`
}
