package engine

import (
	"fmt"
	"sort"

	"onebyone/internal/model"
)

// freezeSolutionRuleScope is called only after a child's initial build. Shared
// files retain earlier accepted requirements even when those repairs removed
// the original source/diagnostic condition. Scope never widens to other files.
func (s *Service) freezeSolutionRuleScope(cfg model.Config, catalogRules, selected []model.Rule, ruleFiles map[string][]string, files []string) ([]model.Rule, map[string][]string, error) {
	s.mu.Lock()
	batch := copySolutionBatch(s.state.SolutionBatch)
	var run model.SolutionRun
	if s.state.SolutionRun != nil {
		run = *s.state.SolutionRun
	}
	s.mu.Unlock()
	rules, scopes, err := mergeSolutionRuleScopes(batch, run, cfg.ExcludedRuleIDs, catalogRules, selected, ruleFiles, files)
	if err != nil {
		return nil, nil, err
	}
	if err := s.solutionProgress(func(r *model.SolutionRun) { r.RuleFiles = copySolutionRuleFiles(scopes) }); err != nil {
		return nil, nil, err
	}
	return rules, scopes, nil
}

func copySolutionRuleFiles(scopes map[string][]string) map[string][]string {
	out := make(map[string][]string, len(scopes))
	for id, files := range scopes {
		out[id] = append([]string(nil), files...)
	}
	return out
}

func mergeSolutionRuleScopes(batch *model.SolutionBatch, current model.SolutionRun, excluded []string, catalogRules, selected []model.Rule, ruleFiles map[string][]string, files []string) ([]model.Rule, map[string][]string, error) {
	authorized, disabled := map[string]bool{}, map[string]bool{}
	for _, file := range files {
		authorized[file] = true
	}
	for _, id := range excluded {
		disabled[id] = true
	}
	definitions := map[string]model.Rule{}
	for _, rule := range catalogRules {
		if !disabled[rule.ID] {
			definitions[rule.ID] = rule
		}
	}
	merged := map[string]map[string]bool{}
	add := func(id string, targets []string) error {
		if disabled[id] {
			return nil
		}
		for _, file := range targets {
			if !authorized[file] {
				continue
			}
			if _, exists := definitions[id]; !exists {
				return fmt.Errorf("先行ソリューションの適用ルール %s が現在のルール一覧にありません", id)
			}
			if merged[id] == nil {
				merged[id] = map[string]bool{}
			}
			merged[id][file] = true
		}
		return nil
	}
	for _, rule := range selected {
		if err := add(rule.ID, ruleFiles[rule.ID]); err != nil {
			return nil, nil, err
		}
	}
	// An old completed batch retained for the results UI is not an input to a
	// new execution. Require the active child slot and parent identity as well.
	if batch != nil && batch.Status == "running" && batch.ID == solutionExecutionID(&current) && batch.CurrentIndex >= 0 && batch.CurrentIndex < len(batch.Runs) && batch.Runs[batch.CurrentIndex].ID == current.ID {
		for i := 0; i < batch.CurrentIndex; i++ {
			previous := batch.Runs[i]
			if previous.Status != "completed" || solutionExecutionID(&previous) != batch.ID {
				continue
			}
			for id, targets := range previous.RuleFiles {
				if err := add(id, targets); err != nil {
					return nil, nil, err
				}
			}
		}
	}
	rules, scopes := []model.Rule{}, map[string][]string{}
	for _, definition := range catalogRules {
		matches := merged[definition.ID]
		if len(matches) == 0 {
			continue
		}
		targets := make([]string, 0, len(matches))
		for file := range matches {
			targets = append(targets, file)
		}
		sort.Strings(targets)
		scopes[definition.ID] = targets
		definition.CandidateCount = len(targets)
		rules = append(rules, definition)
	}
	return rules, scopes, nil
}
