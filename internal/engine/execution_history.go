package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"onebyone/internal/model"
	"onebyone/internal/store"
)

// A completed execution is an immutable snapshot, separate from the mutable
// queue. In particular a resumed repair may reuse an attempt ID; historical
// artifacts must therefore be copied before another execution can start.
type executionRecord struct {
	Version       int                `json:"version"`
	Run           model.ExecutionRun `json:"run"`
	State         model.State        `json:"state"`
	TargetFiles   []string           `json:"targetFiles"`
	Meta          manifest           `json:"manifest"`
	StartingUsage model.Usage        `json:"startingUsage"`
	EvidenceReady bool               `json:"evidenceReady"`
	EvidenceRuns  map[string]string  `json:"evidenceRuns,omitempty"`
	targetSet     map[string]bool
}

type executionEvidence struct {
	Before  string                   `json:"before"`
	After   string                   `json:"after"`
	Diff    string                   `json:"diff"`
	Changes []model.ChangeReportItem `json:"changes"`
	Error   string                   `json:"error,omitempty"`
}

func executionDirectory(queue, id string) (string, error) {
	if queue == "" || len(id) != 24 || strings.Trim(id, "0123456789abcdef") != "" {
		return "", fmt.Errorf("実行IDが不正です")
	}
	return filepath.Join(queue+".executions", id), nil
}

func executionUsage(current, baseline model.Usage) model.Usage {
	return model.Usage{InputTokens: max(0, current.InputTokens-baseline.InputTokens), CachedTokens: max(0, current.CachedTokens-baseline.CachedTokens), OutputTokens: max(0, current.OutputTokens-baseline.OutputTokens), CostUSD: max(0, current.CostUSD-baseline.CostUSD), Turns: max(0, current.Turns-baseline.Turns), Uncertain: current.Uncertain}
}

func (s *Service) beginExecution(limit int) error {
	s.mu.Lock()
	r := &executionRecord{Version: 1, Run: model.ExecutionRun{ID: uid(), StartedAt: now(), Status: "running"}, StartingUsage: s.state.Usage, TargetFiles: []string{}, targetSet: map[string]bool{}}
	for _, task := range s.state.Tasks {
		if task.Excluded || (task.Status != "pending" && task.Status != "failed" && task.Status != "running") {
			continue
		}
		if limit > 0 && len(r.TargetFiles) >= limit {
			break
		}
		r.TargetFiles = append(r.TargetFiles, task.File)
		r.targetSet[task.File] = true
	}
	r.Run.TargetCount = len(r.TargetFiles)
	s.activeExecution = r
	s.state.ExecutionRuns = append(s.state.ExecutionRuns, r.Run)
	s.mu.Unlock()
	if err := s.saveExecutionSnapshot(false); err != nil {
		s.mu.Lock()
		s.state.ExecutionRuns = s.state.ExecutionRuns[:len(s.state.ExecutionRuns)-1]
		s.activeExecution = nil
		s.mu.Unlock()
		return fmt.Errorf("実行履歴を保存できません: %w", err)
	}
	return nil
}

func (s *Service) executionTargetsLocked(file string) bool {
	if s.activeExecution == nil {
		return true // direct runner calls in existing integrations
	}
	return s.activeExecution.targetSet[file]
}

func (s *Service) captureExecution() *executionRecord {
	st := s.Snapshot()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.activeExecution == nil {
		return nil
	}
	r := *s.activeExecution
	r.TargetFiles = append([]string{}, r.TargetFiles...)
	r.State = st
	r.State.Config = withoutCredential(st.Config)
	r.State.LLMConnections = nil
	r.State.LLMSettingsPath = ""
	r.State.WorkspaceLock = nil
	if r.Run.Status != "running" {
		r.State.Running, r.State.Phase, r.State.CurrentFile = false, "idle", ""
	}
	r.State.Usage = executionUsage(st.Usage, r.StartingUsage)
	r.Meta = s.meta
	r.Meta.Config = storedConfig(st.Config)
	r.Meta.Worktree = st.Worktree
	r.Meta.Branch = st.Branch
	return &r
}

func (s *Service) saveExecutionSnapshot(freeze bool) error {
	s.executionMu.Lock()
	defer s.executionMu.Unlock()
	r := s.captureExecution()
	if r == nil {
		return nil
	}
	backfillChangeReports(r.State.Config, r.State.Tasks)
	if freeze {
		if err := freezeExecutionEvidence(r); err != nil {
			return err
		}
	}
	return writeExecutionRecord(r)
}

func writeExecutionRecord(r *executionRecord) error {
	dir, err := executionDirectory(r.State.Config.QueuePath, r.Run.ID)
	if err != nil {
		return err
	}
	if err := writeOutputJSON(r.State.Config, filepath.Join(dir, "snapshot.json"), r); err != nil {
		return err
	}
	return writeOutputJSON(r.State.Config, filepath.Join(dir, "summary.json"), r.Run)
}

func (s *Service) finishExecution(ctx context.Context, runErr error) error {
	s.mu.Lock()
	if s.activeExecution == nil {
		s.mu.Unlock()
		return nil
	}
	r := &s.activeExecution.Run
	r.Status, r.FinishedAt = "completed", now()
	if ctx.Err() != nil {
		r.Status = "stopped"
	} else if runErr != nil {
		r.Status = "failed"
	}
	if runErr != nil {
		r.Error = s.redactLocked(runErr.Error())
	}
	for i := range s.state.ExecutionRuns {
		if s.state.ExecutionRuns[i].ID == r.ID {
			s.state.ExecutionRuns[i] = *r
		}
	}
	s.mu.Unlock()
	err := s.saveExecutionSnapshot(true)
	if err == nil {
		s.mu.Lock()
		s.activeExecution = nil
		s.mu.Unlock()
	}
	return err
}

// A failed archive write must be resolved before retry, discard, or settings
// changes can alter the queue/artifacts from the just-finished execution.
func (s *Service) flushFinishedExecution() error {
	s.mu.Lock()
	pending := s.activeExecution != nil && !s.state.Running
	s.mu.Unlock()
	if !pending {
		return nil
	}
	if err := s.saveExecutionSnapshot(true); err != nil {
		return fmt.Errorf("前回の実行履歴を保存できないため操作できません: %w", err)
	}
	s.mu.Lock()
	s.activeExecution = nil
	s.mu.Unlock()
	return nil
}

func freezeExecutionEvidence(r *executionRecord) error {
	dir, err := executionDirectory(r.State.Config.QueuePath, r.Run.ID)
	if err != nil {
		return err
	}
	r.EvidenceRuns = map[string]string{}
	for _, task := range r.State.Tasks {
		for _, h := range task.History {
			// Earlier executions already own immutable copies. Reuse them rather
			// than duplicating all past file bodies on every press of Execute.
			if h.ExecutionID != "" && h.ExecutionID != r.Run.ID {
				if previous, err := executionDirectory(r.State.Config.QueuePath, h.ExecutionID); err == nil {
					if info, err := os.Stat(filepath.Join(previous, "attempts", h.ID+".json")); err == nil && info.Mode().IsRegular() {
						r.EvidenceRuns[h.ID] = h.ExecutionID
						continue
					}
				}
			}
			e := attemptEvidence(r.State.Config, h)
			if err := writeOutputJSON(r.State.Config, filepath.Join(dir, "attempts", h.ID+".json"), e); err != nil {
				return err
			}
		}
	}
	r.EvidenceReady = true
	return nil
}

func attemptEvidence(cfg model.Config, h model.Attempt) executionEvidence {
	e := executionEvidence{Changes: []model.ChangeReportItem{}}
	base := filepath.Join(cfg.QueuePath+".artifacts", h.ID)
	if b, err := os.ReadFile(base + ".before"); err == nil {
		e.Before = string(b)
	}
	e.After = e.Before
	if b, err := os.ReadFile(base + ".after"); err == nil {
		e.After = string(b)
	}
	if b, err := os.ReadFile(base + ".diff"); err == nil {
		e.Diff = string(b)
	}
	for _, item := range h.Changes {
		item.SourceAttemptID = h.ID
		e.Changes = append(e.Changes, item)
	}
	return e
}

func readExecutionRecord(queue, id string) (*executionRecord, error) {
	dir, err := executionDirectory(queue, id)
	if err != nil {
		return nil, err
	}
	b, err := store.ReadFile(filepath.Join(dir, "snapshot.json"))
	if err != nil {
		return nil, fmt.Errorf("実行履歴を読み込めません: %w", err)
	}
	var r executionRecord
	if err = json.Unmarshal(b, &r); err != nil || r.Version != 1 || r.Run.ID != id || !sameRoot(r.State.Config.QueuePath, queue) {
		return nil, fmt.Errorf("実行履歴の形式が不正です")
	}
	return &r, nil
}

func (s *Service) loadExecutionRuns(queue string) error {
	entries, err := os.ReadDir(queue + ".executions")
	if errors.Is(err, os.ErrNotExist) {
		s.mu.Lock()
		s.state.ExecutionRuns = []model.ExecutionRun{}
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	// Passive diagnostics also restore queues without a lease. They must never
	// turn another window's live execution into an interrupted one.
	readOnly := s.state.ReadOnly || s.workspaceLease == nil
	s.mu.Unlock()
	runs := []model.ExecutionRun{}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir, err := executionDirectory(queue, entry.Name())
		if err != nil {
			continue
		}
		b, err := store.ReadFile(filepath.Join(dir, "summary.json"))
		var run model.ExecutionRun
		if err != nil || json.Unmarshal(b, &run) != nil || run.ID != entry.Name() {
			r, loadErr := readExecutionRecord(queue, entry.Name())
			if loadErr != nil {
				return loadErr
			}
			run = r.Run
		}
		if run.Status == "running" && !readOnly {
			r, err := readExecutionRecord(queue, run.ID)
			if err != nil {
				return err
			}
			// snapshot.json is published before the small listing summary. A
			// crash between those writes must not relabel a completed run.
			if r.Run.Status != "running" {
				runs = append(runs, r.Run)
				continue
			}
			r.Run.Status, r.Run.FinishedAt = "stopped", now()
			r.Run.Error = "アプリの終了により実行が中断されました"
			// The queue and per-turn sidecars can be newer than the last run
			// snapshot. Reconcile their recorded accounting without modifying
			// the private worktree; normal runner recovery remains authoritative
			// for adopting an interrupted commit.
			s.mu.Lock()
			r.State.Tasks = copyTasks(s.state.Tasks)
			r.Meta = s.meta
			r.Meta.Config = storedConfig(r.State.Config)
			r.State.Worktree, r.State.Branch = s.state.Worktree, s.state.Branch
			s.mu.Unlock()
			recoverExecutionAccounting(r)
			r.State.Running, r.State.Phase, r.State.CurrentFile = false, "idle", ""
			for i := range r.State.Tasks {
				if r.State.Tasks[i].Status == "running" {
					r.State.Tasks[i].Status = "failed"
					r.State.Tasks[i].Note = r.Run.Error
				}
			}
			for i := range r.State.ExecutionRuns {
				if r.State.ExecutionRuns[i].ID == r.Run.ID {
					r.State.ExecutionRuns[i] = r.Run
				}
			}
			if !r.EvidenceReady {
				if err := freezeExecutionEvidence(r); err != nil {
					return err
				}
			}
			if err := writeExecutionRecord(r); err != nil {
				return err
			}
			run = r.Run
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].StartedAt < runs[j].StartedAt })
	s.mu.Lock()
	s.state.ExecutionRuns = runs
	s.mu.Unlock()
	return nil
}

func recoverExecutionAccounting(r *executionRecord) {
	var total model.Usage
	for i := range r.State.Tasks {
		t := &r.State.Tasks[i]
		for j := range t.History {
			h := &t.History[j]
			if h.ExecutionID == r.Run.ID && h.RepairPath != "" {
				if checkpoint, err := loadRepairCheckpoint(r.State.Config, *h); err == nil && checkpoint != nil {
					checkpoint.State.Usage.Uncertain = checkpoint.State.Usage.Uncertain || checkpoint.State.RequestPending
					h.Usage = usageSince(checkpoint.State.Usage, checkpoint.PriorUsage)
					h.Reviews = copyReviews(checkpoint.State.Reviews)
					if h.FinishedAt == "" {
						h.Outcome, h.FinishedAt, h.Note = "interrupted", r.Run.FinishedAt, r.Run.Error
					}
					h.Changes = buildChangeReport(*h, checkpoint)
				}
			}
			addUsage(&total, h.Usage)
		}
	}
	r.State.Usage = executionUsage(total, r.StartingUsage)
}

func (s *Service) executionRecord(id string) (*executionRecord, error) {
	s.mu.Lock()
	queue := s.state.Config.QueuePath
	active := s.activeExecution != nil && s.activeExecution.Run.ID == id
	s.mu.Unlock()
	if active {
		if r := s.captureExecution(); r != nil && r.Run.ID == id {
			return r, nil
		}
	}
	return readExecutionRecord(queue, id)
}

func (s *Service) GetExecutionRun(id string) (model.ExecutionRunResult, error) {
	r, err := s.executionRecord(id)
	if err != nil {
		return model.ExecutionRunResult{}, err
	}
	return model.ExecutionRunResult{Run: r.Run, State: r.State, TargetFiles: r.TargetFiles}, nil
}

func (s *Service) ExportExecutionReport(id, path string) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	result, err := s.GetExecutionRun(id)
	if err != nil {
		return "", err
	}
	result.State.Config = storedConfig(result.State.Config)
	result.State.LLMConnections = nil
	result.State.SelectedLLMConnectionID = ""
	return s.exportReportValue(path, result.State, result)
}

func (s *Service) GetExecutionFileDetail(id, file string, index int) (model.FileDetail, error) {
	r, err := s.executionRecord(id)
	if err != nil {
		return model.FileDetail{}, err
	}
	var task model.Task
	found := false
	for _, t := range r.State.Tasks {
		if t.File == file {
			task, found = t, true
			break
		}
	}
	if !found {
		return model.FileDetail{}, fmt.Errorf("この実行に存在しないファイルです")
	}
	d := model.FileDetail{Task: task, Cumulative: index < 0, Changes: []model.ChangeReportItem{}}
	if index >= len(task.History) {
		return d, fmt.Errorf("試行が見つかりません")
	}
	if !r.EvidenceReady {
		if index < 0 {
			return cumulativeFileDetail(r.State.Config, r.Meta, task)
		}
		e := attemptEvidence(r.State.Config, task.History[index])
		d.Before, d.After, d.Diff, d.Changes = e.Before, e.After, e.Diff, e.Changes
		return d, nil
	}
	if index < 0 {
		// These commit identities belong to the frozen execution. No live HEAD,
		// worktree contents or subsequent discard state participates in the read.
		if r.Meta.BaseCommit == "" {
			return d, fmt.Errorf("この実行は処理開始前に終了したため、ファイル内容は記録されていません")
		}
		return cumulativeFileDetail(r.State.Config, r.Meta, task)
	}
	dir, _ := executionDirectory(r.State.Config.QueuePath, r.Run.ID)
	if owner := r.EvidenceRuns[task.History[index].ID]; owner != "" {
		dir, err = executionDirectory(r.State.Config.QueuePath, owner)
		if err != nil {
			return d, err
		}
	}
	path := filepath.Join(dir, "attempts", task.History[index].ID+".json")
	b, err := os.ReadFile(path)
	if err != nil {
		return d, fmt.Errorf("実行時点のファイルを読み込めません: %w", err)
	}
	var e executionEvidence
	if err := json.Unmarshal(b, &e); err != nil {
		return d, err
	}
	if e.Error != "" {
		return d, fmt.Errorf("%s", e.Error)
	}
	d.Before, d.After, d.Diff, d.Changes = e.Before, e.After, e.Diff, e.Changes
	return d, nil
}
