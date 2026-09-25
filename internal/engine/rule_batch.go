package engine

import (
	"fmt"
	"path/filepath"
	"strings"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
	"onebyone/internal/store"
)

func ruleBatchError(id string, err error) error {
	return &catalog.DiagnosticError{RuleID: id, Err: fmt.Errorf("rule %s: %w", id, err)}
}

// SaveRules publishes all drafts in one package version. Existing definitions
// require the revision that was opened; absence of a revision only creates a
// new ID. The workspace lease serializes the disk snapshot and commit.
func (s *Service) SaveRules(edits []model.RuleEdit) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	if len(edits) == 0 {
		return s.Snapshot(), nil
	}
	if len(edits)+1 > rulepack.MaxFiles {
		return s.Snapshot(), &catalog.DiagnosticError{Err: fmt.Errorf("保存するルール数がパッケージのファイル数上限を超えています")}
	}
	s.mu.Lock()
	workspaceID, current := s.state.ActiveWorkspaceID, s.state.Config
	s.mu.Unlock()
	if workspaceID == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選択してください")
	}
	w, saved, err := s.readWorkspaceSetting(workspaceID)
	if err != nil {
		return s.Snapshot(), err
	}
	if !sameRoot(saved.Root, current.Root) || saved.QueuePath != current.QueuePath {
		return s.Snapshot(), fmt.Errorf("ワークスペース設定が更新されています。最新の状態を取得してから保存してください")
	}
	// Keep live settings and personal connection resolution, including a cleared
	// selection after another app deletes that connection. Read the latest rule
	// package paths from disk for optimistic definition checks.
	cfg := current
	cfg.RulesPath, cfg.LegacyPath, cfg.RulePackageName = saved.RulesPath, saved.LegacyPath, saved.RulePackageName
	pkg := &rulepack.Package{Settings: rulepack.FromConfig(cfg), Files: map[string][]byte{}}
	if cfg.RulesPath != "" {
		pkg, err = rulepack.Snapshot(cfg.RulesPath, cfg.LegacyPath, rulepack.FromConfig(cfg))
		if err != nil {
			return s.Snapshot(), err
		}
	}
	existing := map[string]string{}
	for path := range pkg.Files {
		parts := strings.Split(path, "/")
		if len(parts) == 3 && parts[0] == "rules" && parts[2] == "rule.json" {
			existing[strings.ToLower(parts[1])] = parts[1]
		}
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(edits))
	for _, edit := range edits {
		if err := validateRuleEdit(edit); err != nil {
			return s.Snapshot(), ruleBatchError(edit.ID, err)
		}
		key := strings.ToLower(edit.ID)
		if seen[key] {
			return s.Snapshot(), ruleBatchError(edit.ID, fmt.Errorf("保存するルールIDが重複しています"))
		}
		seen[key] = true
		id, exists := existing[key]
		if exists {
			stored, err := ruleFromPackage(pkg, id)
			if err != nil {
				return s.Snapshot(), ruleBatchError(id, err)
			}
			if edit.ExpectedRevision == "" || edit.ExpectedRevision != ruleRevision(stored) {
				return s.Snapshot(), ruleBatchError(id, fmt.Errorf("このルールは他の編集で更新されています。最新の内容を開き直し、変更を確認してから保存してください"))
			}
		} else {
			if edit.ExpectedRevision != "" {
				return s.Snapshot(), ruleBatchError(edit.ID, fmt.Errorf("編集していたルールが見つかりません。最新の内容を開き直してください"))
			}
			id = edit.ID
		}
		data, err := ruleformat.Encode(editDefinition(edit))
		if err != nil {
			return s.Snapshot(), ruleBatchError(id, err)
		}
		pkg.Files["rules/"+id+"/rule.json"] = data
		ids = append(ids, id)
	}
	var leases []*store.WorkspaceLease
	defer func() {
		for _, lease := range leases {
			_ = lease.Release()
		}
	}()
	for _, id := range ids {
		if s.ruleLease != nil && s.ruleLeaseWorkspaceID == workspaceID && strings.EqualFold(s.ruleLeaseID, id) {
			continue
		}
		path, err := s.ruleLockPath(workspaceID, id)
		if err != nil {
			return s.Snapshot(), ruleBatchError(id, err)
		}
		if err := ensureSharedOutputDirectory(filepath.Dir(path), 0700); err != nil {
			return s.Snapshot(), ruleBatchError(id, err)
		}
		lease, owner, err := store.AcquireWorkspace(path, currentWorkspaceOwner())
		if err != nil {
			return s.Snapshot(), ruleBatchError(id, err)
		}
		if owner != nil {
			return s.Snapshot(), ruleBatchError(id, fmt.Errorf("他のユーザーがこのルールを編集中です（%s / %s）", owner.Owner, owner.Host))
		}
		leases = append(leases, lease)
	}
	if cfg.RulePackageName == "" {
		cfg.RulePackageName = "rules.oborules"
	}
	// Keep an already-open editor's lease on both success and failure. Leases
	// acquired just for this batch are released after validation and commit.
	return s.installRulePackage(w, cfg, pkg)
}
