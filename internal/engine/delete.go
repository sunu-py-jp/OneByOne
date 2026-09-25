package engine

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/model"
	"onebyone/internal/privateconfig"
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

// DeleteRule creates a new local rule directory with just this ID omitted.
// Other malformed rules remain intact and visible as diagnostics, so each can
// be removed independently. Existing packages and run history stay immutable.
func (s *Service) DeleteRule(id string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	if !editableRuleID.MatchString(id) {
		return s.Snapshot(), fmt.Errorf("ルールIDが不正です")
	}
	s.mu.Lock()
	cfg := s.state.Config
	w := model.Workspace{ID: s.state.ActiveWorkspaceID, Root: cfg.Root}
	for _, item := range s.workspaces {
		if item.ID == w.ID {
			w.Name = item.Name
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
	// Keep the newly acquired rule lock until after publishing the new setting.
	defer s.releaseRuleLease()
	rulesPath, cleanup, err := s.copyRulesWithout(cfg.RulesPath, w.ID, id)
	if err != nil {
		return s.Snapshot(), err
	}
	committed := false
	defer func() { cleanup(!committed) }()
	cfg.RulesPath = rulesPath
	if cfg.RulePackageName == "" {
		cfg.RulePackageName = "rules.oborules"
	}
	cat, catalogErr := passiveCatalog(cfg)
	if err = s.writeWorkspaceSetting(w, cfg); err != nil {
		return s.Snapshot(), err
	}
	committed = true
	s.mu.Lock()
	s.state.Config, s.cat = cfg, cat
	s.state.Rules = []model.Rule{}
	if cat != nil {
		s.state.Rules = cat.Rules
	}
	s.setWorkspaceIssueLocked(w.ID, "catalog", catalogErr, "rules", "")
	if catalogErr != nil {
		s.state.LastError = s.redactLocked(catalogErr.Error())
	}
	s.recountLocked()
	s.mu.Unlock()
	return s.Snapshot(), nil
}

// This copy intentionally does not use rulepack.Snapshot, whose format checks
// reject the invalid rules that the user is trying to remove. It is bounded,
// rejects links/special files, and copies bytes only into a new private stage.
func (s *Service) copyRulesWithout(path, workspaceID, removedID string) (string, func(bool), error) {
	checked, err := settingsRoot(path)
	if err != nil {
		return "", nil, err
	}
	source, err := os.OpenRoot(checked)
	if err != nil {
		return "", nil, err
	}
	defer source.Close()
	info, err := source.Lstat(removedID)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("削除するルールフォルダが見つからないか、通常のフォルダではありません: %s", removedID)
	}
	setting, err := s.workspacePath(workspaceID, "setting.json")
	if err != nil {
		return "", nil, err
	}
	parentPath := filepath.Join(filepath.Dir(setting), "rule-packages")
	if err = ensureSharedOutputDirectory(parentPath, 0700); err != nil {
		return "", nil, err
	}
	parent, err := os.OpenRoot(parentPath)
	if err != nil {
		return "", nil, err
	}
	stage := uid()
	cleanup := func(remove bool) {
		if remove {
			_ = parent.RemoveAll(stage)
		}
		_ = parent.Close()
	}
	if err = parent.Mkdir(stage, 0700); err != nil {
		parent.Close()
		return "", nil, err
	}
	stageRoot, err := parent.OpenRoot(stage)
	if err != nil {
		cleanup(true)
		return "", nil, err
	}
	defer stageRoot.Close()
	if err = stageRoot.Mkdir("rules", 0700); err != nil {
		cleanup(true)
		return "", nil, err
	}
	destination, err := stageRoot.OpenRoot("rules")
	if err != nil {
		cleanup(true)
		return "", nil, err
	}
	defer destination.Close()
	entries, total := 0, int64(0)
	remainingRules, err := copyRuleResources(source, destination, removedID, 0, &entries, &total)
	if err != nil {
		cleanup(true)
		return "", nil, err
	}
	if remainingRules == 0 {
		return "", cleanup, nil
	}
	return filepath.Join(parentPath, stage, "rules"), cleanup, nil
}

func copyRuleResources(source, destination *os.Root, removedID string, depth int, count *int, total *int64) (int, error) {
	if depth > 32 {
		return 0, fmt.Errorf("ルール資材のフォルダ階層が深すぎます")
	}
	folder, err := source.Open(".")
	if err != nil {
		return 0, err
	}
	entries, err := folder.ReadDir(rulepack.MaxFiles + 1 - *count)
	folder.Close()
	if err != nil && err != io.EOF {
		return 0, err
	}
	*count += len(entries)
	if *count > rulepack.MaxFiles {
		return 0, fmt.Errorf("ルール資材の項目数が上限を超えています")
	}
	remainingRules := 0
	for _, entry := range entries {
		if depth == 0 && entry.Name() == removedID {
			continue
		}
		before, err := source.Lstat(entry.Name())
		if err != nil {
			return 0, err
		}
		if before.Mode()&os.ModeSymlink != 0 || (!before.IsDir() && !before.Mode().IsRegular()) {
			return 0, fmt.Errorf("ルール資材にリンクや特殊ファイルは含められません: %s", entry.Name())
		}
		if before.IsDir() {
			if depth == 0 && !strings.HasPrefix(entry.Name(), ".") {
				remainingRules++
			}
			if err = destination.Mkdir(entry.Name(), 0700); err != nil {
				return 0, err
			}
			input, err := source.OpenRoot(entry.Name())
			if err != nil {
				return 0, err
			}
			after, statErr := input.Stat(".")
			current, currentErr := source.Lstat(entry.Name())
			if statErr != nil || currentErr != nil || !os.SameFile(before, after) || !os.SameFile(after, current) {
				input.Close()
				return 0, fmt.Errorf("ルール資材がコピー中に変更されました")
			}
			output, err := destination.OpenRoot(entry.Name())
			if err != nil {
				input.Close()
				return 0, err
			}
			_, err = copyRuleResources(input, output, "", depth+1, count, total)
			input.Close()
			output.Close()
			if err != nil {
				return 0, err
			}
			continue
		}
		if before.Size() > rulepack.MaxFileBytes || before.Size() > rulepack.MaxExpandedBytes-*total {
			return 0, fmt.Errorf("ルール資材のサイズが上限を超えています")
		}
		input, err := source.Open(entry.Name())
		if err != nil {
			return 0, err
		}
		after, statErr := input.Stat()
		current, currentErr := source.Lstat(entry.Name())
		if statErr != nil || currentErr != nil || !after.Mode().IsRegular() || !current.Mode().IsRegular() || !os.SameFile(before, after) || !os.SameFile(after, current) {
			input.Close()
			return 0, fmt.Errorf("ルール資材がコピー中に変更されました")
		}
		data, readErr := io.ReadAll(io.LimitReader(input, rulepack.MaxFileBytes+1))
		input.Close()
		*total += int64(len(data))
		if readErr != nil {
			return 0, readErr
		}
		if int64(len(data)) > rulepack.MaxFileBytes || *total > rulepack.MaxExpandedBytes {
			return 0, fmt.Errorf("ルール資材のサイズが上限を超えています")
		}
		if err = destination.WriteFile(entry.Name(), data, 0600); err != nil {
			return 0, err
		}
	}
	return remainingRules, nil
}
