package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"onebyone/internal/model"
)

// ValidateTargetFolder checks source readiness without modifying the selected
// directory. Starting a correction run repeats the same Git checks.
func (s *Service) ValidateTargetFolder(root string) (string, error) {
	return s.validateTargetFolder(root, true)
}

func (s *Service) validateTargetFolder(root string, requireReady bool) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("最初に対象フォルダを指定してください")
	}
	root, err := settingsRoot(root)
	if err != nil {
		return "", err
	}
	if err = s.validateTargetLocation(root); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if requireReady {
		_, _, err = readyGitSource(ctx, root)
	} else {
		_, err = sourceGitRepository(ctx, root)
	}
	if err != nil {
		return "", err
	}
	return root, nil
}

func targetOverlaps(root, path string) bool {
	return path != "" && (isAtOrWithin(root, path) || isAtOrWithin(path, root))
}

func validateTargetQueue(root, queue string) error {
	if queue == "" {
		return nil
	}
	for _, path := range []string{queue, queue + ".artifacts", queue + ".worktree", filepath.Join(filepath.Dir(queue), "reports")} {
		if targetOverlaps(root, path) {
			return fmt.Errorf("実行データの保存場所は対象フォルダにできません。別の対象フォルダを選択してください")
		}
	}
	return nil
}

func (s *Service) validateTargetLocation(root string) error {
	base, err := s.localDirectory()
	if err != nil {
		return err
	}
	paths := []string{filepath.Join(base, "workspaces"), filepath.Join(base, "workspace-trash"), filepath.Join(base, "workspace-locks")}
	if s.configPath != "" {
		path, err := filepath.Abs(s.configPath)
		if err != nil {
			return err
		}
		paths = append(paths, path)
	}
	if s.personal != nil {
		paths = append(paths, filepath.Dir(s.personal.ConnectionsDirectory()))
	}
	for _, path := range paths {
		if targetOverlaps(root, path) {
			return fmt.Errorf("対象フォルダとアプリのローカル設定の保存先は別の場所に指定してください")
		}
	}
	s.mu.Lock()
	queues := []string{s.state.Config.QueuePath}
	for _, item := range s.workspaces {
		queues = append(queues, item.QueuePath)
	}
	s.mu.Unlock()
	for _, queue := range queues {
		if err := validateTargetQueue(root, queue); err != nil {
			return err
		}
	}
	return nil
}

// ChangeTargetFolder preserves the workspace and its settings, while starting
// a separate output directory. Previous queues, reports and Git worktrees stay
// on disk exactly where they were and cannot become the new target's session.
func (s *Service) ChangeTargetFolder(root string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	id, c := s.state.ActiveWorkspaceID, s.state.Config
	s.mu.Unlock()
	if id == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選択してください")
	}
	root, err := s.ValidateTargetFolder(root)
	if err != nil {
		return s.Snapshot(), err
	}
	if sameRoot(root, c.Root) {
		s.refreshActiveWorkspaceDiagnostics()
		return s.Snapshot(), nil
	}
	items, err := s.listLocalWorkspaces()
	if err != nil {
		return s.Snapshot(), err
	}
	index := -1
	for i, item := range items {
		if item.ID == id {
			index = i
		}
	}
	if index < 0 || !sameRoot(items[index].Root, c.Root) {
		return s.Snapshot(), fmt.Errorf("対象フォルダの設定が変更されています。ワークスペースを開き直してください")
	}
	w := items[index].Workspace
	w.Root, c.Root = root, root
	queue, err := s.workspacePath(id, "runs/"+uid()+"/queue.jsonl")
	if err != nil {
		return s.Snapshot(), err
	}
	if _, err = os.Lstat(filepath.Dir(queue)); !os.IsNotExist(err) {
		return s.Snapshot(), fmt.Errorf("新しい結果の保存先を確保できません。もう一度変更してください")
	}
	c.QueuePath = queue
	if c, err = normalizeConfig(c); err != nil {
		return s.Snapshot(), err
	}
	registry, err := s.personal.LoadConnections()
	if err != nil {
		return s.Snapshot(), err
	}
	c, _ = selectedConnection(c, registry)
	if err = s.writeWorkspaceSetting(w, c); err != nil {
		return s.Snapshot(), err
	}
	// The lock file is keyed by workspace ID, so retain its lease continuously
	// through the settings write instead of attempting to reacquire our own lock.
	s.adoptWorkspaceLease(w, s.workspaceLease)
	items[index] = workspaceRecord{Workspace: w, QueuePath: c.QueuePath}
	s.mu.Lock()
	s.workspaces = items
	delete(s.workspaceIssues, id)
	s.state = emptyState(c)
	s.state.ActiveWorkspaceID = id
	s.meta, s.cat, s.cancel, s.done = manifest{Version: 1}, nil, nil, nil
	s.publishConnectionsLocked(registry)
	s.publishWorkspacesLocked()
	s.mu.Unlock()
	if err = s.restoreWorkspace(c); err != nil {
		s.mu.Lock()
		s.state.LastError = s.redactLocked(err.Error())
		s.mu.Unlock()
	}
	return s.Snapshot(), nil
}
