package model

import (
	"fmt"
	"path/filepath"
	"strings"
)

// SolutionSettings uses VS2022 MSBuild; zero MaxIterations means until stopped.
type SolutionSettings struct {
	// A nil list uses the legacy Path, or discovers all solutions on first use.
	// An explicitly empty list means that no solutions are selected.
	Paths               []string `json:"paths"`
	Path                string   `json:"path"`
	MSBuildPath         string   `json:"msbuildPath"`
	Configuration       string   `json:"configuration"`
	Platform            string   `json:"platform"`
	MaxIterations       int      `json:"maxIterations"`
	BuildTimeoutSeconds int      `json:"buildTimeoutSeconds"`
}

func DefaultSolutionSettings() SolutionSettings {
	return SolutionSettings{Configuration: "Debug", Platform: "Any CPU", MaxIterations: 20, BuildTimeoutSeconds: 300}
}

func (c Config) SolutionMode() bool { return c.Mode == "solution" }

func (s SolutionSettings) SelectedPaths() []string {
	if s.Paths != nil {
		return append([]string{}, s.Paths...)
	}
	if s.Path != "" {
		return []string{s.Path}
	}
	return nil
}

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
	normalizePath := func(value string) (string, error) {
		value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
		if value == "" || !filepath.IsLocal(filepath.FromSlash(value)) || strings.ContainsAny(value, ":\x00\r\n") || strings.HasPrefix(value, "-") || !strings.EqualFold(filepath.Ext(value), ".sln") {
			return "", fmt.Errorf("ソリューションは対象フォルダ内の .sln 相対パスで指定してください")
		}
		for _, part := range strings.Split(value, "/") {
			if part == ".." {
				return "", fmt.Errorf("ソリューションパスに親フォルダは指定できません")
			}
		}
		return filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))), nil
	}
	if strings.TrimSpace(s.Path) != "" {
		var err error
		s.Path, err = normalizePath(s.Path)
		if err != nil {
			return c, err
		}
	} else {
		s.Path = ""
	}
	if s.Paths != nil {
		paths, seen := []string{}, map[string]bool{}
		for _, value := range s.Paths {
			path, err := normalizePath(value)
			if err != nil {
				return c, err
			}
			if !seen[strings.ToLower(path)] {
				paths = append(paths, path)
				seen[strings.ToLower(path)] = true
			}
		}
		s.Paths = paths
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
	Path           string              `json:"path,omitempty"`
	ExecutionID    string              `json:"executionId,omitempty"`
	SourceFiles    []string            `json:"sourceFiles,omitempty"`
	RuleFiles      map[string][]string `json:"ruleFiles,omitempty"`
	Reviews        []SolutionReview    `json:"reviews,omitempty"`
	RequestPending bool                `json:"requestPending,omitempty"`
	ID             string              `json:"id"`
	Status         string              `json:"status"`
	StartedAt      string              `json:"startedAt"`
	FinishedAt     string              `json:"finishedAt,omitempty"`
	Iteration      int                 `json:"iteration"`
	Note           string              `json:"note"`
	Builds         []SolutionBuild     `json:"builds"`
	Files          []string            `json:"files"`
	Commit         string              `json:"commit,omitempty"`
	Usage          Usage               `json:"usage"`
}

type SolutionBatch struct {
	ID           string        `json:"id"`
	Status       string        `json:"status"`
	Paths        []string      `json:"paths"`
	CurrentIndex int           `json:"currentIndex"`
	Runs         []SolutionRun `json:"runs"`
	StartedAt    string        `json:"startedAt"`
	FinishedAt   string        `json:"finishedAt,omitempty"`
	Note         string        `json:"note,omitempty"`
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
