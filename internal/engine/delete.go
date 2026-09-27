package engine

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"onebyone/internal/model"
	"onebyone/internal/privateconfig"
	"onebyone/internal/ruleformat"
	"onebyone/internal/rulepack"
)

func (s *Service) resetAfterWorkspaceDelete(id string, remaining []workspaceRecord, registry privateconfig.Registry) {
	s.releaseRuleLease()
	s.adoptWorkspaceLease(model.Workspace{}, nil)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workspaces = remaining
	delete(s.workspaceIssues, id)
	s.state = emptyState(DefaultConfig())
	s.meta, s.cat, s.cancel, s.done = manifest{Version: 1}, nil, nil, nil
	s.publishConnectionsLocked(registry)
	s.publishWorkspacesLocked()
}

// DeleteWorkspace removes only the active app-owned workspace from the list.
// Moving its directory retains all managed results and edited worktrees; source
// directories and externally configured queues are never removed or rewritten.
func (s *Service) DeleteWorkspace(id string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	active := s.state.ActiveWorkspaceID
	s.mu.Unlock()
	if id == "" || id != active {
		return s.Snapshot(), fmt.Errorf("削除するワークスペースを開き直してください")
	}
	setting, err := s.workspacePath(id, "setting.json")
	if err != nil {
		return s.Snapshot(), err
	}
	items, err := s.listLocalWorkspaces()
	if err != nil {
		return s.Snapshot(), err
	}
	remaining := make([]workspaceRecord, 0, len(items))
	workspaceDir := filepath.Dir(setting)
	found := false
	for _, item := range items {
		if isAtOrWithin(workspaceDir, item.Root) || isAtOrWithin(item.Root, workspaceDir) {
			return s.Snapshot(), fmt.Errorf("ワークスペース保存先と対象フォルダが重なっているため削除できません")
		}
		if item.ID != id {
			remaining = append(remaining, item)
		} else {
			found = true
		}
	}
	if !found {
		return s.Snapshot(), fmt.Errorf("削除するワークスペースの設定が見つかりません")
	}
	registry, err := s.personal.LoadConnections()
	if err != nil {
		return s.Snapshot(), err
	}
	base := filepath.Dir(filepath.Dir(workspaceDir))
	root, err := os.OpenRoot(base)
	if err != nil {
		return s.Snapshot(), err
	}
	defer root.Close()
	if err = root.Mkdir("workspace-trash", 0700); err != nil && !os.IsExist(err) {
		return s.Snapshot(), err
	}
	info, err := root.Lstat("workspace-trash")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return s.Snapshot(), fmt.Errorf("ワークスペースの退避先が通常のフォルダではありません")
	}
	from, destination := "workspaces/"+id, "workspace-trash/"+id+"-"+uid()
	// Hold the workspace and rule locks through the atomic move. Releasing them
	// first would allow another process to edit the directory being removed.
	// Their stable files are in workspace-locks/<id>, outside the moved tree:
	// Windows rejects directory renames while a descendant file is held open.
	if err = root.Rename(from, destination); err != nil {
		return s.Snapshot(), fmt.Errorf("ワークスペースを退避できません: %w", err)
	}
	next := ""
	if len(remaining) > 0 {
		next = remaining[0].ID
	}
	if err = s.writeActiveWorkspace(next); err != nil {
		if rollbackErr := root.Rename(destination, from); rollbackErr == nil {
			return s.Snapshot(), err
		} else {
			// A failed rollback leaves the data safely in trash. Drop editing
			// authority so later requests cannot recreate the removed workspace.
			s.resetAfterWorkspaceDelete(id, remaining, registry)
			return s.Snapshot(), fmt.Errorf("ワークスペースは%sに退避しましたが、選択の保存と復元に失敗しました: %w", filepath.Join(base, destination), errors.Join(err, rollbackErr))
		}
	}
	s.resetAfterWorkspaceDelete(id, remaining, registry)
	if next != "" {
		if err = s.activateWorkspace(next, false); err != nil {
			s.mu.Lock()
			s.state.LastError = s.redactLocked("削除は完了しました。次のワークスペースを開けません: " + err.Error())
			s.mu.Unlock()
		}
	}
	return s.Snapshot(), nil
}

// DeleteRule publishes a new local version; earlier execution definitions remain intact.
func (s *Service) DeleteRule(id string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	if !ruleformat.ValidID(id) {
		return s.Snapshot(), fmt.Errorf("ルールIDが不正です")
	}
	s.mu.Lock()
	cfg := s.state.Config
	w := model.Workspace{ID: s.state.ActiveWorkspaceID, Root: cfg.Root}
	for _, v := range s.workspaces {
		if v.ID == w.ID {
			w.Name = v.Name
		}
	}
	s.mu.Unlock()
	if w.ID == "" || cfg.RulesPath == "" {
		return s.Snapshot(), fmt.Errorf("ルールが見つかりません: %s", id)
	}
	owner, err := s.acquireRuleLease(w.ID, id)
	if err != nil {
		return s.Snapshot(), err
	}
	if owner != nil {
		return s.Snapshot(), fmt.Errorf("他のユーザーがこのルールを編集中です（%s / %s）", owner.Owner, owner.Host)
	}
	defer s.releaseRuleLease()
	p, err := rulepack.Snapshot(cfg.RulesPath)
	if err != nil {
		return s.Snapshot(), err
	}
	remaining := []rulepack.Entry{}
	found := false
	for _, r := range p.Rules {
		if r.ID == id {
			found = true
		} else {
			remaining = append(remaining, r)
		}
	}
	if !found {
		return s.Snapshot(), fmt.Errorf("ルールが見つかりません: %s", id)
	}
	p.Rules = remaining
	return s.installRuleVersion(w, cfg, p, true)
}
