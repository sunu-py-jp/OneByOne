package engine

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"onebyone/internal/model"
	"onebyone/internal/rulepack"
	"onebyone/internal/solutionrules"
)

func (s *Service) ImportSolutionSkills(path, mode string) (model.State, error) {
	s.mu.Lock()
	solutionMode := s.state.Config.SolutionMode()
	s.mu.Unlock()
	if !solutionMode {
		return s.Snapshot(), fmt.Errorf("Skills はソリューションモードで取り込んでください")
	}
	return s.ImportRulePackage(path, mode)
}

func (s *Service) ExportSolutionSkills(directory string) error {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return err
	}
	s.mu.Lock()
	cfg, id := s.state.Config, s.state.ActiveWorkspaceID
	s.mu.Unlock()
	if !cfg.SolutionMode() {
		return fmt.Errorf("Skills 書き出しはソリューションモードで使用してください")
	}
	if cfg.RulesPath == "" {
		return fmt.Errorf("ルールを追加してください")
	}
	destination, err := filepath.Abs(directory)
	if err != nil {
		return err
	}
	settings, err := s.workspacePath(id, "setting.json")
	if err != nil {
		return err
	}
	if isAtOrWithin(filepath.Dir(settings), destination) {
		return fmt.Errorf("書き出し先はワークスペース設定の外に指定してください")
	}
	pkg, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		return err
	}
	rules, err := rulepack.SolutionRules(pkg)
	if err != nil {
		return err
	}
	return solutionrules.ExportSkills(canonicalAlias(destination), rules)
}

func mergedSolutionRuleIDs(existing, incoming []string) map[string]string {
	used, occupied := map[string]bool{}, map[string]bool{}
	for _, id := range existing {
		used[id], occupied[id] = true, true
	}
	for _, id := range incoming {
		used[id] = true
	}
	result := map[string]string{}
	for _, id := range incoming {
		if !occupied[id] {
			result[id] = id
			continue
		}
		for n := 2; ; n++ {
			suffix := "-" + strconv.Itoa(n)
			base := id
			if len(base)+len(suffix) > 64 {
				base = strings.TrimRight(base[:64-len(suffix)], "-")
			}
			candidate := base + suffix
			if !used[candidate] {
				result[id], used[candidate] = candidate, true
				break
			}
		}
	}
	return result
}
