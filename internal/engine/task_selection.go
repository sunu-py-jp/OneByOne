package engine

import (
	"context"
	"fmt"
	"os"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
)

// SetTaskSelection receives the complete checked set from execution settings.
// Selection lives in the queue itself, so sessions and scans retain it without
// changing historical results. Explicitly checking a held file requests the
// same resumable retry as the retry action; completed files remain completed.
func (s *Service) SetTaskSelection(files []string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	cfg, active, worktree := s.state.Config, s.state.ActiveWorkspaceID, s.state.Worktree
	tasks := copyTasks(s.state.Tasks)
	s.mu.Unlock()
	if active == "" || cfg.QueuePath == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選び、実行設定でファイルを抽出してください")
	}
	known := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		known[task.File] = len(task.Rules) > 0
	}
	checked := make(map[string]bool, len(files))
	for _, file := range files {
		if !known[file] {
			return s.Snapshot(), fmt.Errorf("現在のルールの対象外です。ルール条件を変更して対象を再抽出してください: %s", file)
		}
		checked[file] = true
	}
	changed, retryHeld := false, false
	for _, task := range tasks {
		changed = changed || task.Excluded == checked[task.File]
		retryHeld = retryHeld || checked[task.File] && task.Status == "needs_human"
	}
	if !changed && !retryHeld {
		return s.Snapshot(), nil
	}
	unlock, err := lockSharedQueue(cfg)
	if err != nil {
		return s.Snapshot(), err
	}
	defer unlock()
	if retryHeld && worktree != "" {
		// Reconcile an interrupted journal before enabling resumable execution,
		// as RetryTasks does. Never turn a pending Git write into a fresh attempt.
		if err := s.recover(context.Background()); err != nil {
			return s.Snapshot(), err
		}
		s.mu.Lock()
		tasks = copyTasks(s.state.Tasks)
		s.mu.Unlock()
	}
	s.mu.Lock()
	source := s.sourceDirLocked()
	s.mu.Unlock()
	for i := range tasks {
		task := &tasks[i]
		task.Excluded = !checked[task.File]
		if task.Excluded || task.Status != "needs_human" {
			continue
		}
		task.Status, task.ResumeRequested = "pending", true
		task.Note = "再試行に追加しました（計画・履歴を引き継ぎ、次の実行はターン数0から開始します）"
		task.UpdatedAt = now()
		if worktree != "" {
			if path, err := catalog.PathWithin(source, task.File); err == nil {
				if content, err := os.ReadFile(path); err == nil {
					task.InputHash = digest(content)
				}
			}
		}
	}
	// Only the queue changes. Write its atomic replacement before publishing
	// state so any validation, lock or write failure preserves the old selection.
	if err = writeOutputQueue(cfg, tasks); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	s.state.Tasks = tasks
	s.recountLocked()
	s.mu.Unlock()
	return s.Snapshot(), nil
}
