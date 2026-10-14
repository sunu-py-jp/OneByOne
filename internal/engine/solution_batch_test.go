package engine

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func solutionBatchFixture(t *testing.T, paths []string) (*Service, model.Config) {
	t.Helper()
	s, cfg := solutionFixture(t)
	// All three solutions reference the same project and therefore share VB
	// inputs. Their independent .sln identities must still retain separate runs.
	for _, name := range []string{"Second.sln", "Third.sln"} {
		writeTest(t, filepath.Join(cfg.Root, name), []byte(testSolution))
	}
	gitTest(t, cfg.Root, "add", "Second.sln", "Third.sln")
	gitTest(t, cfg.Root, "commit", "-qm", "add shared-project solutions")
	cfg.Solution.Path = ""
	cfg.Solution.Paths = paths
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	return s, s.Snapshot().Config
}

func solutionBatchNoChange(s *Service, visited *[]string) {
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		return model.SolutionBuild{Success: true}, nil
	}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		*visited = append(*visited, in.Config.Solution.Path)
		return agent.SolutionProposal{Status: "skipped", Note: "ソリューション全体を確認済み", Usage: model.Usage{InputTokens: 5, Turns: 1}}, nil
	}
}

func TestSolutionBatchSelectionOrderAndChangeAfterExecution(t *testing.T) {
	paths := []string{"Second.sln", "Demo.sln"}
	s, _ := solutionBatchFixture(t, paths)
	visited := []string{}
	solutionBatchNoChange(s, &visited)
	if err := s.Start(1); err != nil { // file-count limits cannot split a batch
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	if st.LastError != "" || st.SolutionBatch == nil || st.SolutionBatch.Status != "completed" || !reflect.DeepEqual(visited, paths) {
		t.Fatalf("selected execution order: visited=%v batch=%+v error=%s", visited, st.SolutionBatch, st.LastError)
	}
	if !reflect.DeepEqual(st.SolutionBatch.Paths, paths) || len(st.SolutionBatch.Runs) != 2 || len(st.ExecutionRuns) != 1 || st.SolutionBatch.ID != st.ExecutionRuns[0].ID {
		t.Fatalf("one selected batch must own one execution: %+v %+v", st.SolutionBatch, st.ExecutionRuns)
	}
	for i, run := range st.SolutionBatch.Runs {
		if run.Path != paths[i] || run.Status != "completed" || run.ExecutionID != st.SolutionBatch.ID || run.Commit != "" {
			t.Fatalf("no-change child run: %+v", run)
		}
	}
	cfg := st.Config
	cfg.Solution.Paths = []string{"Third.sln"}
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatalf("selection must remain editable after execution: %v", err)
	}
	visited = nil
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st = s.Snapshot()
	if st.LastError != "" || !reflect.DeepEqual(visited, []string{"Third.sln"}) || len(st.ExecutionRuns) != 2 || st.Usage.InputTokens != 15 {
		t.Fatalf("changed selection: visited=%v executions=%v usage=%+v error=%s", visited, st.ExecutionRuns, st.Usage, st.LastError)
	}
}

func TestSolutionBatchInitialDiscoveryAndExplicitEmptySelection(t *testing.T) {
	t.Run("discover-all", func(t *testing.T) {
		s, _ := solutionBatchFixture(t, nil)
		st, err := s.Scan()
		if err != nil {
			t.Fatal(err)
		}
		paths := []string{"Demo.sln", "Second.sln", "Third.sln"}
		if !reflect.DeepEqual(st.Config.Solution.SelectedPaths(), paths) {
			t.Fatalf("first extraction did not select discovered solutions: %+v", st.Config.Solution)
		}
		visited := []string{}
		solutionBatchNoChange(s, &visited)
		if err := s.Start(0); err != nil {
			t.Fatal(err)
		}
		s.Wait()
		if st := s.Snapshot(); st.LastError != "" || !reflect.DeepEqual(visited, paths) || st.SolutionBatch == nil || len(st.SolutionBatch.Runs) != 3 {
			t.Fatalf("discovered batch: visited=%v state=%+v", visited, st.SolutionBatch)
		}
	})
	t.Run("explicit-empty-overrides-legacy-path", func(t *testing.T) {
		s, cfg := solutionBatchFixture(t, []string{})
		cfg.Solution.Path = "Demo.sln"
		if _, err := s.SaveConfig(cfg); err != nil {
			t.Fatal(err)
		}
		if err := s.Start(0); err == nil {
			s.Stop()
			s.Wait()
			t.Fatal("explicit empty selection unexpectedly started")
		}
		if st := s.Snapshot(); st.Running || len(st.ExecutionRuns) != 0 || st.Config.Solution.Paths == nil {
			t.Fatalf("empty selection lost or created execution: %+v", st)
		}
	})
}

func TestSolutionBatchSharedSourcesCarryForwardAndArchivePerChild(t *testing.T) {
	paths := []string{"Demo.sln", "Second.sln"}
	s, cfg := solutionBatchFixture(t, paths)
	visited := []string{}
	s.buildSolution = func(_ context.Context, root string, settings model.SolutionSettings) (model.SolutionBuild, error) {
		if settings.Path == "Second.sln" {
			data, err := os.ReadFile(filepath.Join(root, "Demo/A.vb"))
			if err != nil || strings.Contains(string(data), "= 1") {
				return model.SolutionBuild{}, fmt.Errorf("second solution lost first accepted edit: %v", err)
			}
		}
		return model.SolutionBuild{Success: true}, nil
	}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		path := in.Config.Solution.Path
		visited = append(visited, path)
		old, next := "1", "2"
		if path == "Second.sln" {
			old, next = "2", "3"
		}
		if !strings.Contains(in.Files["Demo/A.vb"], "= "+old) {
			return agent.SolutionProposal{}, fmt.Errorf("%s expected accepted value %s", path, old)
		}
		return agent.SolutionProposal{Status: "modified", Note: path + " の共有コードを修正", Usage: model.Usage{InputTokens: 11, OutputTokens: 3, Turns: 1}, Files: []agent.SolutionFileEdit{{File: "Demo/A.vb", RuleIDs: []string{"raise-value"}, Edits: []model.Edit{{OldText: "Value As Integer = " + old, NewText: "Value As Integer = " + next}}}}}, nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	if st.LastError != "" || st.SolutionBatch == nil || st.SolutionBatch.Status != "completed" || !reflect.DeepEqual(visited, paths) {
		t.Fatalf("shared source batch: visited=%v batch=%+v error=%s", visited, st.SolutionBatch, st.LastError)
	}
	batch := st.SolutionBatch
	if batch.Runs[0].ID == batch.Runs[1].ID || batch.Runs[0].Commit == "" || batch.Runs[1].Commit == "" || batch.Runs[0].Commit == batch.Runs[1].Commit {
		t.Fatalf("child identities/commits were combined: %+v", batch.Runs)
	}
	if parent := gitTest(t, st.Worktree, "rev-parse", batch.Runs[1].Commit+"^"); parent != batch.Runs[0].Commit {
		t.Fatalf("second adoption did not extend first: %s", parent)
	}
	if st.Usage.InputTokens != 22 || st.Usage.OutputTokens != 6 || st.Usage.Turns != 2 || len(st.ExecutionRuns) != 1 {
		t.Fatalf("batch accounting: %+v %+v", st.Usage, st.ExecutionRuns)
	}
	archive, err := s.GetExecutionRun(batch.ID)
	if err != nil || archive.State.SolutionBatch == nil || archive.State.Usage.InputTokens != 22 || archive.Run.Status != "completed" {
		t.Fatalf("archived batch: %+v %v", archive, err)
	}
	for _, run := range archive.State.SolutionBatch.Runs {
		if run.ExecutionID != batch.ID || run.Usage.InputTokens != 11 || !containsSolutionBatchFile(run.SourceFiles, "Demo/A.vb") || !containsSolutionBatchFile(run.SourceFiles, run.Path) {
			t.Fatalf("child source/accounting identity: %+v", run)
		}
	}
	var shared model.Task
	for _, task := range archive.State.Tasks {
		for _, attempt := range task.History {
			if attempt.ExecutionID != batch.ID || attempt.SolutionRunID == "" {
				t.Fatalf("attempt belongs to wrong execution/child: %+v", attempt)
			}
		}
		if task.File == "Demo/A.vb" {
			shared = task
		}
	}
	if len(shared.History) != 2 {
		t.Fatalf("shared history was overwritten: %+v", shared)
	}
	for index, attempt := range shared.History {
		if attempt.SolutionRunID != batch.Runs[index].ID || attempt.Commit != batch.Runs[index].Commit {
			t.Fatalf("child-specific history: %+v", attempt)
		}
		detail, err := s.GetExecutionFileDetail(batch.ID, shared.File, index)
		if err != nil || !strings.Contains(detail.Before, fmt.Sprintf("= %d", index+1)) || !strings.Contains(detail.After, fmt.Sprintf("= %d", index+2)) {
			t.Fatalf("child-specific evidence %d: %+v %v", index, detail, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(cfg.Root, "Demo/A.vb")); err != nil || !strings.Contains(string(data), "= 1") {
		t.Fatalf("original checkout changed: %s %v", data, err)
	}
	configPath := s.configPath
	s.Close()
	reopened := workspaceNewService(t, configPath)
	got := reopened.Snapshot()
	if got.LastError != "" || !reflect.DeepEqual(got.SolutionBatch, batch) || got.Usage.InputTokens != 22 || !reflect.DeepEqual(got.Config.Solution.SelectedPaths(), paths) {
		t.Fatalf("batch did not survive reopen: batch=%+v usage=%+v settings=%+v error=%s", got.SolutionBatch, got.Usage, got.Config.Solution, got.LastError)
	}
	if len(got.ExecutionRuns) != 1 || !reflect.DeepEqual(got.Tasks, st.Tasks) {
		t.Fatal("reopening duplicated execution or child history")
	}
}

func containsSolutionBatchFile(files []string, expected string) bool {
	for _, file := range files {
		if file == expected {
			return true
		}
	}
	return false
}

func TestSolutionBatchEarlierSolutionRegressionFeedsRepairAndBlocksAdoption(t *testing.T) {
	for _, outcome := range []string{"repair", "iteration-limit"} {
		t.Run(outcome, func(t *testing.T) {
			s, cfg := solutionBatchFixture(t, []string{"Demo.sln", "Second.sln"})
			cfg.Solution.MaxIterations = 2
			if _, err := s.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			const regression = "shared-regression-marker: required member is unavailable"
			secondProposals, failedEarlierChecks := 0, 0
			sawRepairEvidence := false
			s.buildSolution = func(_ context.Context, root string, settings model.SolutionSettings) (model.SolutionBuild, error) {
				data, err := os.ReadFile(filepath.Join(root, "Demo/A.vb"))
				if err != nil {
					return model.SolutionBuild{}, err
				}
				// The later solution itself always compiles. Values 3 and 5 only
				// break the already adopted earlier solution's API requirements.
				if settings.Path == "Demo.sln" && (strings.Contains(string(data), "= 3") || strings.Contains(string(data), "= 5")) {
					failedEarlierChecks++
					return model.SolutionBuild{Success: false, ExitCode: 1, Output: regression, Diagnostics: []model.SourceDiagnostic{{File: "Demo/A.vb", Code: "BC30456", Message: regression}}}, nil
				}
				return model.SolutionBuild{Success: true, Output: settings.Path + " compiled successfully"}, nil
			}
			s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
				old, next := "1", "2"
				if in.Config.Solution.Path == "Second.sln" {
					secondProposals++
					old, next = "2", "3"
					if secondProposals > 1 {
						old, next = "3", "4"
						if outcome == "iteration-limit" {
							next = "5"
						}
						for _, expected := range []string{"MSBuild success: false", "Second.sln compiled successfully", "Demo.sln", "BC30456", regression} {
							if !strings.Contains(in.BuildOutput, expected) {
								return agent.SolutionProposal{}, fmt.Errorf("earlier solution failure omitted %q from repair evidence: %s", expected, in.BuildOutput)
							}
						}
						sawRepairEvidence = true
						st := s.Snapshot()
						accepted, err := os.ReadFile(filepath.Join(st.Worktree, "Demo/A.vb"))
						if err != nil || !strings.Contains(string(accepted), "= 2") {
							return agent.SolutionProposal{}, fmt.Errorf("regressing candidate was adopted before repair: %s %v", accepted, err)
						}
					}
				}
				if !strings.Contains(in.Files["Demo/A.vb"], "= "+old) {
					return agent.SolutionProposal{}, fmt.Errorf("repair lost current candidate %s", old)
				}
				return agent.SolutionProposal{Status: "modified", Note: "先行ソリューションの互換性を含めて修正", Files: []agent.SolutionFileEdit{{File: "Demo/A.vb", RuleIDs: []string{"raise-value"}, Edits: []model.Edit{{OldText: "Value As Integer = " + old, NewText: "Value As Integer = " + next}}}}}, nil
			}
			if err := s.Start(0); err != nil {
				t.Fatal(err)
			}
			s.Wait()
			st := s.Snapshot()
			if st.SolutionBatch == nil || len(st.SolutionBatch.Runs) != 2 || !sawRepairEvidence || secondProposals != 2 || failedEarlierChecks == 0 {
				t.Fatalf("earlier solution gate did not feed repair: batch=%+v proposals=%d checks=%d evidence=%t error=%s", st.SolutionBatch, secondProposals, failedEarlierChecks, sawRepairEvidence, st.LastError)
			}
			first, second := st.SolutionBatch.Runs[0], st.SolutionBatch.Runs[1]
			if first.Status != "completed" || first.Commit == "" {
				t.Fatalf("earlier adoption was lost: %+v", first)
			}
			var rejected *model.SolutionBuild
			for i := range second.Builds {
				if second.Builds[i].Iteration == 1 {
					rejected = &second.Builds[i]
				}
			}
			if rejected == nil || rejected.Success || rejected.ExitCode != 1 || len(rejected.Diagnostics) != 1 || !strings.Contains(rejected.Diagnostics[0].Message, "[Demo.sln]") {
				t.Fatalf("earlier failure was not archived under the later candidate: %+v", rejected)
			}
			status, value, expectedHead := "completed", "4", second.Commit
			if outcome == "iteration-limit" {
				status, value, expectedHead = "needs_human", "2", first.Commit
				if second.Commit != "" || failedEarlierChecks != 2 {
					t.Fatalf("unrepaired later solution was adopted: %+v checks=%d", second, failedEarlierChecks)
				}
			} else if second.Commit == "" || st.LastError != "" {
				t.Fatalf("repaired later solution was not adopted: %+v error=%s", second, st.LastError)
			}
			if second.Status != status || st.SolutionBatch.Status != status || gitTest(t, st.Worktree, "rev-parse", "HEAD") != expectedHead {
				t.Fatalf("batch accepted wrong candidate: first=%+v second=%+v batch=%s", first, second, st.SolutionBatch.Status)
			}
			data, err := os.ReadFile(filepath.Join(st.Worktree, "Demo/A.vb"))
			if err != nil || !strings.Contains(string(data), "= "+value) {
				t.Fatalf("accepted content: %s %v; expected value %s", data, err, value)
			}
			for _, task := range st.Tasks {
				if task.File != "Demo/A.vb" {
					continue
				}
				expectedAttempts := 2
				if outcome == "iteration-limit" {
					expectedAttempts = 1
				}
				if len(task.History) != expectedAttempts {
					t.Fatalf("failed shared candidate entered accepted file history: %+v", task.History)
				}
			}
		})
	}
}

func TestSolutionBatchContinuesAfterNeedsHuman(t *testing.T) {
	s, _ := solutionBatchFixture(t, []string{"Demo.sln", "Second.sln"})
	visited := []string{}
	s.buildSolution = func(context.Context, string, model.SolutionSettings) (model.SolutionBuild, error) {
		return model.SolutionBuild{Success: true}, nil
	}
	s.proposeSolution = func(_ context.Context, in agent.SolutionInput) (agent.SolutionProposal, error) {
		visited = append(visited, in.Config.Solution.Path)
		if in.Config.Solution.Path == "Demo.sln" {
			return agent.SolutionProposal{Status: "needs_human", Note: "仕様の確認が必要", Usage: model.Usage{InputTokens: 7}}, nil
		}
		return agent.SolutionProposal{Status: "skipped", Note: "全ルール確認済み", Usage: model.Usage{InputTokens: 11}}, nil
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	st := s.Snapshot()
	if st.SolutionBatch == nil || st.SolutionBatch.Status != "needs_human" || !reflect.DeepEqual(visited, []string{"Demo.sln", "Second.sln"}) {
		t.Fatalf("held solution prevented later processing: visited=%v batch=%+v error=%s", visited, st.SolutionBatch, st.LastError)
	}
	if st.SolutionBatch.Runs[0].Status != "needs_human" || st.SolutionBatch.Runs[1].Status != "completed" || st.Usage.InputTokens != 18 {
		t.Fatalf("held child outcomes/accounting: %+v %+v", st.SolutionBatch, st.Usage)
	}
	archive, err := s.GetExecutionRun(st.SolutionBatch.ID)
	if err != nil || archive.State.SolutionBatch == nil || archive.State.SolutionBatch.Status != "needs_human" || archive.State.Usage.InputTokens != 18 {
		t.Fatalf("held batch archive: %+v %v", archive, err)
	}
}

func TestSolutionBatchFailureOrStopLeavesRemainingNotRun(t *testing.T) {
	for _, kind := range []string{"failed", "stopped"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := solutionBatchFixture(t, []string{"Demo.sln", "Second.sln", "Third.sln"})
			started := make(chan struct{}, 1)
			visited := []string{}
			s.buildSolution = func(ctx context.Context, _ string, settings model.SolutionSettings) (model.SolutionBuild, error) {
				visited = append(visited, settings.Path)
				started <- struct{}{}
				if kind == "stopped" {
					<-ctx.Done()
					return model.SolutionBuild{}, ctx.Err()
				}
				return model.SolutionBuild{}, errors.New("MSBuild cannot be launched")
			}
			s.proposeSolution = func(context.Context, agent.SolutionInput) (agent.SolutionProposal, error) {
				return agent.SolutionProposal{}, errors.New("editor must not run after a failed build launch")
			}
			if err := s.Start(0); err != nil {
				t.Fatal(err)
			}
			select {
			case <-started:
			case <-time.After(15 * time.Second):
				t.Fatal("batch did not start its first build")
			}
			if kind == "stopped" {
				s.Stop()
			}
			s.Wait()
			st := s.Snapshot()
			if st.SolutionBatch == nil || st.SolutionBatch.Status != kind || !reflect.DeepEqual(visited, []string{"Demo.sln"}) || st.LastError == "" {
				t.Fatalf("batch termination: visited=%v batch=%+v error=%s", visited, st.SolutionBatch, st.LastError)
			}
			for i, run := range st.SolutionBatch.Runs {
				status := "not_run"
				if i == 0 {
					status = kind
				}
				if run.Status != status || run.Commit != "" || (i > 0 && (len(run.Builds) != 0 || run.Usage.InputTokens != 0)) {
					t.Fatalf("unexpected remaining child state: %+v", run)
				}
			}
		})
	}
}
