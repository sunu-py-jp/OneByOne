package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

// This contains explicit work state, never raw model conversations or credentials.
// PriorUsage separates this attempt's usage from the file's cumulative budget.
type repairCheckpoint struct {
	Version      int               `json:"version"`
	AttemptID    string            `json:"attemptId"`
	File         string            `json:"file"`
	BaseCommit   string            `json:"baseCommit"`
	InputHash    string            `json:"inputHash"`
	RuleHash     string            `json:"ruleHash"`
	SettingsHash string            `json:"settingsHash"`
	RuleTitles   map[string]string `json:"ruleTitles,omitempty"`
	PriorUsage   model.Usage       `json:"priorUsage"`
	State        model.RepairState `json:"state"`
}

func repairSettingsHash(cfg model.Config) string {
	// A turn-limit adjustment authorizes more/fewer requests, but does not change
	// the source, rules, model or validations of the saved plan and candidate.
	cfg.MaxTurns = 0
	return fullRepairSettingsHash(cfg)
}

func fullRepairSettingsHash(cfg model.Config) string {
	// Hash execution semantics without storing a credential or its fingerprint.
	cfg.Credential = ""
	cfg.CredentialSet = false
	b, _ := json.Marshal(cfg)
	return digest(b)
}

func repairSettingsMatch(stored string, cfg model.Config) bool {
	if stored == repairSettingsHash(cfg) {
		return true
	}
	// Existing journals hashed MaxTurns with execution settings. Only this
	// bounded field may differ; changing any execution setting still invalidates
	// the plan. New journals always store the turn-independent fingerprint.
	for turns := 1; turns <= 32; turns++ {
		cfg.MaxTurns = turns
		if stored == fullRepairSettingsHash(cfg) {
			return true
		}
	}
	return false
}

func repairPath(cfg model.Config, id string) (string, error) {
	if id == "" || filepath.Base(id) != id || strings.ContainsAny(id, "/\\:*?\"<>|\x00\r\n") || id == "." || id == ".." {
		return "", fmt.Errorf("修復状態の試行IDが不正です")
	}
	return outputAbsolute(filepath.Join(cfg.QueuePath+".artifacts", id+".state.json"))
}

func loadRepairCheckpoint(cfg model.Config, h model.Attempt) (*repairCheckpoint, error) {
	if h.RepairPath == "" {
		return nil, nil
	}
	path, err := repairPath(cfg, h.ID)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(path) != filepath.Clean(h.RepairPath) {
		return nil, fmt.Errorf("修復状態の保存先が一致しません")
	}
	if _, err = catalog.PathWithin(filepath.Dir(path), filepath.Base(path)); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("修復状態を読み込めません: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > 4<<20 {
		return nil, fmt.Errorf("修復状態ファイルの形式またはサイズが不正です")
	}
	b, err := store.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c repairCheckpoint
	if err = json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	if c.Version != 1 || c.State.Version != 1 || c.AttemptID != h.ID || c.State.ElapsedMS < 0 || c.State.ToolCalls < 0 || c.State.ValidationCount < 0 || c.State.Usage.Turns < 0 {
		return nil, fmt.Errorf("修復状態のバージョン・試行・使用量が不正です")
	}
	return &c, nil
}

func loadLatestRepairCheckpoint(cfg model.Config, history []model.Attempt) (*repairCheckpoint, error) {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].RepairPath != "" {
			return loadRepairCheckpoint(cfg, history[i])
		}
	}
	return nil, nil
}

func retainKnownUsage(state *model.RepairState, known model.Usage) {
	state.Usage.InputTokens = max(state.Usage.InputTokens, known.InputTokens)
	state.Usage.CachedTokens = max(state.Usage.CachedTokens, known.CachedTokens)
	state.Usage.OutputTokens = max(state.Usage.OutputTokens, known.OutputTokens)
	state.Usage.Turns = max(state.Usage.Turns, known.Turns)
	state.Usage.CostUSD = max(state.Usage.CostUSD, known.CostUSD)
	state.Usage.Uncertain = state.Usage.Uncertain || known.Uncertain
}

func saveRepairCheckpoint(cfg model.Config, c *repairCheckpoint) (string, error) {
	path, err := repairPath(cfg, c.AttemptID)
	if err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return "", err
	}
	err = writeSharedArtifact(cfg, path, b, filepath.Join(cfg.Root, filepath.FromSlash(c.File)))
	return path, err
}

func sumUsage(history []model.Attempt) model.Usage {
	u := model.Usage{}
	for _, h := range history {
		u.InputTokens += h.Usage.InputTokens
		u.CachedTokens += h.Usage.CachedTokens
		u.OutputTokens += h.Usage.OutputTokens
		u.Turns += h.Usage.Turns
		u.CostUSD += h.Usage.CostUSD
		u.Uncertain = u.Uncertain || h.Usage.Uncertain
	}
	return u
}

func usageSince(u, prior model.Usage) model.Usage {
	return model.Usage{InputTokens: max(0, u.InputTokens-prior.InputTokens), CachedTokens: max(0, u.CachedTokens-prior.CachedTokens), OutputTokens: max(0, u.OutputTokens-prior.OutputTokens), Turns: max(0, u.Turns-prior.Turns), CostUSD: max(0, u.CostUSD-prior.CostUSD), Uncertain: u.Uncertain}
}

func unfinishedRepair(t model.Task) bool {
	history := repairHistory(t)
	if len(history) == 0 {
		return false
	}
	h := history[len(history)-1]
	return h.RepairPath != "" && h.Outcome != "done" && h.Outcome != "skipped"
}

func resetRepairPlan(c *repairCheckpoint, cfg model.Config, cat *catalog.Catalog, file, head, inputHash string) bool {
	settings := repairSettingsHash(cfg)
	changed := c.File != file || c.BaseCommit != head || c.InputHash != inputHash || c.RuleHash != cat.Hash || !repairSettingsMatch(c.SettingsHash, cfg)
	if changed {
		c.State.Plan = model.RepairPlan{}
		c.State.LastCandidate = nil
		c.State.ReadRuleIDs = nil
	}
	if changed || c.RuleTitles == nil {
		c.RuleTitles = changeReportTitles(cat.Rules)
	}
	c.File, c.BaseCommit, c.InputHash, c.RuleHash, c.SettingsHash = file, head, inputHash, cat.Hash, settings
	return changed
}

func repairProvenanceMatches(c *repairCheckpoint, cfg model.Config, cat *catalog.Catalog, file, head, inputHash string) bool {
	return c != nil && c.File == file && c.BaseCommit == head && c.InputHash == inputHash && c.RuleHash == cat.Hash && repairSettingsMatch(c.SettingsHash, cfg)
}
