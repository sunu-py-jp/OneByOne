package engine

import (
	"onebyone/internal/model"
	"onebyone/internal/rulepack"
)

func normalizedWorkspaceRuleSelection(cfg model.Config) []string {
	if cfg.RulesPath == "" {
		return nil
	}
	pkg, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		// Keep exclusions while damaged rules are repaired; saving unrelated
		// settings must neither fail nor silently select every rule.
		return model.NormalizeExcludedRuleIDs(cfg.ExcludedRuleIDs, nil)
	}
	ids := make([]string, 0, len(pkg.Rules))
	for _, entry := range pkg.Rules {
		ids = append(ids, entry.ID)
	}
	return model.NormalizeExcludedRuleIDs(cfg.ExcludedRuleIDs, ids)
}
