package engine

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func TestDiscardChangesIsolatedHistoryAndFreshRetry(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n", "src/B.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "done" || !st.Tasks[0].CanDiscardChanges {
		t.Fatalf("initial run: %+v", st)
	}
	if _, err := s.RetryTasks([]string{"src/A.txt"}); err != nil {
		t.Fatal(err)
	}
	s.propose = func(context.Context, agent.Input) (model.Proposal, error) {
		return model.Proposal{Outcome: "skipped", Note: "already updated", RulesApplied: []string{"R001", "R019"}}, nil
	}
	st = runTest(t, s, 0)
	if st.LastError != "" || st.Tasks[0].Status != "done" || st.Tasks[0].History[1].Outcome != "skipped" || !st.Tasks[0].CanDiscardChanges {
		t.Fatalf("done followed by skipped must retain completion and discardability: %+v", st.Tasks[0])
	}
	history, usage, other := st.Tasks[0].History, st.Usage, st.Tasks[1]
	original := gitTest(t, cfg.Root, "rev-parse", "HEAD")
	st, err := s.DiscardFileChanges("src/A.txt")
	if err != nil {
		t.Fatal(err)
	}
	a := st.Tasks[0]
	if a.Status != "pending" || a.CanDiscardChanges || !a.ResumeRequested || len(a.RulesApplied) != 0 || !strings.Contains(a.Note, "再試行") || a.Attempts != 2 || len(a.Discards) != 1 || a.Discards[0].State != "done" {
		t.Fatalf("discard state: %+v", a)
	}
	if !reflect.DeepEqual(a.History, history) || !reflect.DeepEqual(st.Tasks[1], other) || st.Usage != usage {
		t.Fatal("discard changed another task, attempt evidence, or cumulative fees")
	}
	if readTest(t, filepath.Join(st.Worktree, "src/A.txt")) != "Legacy.Save()\n" || readTest(t, filepath.Join(st.Worktree, "src/B.txt")) != "Modern.Save()\n" || gitTest(t, cfg.Root, "rev-parse", "HEAD") != original {
		t.Fatal("discard touched another file or the original source")
	}
	if got := gitTest(t, st.Worktree, "diff-tree", "--no-commit-id", "--name-only", "-r", "HEAD"); got != "src/A.txt" {
		t.Fatalf("compensating commit has wrong scope: %s", got)
	}
	if gitTest(t, st.Worktree, "rev-list", "--count", original+"..HEAD") != "3" {
		t.Fatal("discard must retain both previous commits")
	}
	if _, err = s.DiscardFileChanges("src/A.txt"); err == nil {
		t.Fatal("already discarded file accepted")
	}
	if st, err = s.Scan(); err != nil || len(st.Tasks[0].Discards) != 1 || st.Tasks[0].CanDiscardChanges {
		t.Fatalf("scan lost discard: %v %+v", err, st.Tasks)
	}
	s.Close()
	s2 := New(s.configPath)
	t.Cleanup(s2.Close)
	st = s2.Snapshot()
	if st.LastError != "" || st.Tasks[0].Status != "pending" || st.Tasks[0].CanDiscardChanges || len(st.Tasks[0].Discards) != 1 || !reflect.DeepEqual(st.Tasks[0].History, history) {
		t.Fatalf("restart lost discard: %+v", st)
	}
	if _, err = s2.RetryTasks([]string{"src/A.txt"}); err != nil {
		t.Fatalf("discard commit not recognized by recovery: %v", err)
	}
	called := false
	s2.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		called = true
		if in.File != "src/A.txt" || in.Content != "Legacy.Save()\n" || in.PreviousFailure != "" || in.RepairState == nil || in.RepairState.Plan.Revision != 0 || in.RepairState.LastCandidate != nil || len(in.RepairState.ReadRuleIDs) != 0 {
			t.Fatalf("discarded context was reused: %+v", in)
		}
		if in.RepairState.Usage.CostUSD != sumUsage(history).CostUSD {
			t.Fatal("fresh plan dropped cumulative usage")
		}
		return successfulProposal(ctx, in)
	}
	st = runTest(t, s2, 0)
	if !called || st.LastError != "" || st.Tasks[0].Status != "done" || !st.Tasks[0].CanDiscardChanges || st.Tasks[0].Attempts != 3 {
		t.Fatalf("fresh execution failed: %+v", st)
	}
	if _, err = s2.DiscardFileChanges("src/A.txt"); err != nil {
		t.Fatalf("second discard failed: %v", err)
	}
}

func prepareDiscardTest(t *testing.T, s *Service, cfg model.Config) model.DiscardChange {
	t.Helper()
	s.mu.Lock()
	tasks, m := copyTasks(s.state.Tasks), s.meta
	s.mu.Unlock()
	before := []byte(readTest(t, filepath.Join(m.Worktree, tasks[0].File)))
	rel := filepath.ToSlash(filepath.Join(m.SourceRelative, tasks[0].File))
	after, _, err := discardBaseline(context.Background(), m, rel)
	if err != nil {
		t.Fatal(err)
	}
	d := model.DiscardChange{ID: uid(), State: "prepared", StartedAt: now(), BaseCommit: gitTest(t, m.Worktree, "rev-parse", "HEAD"), RestoreCommit: m.BaseCommit, InputHash: digest(before), OutputHash: digest(after), ThroughAttempt: tasks[0].Attempts}
	tasks[0].Discards = append(tasks[0].Discards, d)
	if err = s.publishDiscardQueue(cfg, tasks); err != nil {
		t.Fatal(err)
	}
	return d
}

func TestDiscardRecoveryAcrossPublicationBoundaries(t *testing.T) {
	for _, boundary := range []string{"prepared", "restored", "staged", "committed", "canceled"} {
		t.Run(boundary, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n"})
			s.propose = successfulProposal
			st := runTest(t, s, 0)
			if st.LastError != "" || st.Tasks[0].Status != "done" {
				t.Fatalf("initial run: %+v", st)
			}
			d := prepareDiscardTest(t, s, cfg)
			if boundary == "restored" || boundary == "staged" || boundary == "committed" {
				writeTest(t, filepath.Join(st.Worktree, "src/A.txt"), []byte("Legacy.Save()\n"))
			}
			if boundary == "staged" || boundary == "committed" {
				gitTest(t, st.Worktree, "add", "--", "src/A.txt")
			}
			if boundary == "committed" {
				gitTest(t, st.Worktree, "commit", "-m", "OneByOne: discard src/A.txt\n\nOneByOne-Discard: "+d.ID)
			}
			if boundary == "canceled" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := s.recoverDiscards(ctx); err == nil {
					t.Fatal("canceled recovery succeeded")
				}
			}
			s.Close()
			s2 := New(s.configPath)
			t.Cleanup(s2.Close)
			restored := s2.Snapshot()
			if restored.LastError != "" || restored.Tasks[0].Status != "pending" || restored.Tasks[0].Discards[0].State != "done" || restored.Tasks[0].CanDiscardChanges {
				t.Fatalf("startup did not reconcile %s: %+v", boundary, restored)
			}
			if readTest(t, filepath.Join(st.Worktree, "src/A.txt")) != "Legacy.Save()\n" || gitTest(t, st.Worktree, "status", "--porcelain") != "" || gitTest(t, st.Worktree, "rev-list", "--count", s2.meta.BaseCommit+"..HEAD") != "2" {
				t.Fatal("recovery duplicated a commit or failed to restore exactly one file")
			}
			if err := s2.recover(context.Background()); err != nil {
				t.Fatalf("normal recovery rejected discard commit: %v", err)
			}
			queue, err := store.LoadQueue(cfg.QueuePath)
			if err != nil || queue[0].Status != "pending" || queue[0].Discards[0].Commit == "" || len(queue[0].History) != 1 {
				t.Fatalf("recovered queue not durable: %+v %v", queue, err)
			}
		})
	}
}

func TestDiscardRefusesUnsafeStateWithoutChangingFiles(t *testing.T) {
	for _, condition := range []string{"unknown", "readonly", "running", "dirty-target", "dirty-other", "staged-other", "untracked", "external-commit", "external-reset", "symlink", "wrong-worktree"} {
		t.Run(condition, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n", "src/B.txt": "Legacy.Save()\n"})
			s.propose = successfulProposal
			st := runTest(t, s, 0)
			if st.LastError != "" || st.Tasks[0].Status != "done" {
				t.Fatalf("initial run: %+v", st)
			}
			file := "src/A.txt"
			switch condition {
			case "unknown":
				file = "../A.txt"
			case "readonly":
				s.state.ReadOnly = true
			case "running":
				s.state.Running = true
			case "dirty-target":
				writeTest(t, filepath.Join(st.Worktree, "src/A.txt"), []byte("user changes\n"))
			case "dirty-other", "staged-other":
				writeTest(t, filepath.Join(st.Worktree, "src/B.txt"), []byte("user changes\n"))
				if condition == "staged-other" {
					gitTest(t, st.Worktree, "add", "--", "src/B.txt")
					writeTest(t, filepath.Join(st.Worktree, "src/B.txt"), []byte("Modern.Save()\n"))
				}
			case "untracked":
				writeTest(t, filepath.Join(st.Worktree, "notes.txt"), []byte("user notes\n"))
			case "external-commit":
				gitTest(t, st.Worktree, "commit", "--allow-empty", "-m", "external")
			case "external-reset":
				gitTest(t, st.Worktree, "reset", "--hard", "HEAD^")
			case "symlink":
				external := filepath.Join(t.TempDir(), "external.txt")
				writeTest(t, external, []byte("do not modify\n"))
				if err := os.Remove(filepath.Join(st.Worktree, file)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(external, filepath.Join(st.Worktree, file)); err != nil {
					t.Skip(err)
				}
			case "wrong-worktree":
				s.meta.Worktree = cfg.Root
			}
			queue := readTest(t, cfg.QueuePath)
			head := gitTest(t, st.Worktree, "rev-parse", "HEAD")
			status := gitTest(t, st.Worktree, "status", "--porcelain")
			content := readTest(t, filepath.Join(st.Worktree, "src/A.txt"))
			if _, err := s.DiscardFileChanges(file); err == nil {
				t.Fatal("unsafe discard accepted")
			}
			s.state.Running = false
			if head != gitTest(t, st.Worktree, "rev-parse", "HEAD") || status != gitTest(t, st.Worktree, "status", "--porcelain") || content != readTest(t, filepath.Join(st.Worktree, "src/A.txt")) || queue != readTest(t, cfg.QueuePath) {
				t.Fatal("refused discard still changed files, history, or queue")
			}
		})
	}
}

func TestDiscardRecoveryRefusesUserEdits(t *testing.T) {
	for _, change := range []string{"target", "other", "staged-target", "staged-other", "external-commit"} {
		t.Run(change, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n", "src/B.txt": "Legacy.Save()\n"})
			s.propose = successfulProposal
			st := runTest(t, s, 0)
			prepareDiscardTest(t, s, cfg)
			file := "src/A.txt"
			if strings.Contains(change, "other") {
				file = "src/B.txt"
			}
			if change == "external-commit" {
				gitTest(t, st.Worktree, "commit", "--allow-empty", "-m", "external")
			} else {
				writeTest(t, filepath.Join(st.Worktree, file), []byte("user edit\n"))
				if strings.HasPrefix(change, "staged-") {
					gitTest(t, st.Worktree, "add", "--", file)
					writeTest(t, filepath.Join(st.Worktree, file), []byte("Modern.Save()\n"))
				}
			}
			head, content := gitTest(t, st.Worktree, "rev-parse", "HEAD"), readTest(t, filepath.Join(st.Worktree, file))
			status := gitTest(t, st.Worktree, "status", "--porcelain")
			if err := s.recoverDiscards(context.Background()); err == nil {
				t.Fatal("recovery overwrote an external edit")
			}
			if head != gitTest(t, st.Worktree, "rev-parse", "HEAD") || content != readTest(t, filepath.Join(st.Worktree, file)) || status != gitTest(t, st.Worktree, "status", "--porcelain") {
				t.Fatal("recovery refusal changed user content")
			}
		})
	}
}

func TestDiscardExplicitRequeueRetainsUncertainUsagePolicy(t *testing.T) {
	for _, withCap := range []bool{false, true} {
		t.Run(map[bool]string{false: "without-cap", true: "with-cap"}[withCap], func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n"})
			s.propose = successfulProposal
			st := runTest(t, s, 0)
			if st.LastError != "" || st.Tasks[0].Status != "done" {
				t.Fatalf("initial run: %+v", st)
			}
			if withCap {
				cfg = s.state.Config
				cfg.MaxCostUSD, cfg.InputPricePerMillion, cfg.OutputPricePerMillion = 100, 1, 1
				if _, err := s.SaveConfig(cfg); err != nil {
					t.Fatal(err)
				}
			}
			s.mu.Lock()
			task := &s.state.Tasks[0]
			task.Attempts++
			task.Status = "needs_human"
			task.History = append(task.History, model.Attempt{ID: uid(), Number: task.Attempts, Outcome: "needs_human", StartedAt: now(), FinishedAt: now(), BaseCommit: task.History[0].Commit, InputHash: digest([]byte("Modern.Save()\n")), Usage: model.Usage{InputTokens: 20, Turns: 1, Uncertain: true}, Note: "canceled request"})
			s.recountLocked()
			s.mu.Unlock()
			if err := s.persist(); err != nil {
				t.Fatal(err)
			}
			if _, err := s.DiscardFileChanges("src/A.txt"); err != nil {
				t.Fatal(err)
			}
			called := false
			s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
				called = true
				if !in.AllowUncertainResume || !in.RepairState.Usage.Uncertain || in.RepairState.Plan.Revision != 0 || in.PreviousFailure != "" {
					t.Fatalf("discard requeue context is incorrect: %+v", in)
				}
				return successfulProposal(ctx, in)
			}
			st = runTest(t, s, 0)
			if st.LastError != "" || !st.Usage.Uncertain {
				t.Fatalf("billing uncertainty was lost: %+v", st)
			}
			if withCap && (called || st.Tasks[0].Status != "needs_human") {
				t.Fatal("discard bypassed the uncertain billing cap guard")
			}
			if !withCap && (!called || st.Tasks[0].Status != "done") {
				t.Fatalf("explicit discard requeue should permit a new request without a fee cap: %+v", st.Tasks[0])
			}
		})
	}
}

func TestDiscardPreparedReadOnlyStartupDoesNotMutate(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	prepareDiscardTest(t, s, cfg)
	queue := readTest(t, cfg.QueuePath)
	reader := New(s.configPath)
	t.Cleanup(reader.Close)
	if !reader.Snapshot().ReadOnly || readTest(t, cfg.QueuePath) != queue || readTest(t, filepath.Join(st.Worktree, "src/A.txt")) != "Modern.Save()\n" {
		t.Fatal("read-only startup completed a pending discard")
	}
}
