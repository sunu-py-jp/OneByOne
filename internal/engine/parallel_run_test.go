package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func parallelReviewedProposal(ctx context.Context, in agent.Input) (model.Proposal, error) {
	return parallelReviewedEdit(ctx, in, "Legacy.Save()", "Modern.Save()")
}

func parallelReviewedEdit(ctx context.Context, in agent.Input, oldText, newText string) (model.Proposal, error) {
	state := *in.RepairState
	state.Plan = model.RepairPlan{Revision: 1, RuleDecisions: []model.PlanDecision{{RuleID: "R001", Decision: "no_change", Reason: "Preserve behavior"}, {RuleID: "R019", Decision: "modify", Reason: "Update API"}}, Items: []model.PlanItem{{ID: "P1", RuleID: "R019", Location: "call", Risk: "old API", Change: "Use Modern.Save", Expected: "supported API", Status: "proposed"}}}
	if err := in.SaveRepairState(state); err != nil {
		return model.Proposal{}, err
	}
	request := model.CandidateRequest{PlanRevision: 1, BaseHash: in.BaseHash, AddressedItemIDs: []string{"P1"}, Edits: []model.Edit{{OldText: oldText, NewText: newText, ItemIDs: []string{"P1"}, Attributions: []model.EditAttribution{{ItemID: "P1", BeforeText: oldText, AfterText: newText}}}}}
	result, err := in.ValidateCandidate(ctx, request)
	if err != nil {
		return model.Proposal{}, err
	}
	if !result.Passed {
		return model.Proposal{}, fmt.Errorf("candidate failed: %+v", result.Checks)
	}
	review := independentReviewAcceptedFixture(result, in.BaseHash)
	state.LastCandidate = &model.CandidateRecord{Request: request, Result: result, Review: &review}
	state.Reviews = []model.IndependentReview{review}
	if err := in.SaveRepairState(state); err != nil {
		return model.Proposal{}, err
	}
	return model.Proposal{Outcome: "modified", CandidateID: result.CandidateID, Edits: request.Edits, RulesApplied: []string{"R019"}, Note: "Updated API"}, nil
}

func waitParallelSignal(t *testing.T, signal <-chan string) string {
	t.Helper()
	select {
	case value := <-signal:
		return value
	case <-time.After(15 * time.Second):
		t.Fatal("parallel worker did not reach barrier")
		return ""
	}
}

func TestParallelRunReviewedCandidatesShareOneWorktreeAndFixedContext(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n", "C.txt": "Legacy.Save()\n"})
	cfg.Concurrency = 2
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	entered := make(chan string, 3)
	releaseA, releaseB := make(chan struct{}), make(chan struct{})
	var active, peak atomic.Int32
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
		}
		entered <- in.File
		switch in.File {
		case "A.txt":
			select {
			case <-releaseA:
			case <-ctx.Done():
				return model.Proposal{}, ctx.Err()
			}
		case "B.txt":
			select {
			case <-releaseB:
			case <-ctx.Done():
				return model.Proposal{}, ctx.Err()
			}
		case "C.txt":
			// A has already committed, but later jobs still read the execution baseline.
			content, err := in.ReadContext("A.txt", 1, 1)
			if err != nil || content != "Legacy.Save()" {
				return model.Proposal{}, fmt.Errorf("mutable context: %q %v", content, err)
			}
		}
		return parallelReviewedProposal(ctx, in)
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	first, second := waitParallelSignal(t, entered), waitParallelSignal(t, entered)
	if first == second || peak.Load() != 2 {
		t.Fatalf("workers did not overlap: %s %s peak=%d", first, second, peak.Load())
	}
	st := s.Snapshot()
	if len(st.CurrentFiles) != 2 || len(st.FilePhases) != 2 {
		t.Fatalf("missing active files: %+v", st.CurrentFiles)
	}
	close(releaseA)
	if got := waitParallelSignal(t, entered); got != "C.txt" {
		t.Fatalf("next job=%q", got)
	}
	close(releaseB)
	s.Wait()
	st = s.Snapshot()
	if st.LastError != "" || peak.Load() > 2 || len(st.CurrentFiles) != 0 {
		t.Fatalf("run failed: error=%s peak=%d active=%v", st.LastError, peak.Load(), st.CurrentFiles)
	}
	var base string
	parents := map[string]bool{}
	for _, task := range st.Tasks {
		if task.Status != "done" || len(task.History) != 1 {
			t.Fatalf("task %+v", task)
		}
		h := task.History[0]
		if base == "" {
			base = h.BaseCommit
		}
		if h.BaseCommit != base {
			t.Fatal("execution reference changed")
		}
		if h.CommitBase == "" || gitTest(t, st.Worktree, "rev-parse", h.Commit+"^") != h.CommitBase {
			t.Fatalf("wrong adoption parent: %+v", h)
		}
		parents[h.CommitBase] = true
		if readTest(t, filepath.Join(cfg.Root, task.File)) != "Legacy.Save()\n" || readTest(t, filepath.Join(st.Worktree, task.File)) != "Modern.Save()\n" {
			t.Fatal("wrong adopted/original bytes")
		}
	}
	if len(parents) != 3 || gitTest(t, st.Worktree, "rev-list", "--count", base+"..HEAD") != "3" {
		t.Fatal("adoptions were not serialized")
	}
	if got := gitTest(t, st.Worktree, "status", "--porcelain"); got != "" {
		t.Fatal(got)
	}
	stored, err := store.LoadQueue(cfg.QueuePath)
	if err != nil || len(stored) != 3 {
		t.Fatal(err)
	}
	for _, task := range stored {
		if task.Status != "done" {
			t.Fatalf("lost saved result: %+v", task)
		}
	}
	if len(st.ExecutionRuns) != 1 || st.ExecutionRuns[0].Concurrency != 2 {
		t.Fatal("execution did not record concurrency")
	}
}

func TestParallelStopDrainsEventsAndLeavesUnstartedFilesPending(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n", "C.txt": "Legacy.Save()\n"})
	cfg.Concurrency = 2
	if _, err := s.SaveConfig(cfg); err != nil {
		t.Fatal(err)
	}
	entered := make(chan string, 3)
	s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
		entered <- in.File
		<-ctx.Done()
		return model.Proposal{}, ctx.Err()
	}
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	waitParallelSignal(t, entered)
	waitParallelSignal(t, entered)
	s.Stop()
	s.Wait()
	st := s.Snapshot()
	held, pending := 0, 0
	for _, task := range st.Tasks {
		switch task.Status {
		case "needs_human":
			held++
		case "pending":
			pending++
		default:
			t.Fatalf("unexpected status: %s", task.Status)
		}
	}
	if held != 2 || pending != 1 || len(st.CurrentFiles) != 0 {
		t.Fatalf("held=%d pending=%d active=%v", held, pending, st.CurrentFiles)
	}
	if got := gitTest(t, st.Worktree, "status", "--porcelain"); got != "" {
		t.Fatal(got)
	}
	stored, err := store.LoadQueue(cfg.QueuePath)
	if err != nil || len(stored) != 3 {
		t.Fatal(err)
	}
	for _, task := range stored {
		if task.Status == "running" {
			t.Fatal("stop left unsaved running task")
		}
	}
}

func TestRollbackAdoptionPreservesUnknownOtherFiles(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	if err := s.prepareWorktree(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	worktree := s.Snapshot().Worktree
	head := gitTest(t, worktree, "rev-parse", "HEAD")
	writeTest(t, filepath.Join(worktree, "A.txt"), []byte("Modern.Save()\n"))
	writeTest(t, filepath.Join(worktree, "B.txt"), []byte("external edit\n"))
	writeTest(t, filepath.Join(worktree, "new.txt"), []byte("external new file\n"))
	err := rollbackAdoption(context.Background(), worktree, "A.txt", head, digest([]byte("Legacy.Save()\n")), digest([]byte("Modern.Save()\n")))
	if err != nil {
		t.Fatal(err)
	}
	if readTest(t, filepath.Join(worktree, "A.txt")) != "Legacy.Save()\n" || readTest(t, filepath.Join(worktree, "B.txt")) != "external edit\n" || readTest(t, filepath.Join(worktree, "new.txt")) != "external new file\n" {
		t.Fatal("rollback lost external changes")
	}
}

func TestParallelRecoveryCommittedParentDiffersFromReference(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	s.propose = parallelReviewedProposal
	st := runTest(t, s, 0)
	if st.LastError != "" {
		t.Fatal(st.LastError)
	}
	head := gitTest(t, st.Worktree, "rev-parse", "HEAD")
	s.mu.Lock()
	for i := range s.state.Tasks {
		h := &s.state.Tasks[i].History[0]
		if h.Commit == head {
			if h.BaseCommit == h.CommitBase {
				t.Error("test requires advanced parent")
			}
			h.Commit = ""
			h.Outcome = "validated"
			h.FinishedAt = ""
			s.state.Tasks[i].Status = "running"
		}
	}
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, task := range s.Snapshot().Tasks {
		if task.Status != "done" || task.History[0].Commit == "" {
			t.Fatalf("bad recovery: %+v", task)
		}
	}
}

func TestParallelRunHonorsOneAndTenWorkers(t *testing.T) {
	for _, concurrency := range []int{1, 10} {
		t.Run(fmt.Sprint(concurrency), func(t *testing.T) {
			sources := map[string]string{}
			for i := 0; i < concurrency+2; i++ {
				sources[fmt.Sprintf("%02d.txt", i)] = "Legacy.Save()\n"
			}
			s, cfg := fixture(t, sources)
			cfg.Concurrency = concurrency
			if _, err := s.SaveConfig(cfg); err != nil {
				t.Fatal(err)
			}
			entered := make(chan string, len(sources))
			release := make(chan struct{})
			var active, peak atomic.Int32
			s.propose = func(ctx context.Context, in agent.Input) (model.Proposal, error) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				entered <- in.File
				select {
				case <-release:
				case <-ctx.Done():
					return model.Proposal{}, ctx.Err()
				}
				return model.Proposal{Outcome: "needs_human", Note: "Requires external contract"}, nil
			}
			if err := s.Start(0); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < concurrency; i++ {
				waitParallelSignal(t, entered)
			}
			if n := len(s.Snapshot().CurrentFiles); n != concurrency {
				t.Fatalf("active=%d expected=%d", n, concurrency)
			}
			close(release)
			s.Wait()
			if peak.Load() != int32(concurrency) {
				t.Fatalf("peak=%d", peak.Load())
			}
			st := s.Snapshot()
			if st.LastError != "" {
				t.Fatal(st.LastError)
			}
			for _, task := range st.Tasks {
				if task.Status != "needs_human" || task.Attempts != 1 {
					t.Fatalf("lost/duplicate task %+v", task)
				}
			}
		})
	}
}

func TestParallelRecoveryKeepsAdoptedFileAndRecoversAllInterruptedWorkers(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n", "C.txt": "Legacy.Save()\n"})
	ctx := context.Background()
	if err := s.prepareWorktree(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	worktree := s.Snapshot().Worktree
	base := gitTest(t, worktree, "rev-parse", "HEAD")
	before, after := []byte("Legacy.Save()\n"), []byte("Modern.Save()\n")
	writeTest(t, filepath.Join(worktree, "A.txt"), after)
	gitTest(t, worktree, "add", "A.txt")
	gitTest(t, worktree, "commit", "-qm", "adopt A")
	head := gitTest(t, worktree, "rev-parse", "HEAD")
	s.mu.Lock()
	for i := range s.state.Tasks {
		task := &s.state.Tasks[i]
		task.Status = "running"
		task.Attempts = 1
		h := model.Attempt{ID: uid(), Number: 1, BaseCommit: base, InputHash: digest(before)}
		switch task.File {
		case "A.txt":
			task.Status = "done"
			h.Outcome = "done"
			h.CommitBase = base
			h.Commit = head
			h.OutputHash = digest(after)
		case "C.txt":
			h.CommitBase = head
			h.OutputHash = digest(after)
			h.Outcome = "validated"
		}
		task.History = []model.Attempt{h}
	}
	s.mu.Unlock()
	writeTest(t, filepath.Join(worktree, "C.txt"), after)
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	if err := s.recover(ctx); err != nil {
		t.Fatal(err)
	}
	st := s.Snapshot()
	for _, task := range st.Tasks {
		if task.File == "A.txt" {
			if task.Status != "done" {
				t.Fatal("adopted task lost")
			}
			continue
		}
		if task.Status != "needs_human" || task.History[0].Outcome != "interrupted" {
			t.Fatalf("reader not recovered: %+v", task)
		}
		if got := readTest(t, filepath.Join(worktree, task.File)); got != string(before) {
			t.Fatalf("wrong recovered bytes %s: %q", task.File, got)
		}
	}
	if got := gitTest(t, worktree, "rev-parse", "HEAD"); got != head {
		t.Fatal("recovery dropped adopted commit")
	}
	if got := readTest(t, filepath.Join(worktree, "A.txt")); got != string(after) {
		t.Fatal("recovery lost adopted result")
	}
}

func TestConcurrencyChangeKeepsQueueAndSurvivesSessionReload(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	if st.LastError != "" {
		t.Fatal(st.LastError)
	}
	before := repairSettingsHash(st.Config)
	cfg = st.Config
	cfg.Concurrency = 10
	updated, err := s.SaveConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if repairSettingsHash(updated.Config) != before {
		t.Fatal("concurrency invalidated the repair plan")
	}
	if err := s.load(cfg.QueuePath); err != nil {
		t.Fatal(err)
	}
	reloaded := s.Snapshot()
	if reloaded.Config.Concurrency != 10 || len(reloaded.Tasks) != 1 || reloaded.Tasks[0].Status != "done" {
		t.Fatal("session replaced saved concurrency or results")
	}
	for _, invalid := range []int{-1, 11} {
		cfg.Concurrency = invalid
		if _, err := s.SaveConfig(cfg); err == nil {
			t.Fatalf("accepted invalid concurrency %d", invalid)
		}
	}
}
