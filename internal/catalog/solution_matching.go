package catalog

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"onebyone/internal/model"
	"onebyone/internal/sourceencoding"
)

// MatchSolutionRules freezes the same source/diagnostic conditions used by Scan
// over an explicitly authorized solution file set. Call once against the initial
// scratch snapshot and initial build diagnostics, then retain both results for
// every repair/build iteration even when a successful edit removes a match.
// It does not import external diagnostics or enumerate additional target files.
func (c *Catalog) MatchSolutionRules(ctx context.Context, root string, files []string, buildDiagnostics []model.SourceDiagnostic) ([]model.Rule, map[string][]string, error) {
	root, err := checkedAbsolute(root)
	if err != nil {
		return nil, nil, err
	}
	authorized := make(map[string]bool, len(files))
	all := append([]string(nil), files...)
	for _, file := range all {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if authorized[file] || strings.Contains(file, "\\") {
			return nil, nil, fmt.Errorf("ソリューションの対象ファイル一覧が不正です: %s", file)
		}
		path, err := PathWithin(root, file)
		if err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, fmt.Errorf("read solution source %s: %w", file, err)
		}
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("solution source is not a regular file: %s", file)
		}
		authorized[file] = true
	}
	sort.Strings(all)
	diagnosticsByFile := map[string][]model.SourceDiagnostic{}
	for _, diagnostic := range buildDiagnostics {
		if authorized[diagnostic.File] {
			diagnosticsByFile[diagnostic.File] = append(diagnosticsByFile[diagnostic.File], diagnostic)
		}
	}
	selected, ruleFiles := []model.Rule{}, map[string][]string{}
	pathCache := map[string]map[string]bool{}
	encodingCache := map[string]sourceencoding.Encoding{}
	for _, rule := range c.Rules {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if c.excludedRules[rule.ID] {
			continue
		}
		candidates := append([]string(nil), all...)
		if rule.PathPattern != "" {
			matches, known := pathCache[rule.PathPattern]
			if !known {
				output, err := c.run(ctx, root, nil, "--files", "--null", "--hidden", "--no-ignore", "--no-config", "--iglob", rule.PathPattern)
				if err != nil {
					return nil, nil, ruleError(rule.ID, err)
				}
				matches = map[string]bool{}
				for _, file := range nulPaths(output) {
					matches[file] = true
				}
				pathCache[rule.PathPattern] = matches
			}
			filtered := []string{}
			for _, file := range candidates {
				if matches[file] {
					filtered = append(filtered, file)
				}
			}
			candidates = filtered
		}
		if rule.ContentPattern != "" {
			matches, err := c.matchFilesEncoded(ctx, root, candidates, []string{rule.ContentPattern}, encodingCache)
			if err != nil {
				return nil, nil, ruleError(rule.ID, err)
			}
			filtered := []string{}
			for _, file := range candidates {
				if matches[file] {
					filtered = append(filtered, file)
				}
			}
			candidates = filtered
		}
		if rule.DiagnosticPattern != "" {
			matcher := c.diagnosticMatchers[rule.ID]
			if matcher == nil {
				return nil, nil, ruleError(rule.ID, fmt.Errorf("diagnostic_pattern の照合が初期化されていません"))
			}
			filtered := []string{}
			for _, file := range candidates {
				for _, diagnostic := range diagnosticsByFile[file] {
					if matcher.MatchString(diagnostic.Code + " " + diagnostic.Message) {
						filtered = append(filtered, file)
						break
					}
				}
			}
			candidates = filtered
		}
		if len(candidates) == 0 {
			continue
		}
		rule.CandidateCount = len(candidates)
		selected = append(selected, rule)
		ruleFiles[rule.ID] = candidates
	}
	return selected, ruleFiles, nil
}
