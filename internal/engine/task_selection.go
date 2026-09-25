package engine

import (
	"fmt"

	"onebyone/internal/model"
)

// SetTaskSelection receives the complete checked set from execution settings.
// Selection lives in the queue itself, so sessions and scans retain it without
// changing a task's outcome, attempts or historical results.
func (s *Service) SetTaskSelection(files []string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	cfg, active := s.state.Config, s.state.ActiveWorkspaceID
	tasks := copyTasks(s.state.Tasks)
	s.mu.Unlock()
	if active == "" || cfg.QueuePath == "" {
		return s.Snapshot(), fmt.Errorf("先にワークスペースを選び、実行設定でファイルを抽出してください")
	}
	known := make(map[string]bool, len(tasks))
	for _, task := range tasks {
		known[task.File] = true
	}
	checked := make(map[string]bool, len(files))
	for _, file := range files {
		if !known[file] {
			return s.Snapshot(), fmt.Errorf("抽出した対象一覧にないファイルは選択できません: %s", file)
		}
		checked[file] = true
	}
	changed := false
	for i := range tasks {
		excluded := !checked[tasks[i].File]
		changed = changed || tasks[i].Excluded != excluded
		tasks[i].Excluded = excluded
	}
	if !changed {
		return s.Snapshot(), nil
	}
	unlock, err := lockSharedQueue(cfg)
	if err != nil {
		return s.Snapshot(), err
	}
	defer unlock()
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
