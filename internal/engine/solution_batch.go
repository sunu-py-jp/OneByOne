package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"onebyone/internal/model"
)

func solutionExecutionID(run *model.SolutionRun) string {
	if run.ExecutionID != "" {
		return run.ExecutionID
	}
	return run.ID
}

func attemptMatchesSolutionRun(a model.Attempt, run *model.SolutionRun) bool {
	return a.SolutionRunID == run.ID || (a.SolutionRunID == "" && a.ExecutionID == run.ID)
}

func copySolutionBatch(batch *model.SolutionBatch) *model.SolutionBatch {
	if batch == nil {
		return nil
	}
	b, _ := json.Marshal(batch)
	var out model.SolutionBatch
	_ = json.Unmarshal(b, &out)
	return &out
}

func (s *Service) syncSolutionBatchLocked() {
	batch, run := s.state.SolutionBatch, s.state.SolutionRun
	if batch == nil || run == nil {
		return
	}
	for i := range batch.Runs {
		if batch.Runs[i].ID == run.ID {
			batch.Runs[i] = *run
			return
		}
	}
}

func finishSolutionBatch(batch *model.SolutionBatch, interrupted bool) {
	batch.Status = "completed"
	for i := range batch.Runs {
		r := &batch.Runs[i]
		if r.Status == "pending" || r.Status == "running" {
			r.Status = "not_run"
			r.Note = "実行が終了したため未処理です。次回の実行対象として選択できます"
		}
		if r.Status == "needs_human" && batch.Status == "completed" {
			batch.Status = "needs_human"
		}
		if r.Status == "failed" {
			batch.Status = "failed"
		}
		if (r.Status == "stopped" || r.Status == "not_run") && batch.Status != "failed" {
			batch.Status = "stopped"
		}
	}
	if interrupted {
		batch.Status = "stopped"
	}
	batch.FinishedAt = now()
}

// Each solution is an independent adoption unit, but one execution owns the
// ordered queue and its complete history. Shared inputs start from accepted HEAD.
func (s *Service) runSolutionBatch(ctx context.Context, cfg model.Config) (batchErr error) {
	s.mu.Lock()
	id := uid()
	if s.activeExecution != nil {
		id = s.activeExecution.Run.ID
	}
	paths := cfg.Solution.SelectedPaths()
	batch := &model.SolutionBatch{ID: id, Status: "running", Paths: paths, Runs: make([]model.SolutionRun, len(paths)), StartedAt: now()}
	for i, path := range paths {
		runID := id
		if i > 0 {
			runID = uid()
		}
		batch.Runs[i] = model.SolutionRun{ID: runID, ExecutionID: id, Path: path, Status: "pending", Builds: []model.SolutionBuild{}, Files: []string{}}
	}
	s.state.SolutionBatch, s.state.SolutionRun = batch, nil
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.syncSolutionBatchLocked()
		finishSolutionBatch(batch, ctx.Err() != nil)
		if batchErr != nil {
			batch.Note = s.redactLocked(batchErr.Error())
			if _, held := batchErr.(*solutionHoldError); !held && ctx.Err() == nil {
				batch.Status = "failed"
			}
		}
		s.recountLocked()
		s.mu.Unlock()
		if err := s.persist(); err != nil {
			batchErr = fmt.Errorf("ソリューション一覧の結果を保存できません: %w", err)
			s.mu.Lock()
			batch.Status, batch.Note = "failed", s.redactLocked(batchErr.Error())
			s.mu.Unlock()
		}
	}()
	unlock, err := lockSharedQueue(cfg)
	if err != nil {
		return err
	}
	defer unlock()
	if err = s.persist(); err != nil {
		return err
	}
	held := []string{}
	for i, path := range paths {
		if err = ctx.Err(); err != nil {
			return err
		}
		s.mu.Lock()
		batch.CurrentIndex = i
		batch.Runs[i].Status, batch.Runs[i].StartedAt = "running", now()
		s.state.SolutionRun = &batch.Runs[i]
		s.mu.Unlock()
		if err = s.persist(); err != nil {
			return err
		}
		s.log("info", fmt.Sprintf("ソリューション %d/%d: %s", i+1, len(paths), path))
		current := cfg
		settings := *cfg.Solution
		settings.Path = path
		current.Solution = &settings
		err = s.runSolution(ctx, current)
		s.mu.Lock()
		run := s.state.SolutionRun
		// Infrastructure can fail before the inner execution creates a scratch.
		if err != nil && run.Status == "running" {
			run.Status, run.FinishedAt, run.Note = "failed", now(), s.redactLocked(err.Error())
			if ctx.Err() != nil {
				run.Status = "stopped"
			}
			s.recordSolutionFailureLocked(run)
		}
		s.syncSolutionBatchLocked()
		s.mu.Unlock()
		if err != nil {
			if _, ok := err.(*solutionHoldError); ok && ctx.Err() == nil {
				held = append(held, path+": "+err.Error())
				s.log("warn", "要確認として次のソリューションへ進みます: "+path)
				continue
			}
			return err
		}
	}
	if len(held) > 0 {
		return &solutionHoldError{strings.Join(held, "\n")}
	}
	return nil
}

// A later solution can reference the same physical inputs as an earlier one.
// Validate those earlier successes against the candidate before advancing HEAD.
func (s *Service) verifyEarlierSolutions(ctx context.Context, source, scratch string, m manifest, head string, cfg model.Config, original, expected map[string][]byte, result model.SolutionBuild, build func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error)) (model.SolutionBuild, error) {
	s.mu.Lock()
	batch := copySolutionBatch(s.state.SolutionBatch)
	s.mu.Unlock()
	if batch == nil {
		return result, nil
	}
	changed := map[string]bool{}
	for file, data := range expected {
		if digest(data) != digest(original[file]) {
			changed[file] = true
		}
	}
	for i := 0; i < batch.CurrentIndex; i++ {
		previous := batch.Runs[i]
		if previous.Status != "completed" {
			continue
		}
		affected := false
		for _, file := range previous.SourceFiles {
			if changed[file] {
				affected = true
				break
			}
		}
		if !affected {
			continue
		}
		s.log("info", "共有ファイルの変更を先行ソリューションで再検証しています: "+previous.Path)
		settings := *cfg.Solution
		settings.Path = previous.Path
		check, err := build(ctx, source, settings)
		result.Output += fmt.Sprintf("\n\n[共有入力の検証: %s]\nMSBuild success: %t; exit code: %d\n%s", previous.Path, check.Success, check.ExitCode, check.Output)
		for _, diagnostic := range check.Diagnostics {
			diagnostic.Message = "[" + previous.Path + "] " + diagnostic.Message
			result.Diagnostics = append(result.Diagnostics, diagnostic)
		}
		if !check.Success || err != nil {
			result.Success = false
			result.ExitCode = check.ExitCode
		}
		if check.FinishedAt != "" {
			result.FinishedAt = check.FinishedAt
		}
		if err != nil {
			return result, err
		}
		if err = verifySolutionScratch(ctx, scratch, m.SourceRelative, head, expected); err != nil {
			return result, err
		}
	}
	return result, nil
}
