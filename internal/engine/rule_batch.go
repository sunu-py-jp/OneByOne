package engine

import (
	"fmt"
	"path/filepath"
	"strconv"
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
	return s.saveRules(edits)
}

func (s *Service) saveRules(edits []model.RuleEdit) (model.State, error) {
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
	cfg.RulesPath = saved.RulesPath
	pkg := &rulepack.Package{Rules: []rulepack.Entry{}}
	if cfg.RulesPath != "" {
		pkg, err = rulepack.Snapshot(cfg.RulesPath)
		if err != nil {
			return s.Snapshot(), err
		}
	}
	existing := map[string]string{}
	positions := map[string]int{}
	taken := map[string]bool{}
	for i, entry := range pkg.Rules {
		existing[strings.ToLower(entry.ID)] = entry.ID
		positions[entry.ID] = i
		taken[strings.ToLower(entry.ID)] = true
	}
	for _, edit := range edits {
		taken[strings.ToLower(strings.TrimSpace(edit.ID))] = true
	}
	seen := map[string]bool{}
	ids := make([]string, 0, len(edits))
	for _, edit := range edits {
		edit.ID = strings.TrimSpace(edit.ID)
		if edit.ID == "" && edit.ExpectedRevision == "" {
			edit.ID = nextRuleID(taken)
			taken[strings.ToLower(edit.ID)] = true
		}
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
			if edit.ExpectedRevision == "" {
				return s.Snapshot(), ruleBatchError(edit.ID, fmt.Errorf("同じIDのルールが既にあります: %s", id))
			}
			stored, err := ruleFromPackage(pkg, id)
			if err != nil {
				return s.Snapshot(), ruleBatchError(id, err)
			}
			if edit.ExpectedRevision != ruleRevision(stored) {
				return s.Snapshot(), ruleBatchError(id, fmt.Errorf("このルールは他の編集で更新されています。最新の内容を開き直し、変更を確認してから保存してください"))
			}
		} else {
			if edit.ExpectedRevision != "" {
				return s.Snapshot(), ruleBatchError(edit.ID, fmt.Errorf("編集していたルールが見つかりません。最新の内容を開き直してください"))
			}
			id = edit.ID
		}
		// The stored spelling stays canonical; an ID is fixed after creation.
		definition := editDefinition(edit)
		definition.ID = id
		data, err := ruleformat.Encode(definition)
		if err != nil {
			return s.Snapshot(), ruleBatchError(id, err)
		}
		entry := rulepack.Entry{ID: id, Markdown: string(data)}
		if index, ok := positions[id]; ok {
			pkg.Rules[index] = entry
		} else {
			positions[id] = len(pkg.Rules)
			pkg.Rules = append(pkg.Rules, entry)
		}
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
	// Keep an already-open editor's lease on both success and failure. Leases
	// acquired just for this batch are released after validation and commit.
	return s.installRulePackage(w, cfg, pkg)
}

// nextRuleID continues the numeric sequence used by the bundled rules.
func nextRuleID(taken map[string]bool) string {
	next := 1
	for id := range taken {
		if n, err := strconv.Atoi(id); err == nil && n >= next {
			next = n + 1
		}
	}
	for taken[strconv.Itoa(next)] {
		next++
	}
	return strconv.Itoa(next)
}
