package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"onebyone/internal/agent"
	"onebyone/internal/catalog"
	"onebyone/internal/model"
)

// One execution has one mutation owner. Workers wait for acknowledgement before
// advancing their journals; cancellation never drops already-enqueued records.
type executionEvent struct {
	apply func() error
	done  chan error
}

type executionWriter struct {
	events        chan executionEvent
	done          chan struct{}
	referenceHead string
	failure       error  // an uncertain write/persistence failure prevents later adoption
	acceptedHead  string // only the event consumer accesses this after startup
}

func newExecutionWriter(head string) *executionWriter {
	w := &executionWriter{events: make(chan executionEvent), done: make(chan struct{}), referenceHead: head, acceptedHead: head}
	go func() {
		defer close(w.done)
		for event := range w.events {
			err := event.apply()
			if err != nil && w.failure == nil {
				w.failure = err
			}
			event.done <- err
		}
	}()
	return w
}

func (w *executionWriter) apply(fn func() error) error {
	event := executionEvent{apply: fn, done: make(chan error, 1)}
	w.events <- event
	return <-event.done
}

func (w *executionWriter) close() { close(w.events); <-w.done }

func (s *Service) runFiles(parent context.Context, cfg model.Config, cat *catalog.Catalog, limit int) error {
	if cfg.EffectiveConcurrency() < 1 || cfg.EffectiveConcurrency() > model.MaxConcurrency {
		return fmt.Errorf("並列数は1〜10です")
	}
	s.mu.Lock()
	worktree := s.state.Worktree
	indices := []int{}
	for i, task := range s.state.Tasks {
		if s.executionTargetsLocked(task.File) && !task.Excluded && (task.Status == "pending" || task.Status == "failed") {
			indices = append(indices, i)
			if limit > 0 && len(indices) >= limit {
				break
			}
		}
	}
	s.mu.Unlock()
	head, err := git(parent, worktree, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	writer := newExecutionWriter(trim(head))
	defer writer.close()
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	jobs := make(chan int, len(indices))
	for _, index := range indices {
		jobs <- index
	}
	close(jobs)
	var wg sync.WaitGroup
	var resultMu sync.Mutex
	var firstErr error
	processed, visited := 0, 0
	for worker := 0; worker < min(cfg.EffectiveConcurrency(), len(indices)); worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				if ctx.Err() != nil {
					return
				}
				resultMu.Lock()
				visited++
				resultMu.Unlock()
				// Each file owns its budget and retry counter; no shared mutable map.
				budgets := map[string]agent.ExecutionBudgetBaseline{}
				for attempts := 0; ctx.Err() == nil; attempts++ {
					if cfg.MaxAttempts > 0 && attempts >= cfg.MaxAttempts {
						failure := writer.apply(func() error {
							s.mu.Lock()
							task := &s.state.Tasks[index]
							task.Status, task.UpdatedAt = "needs_human", now()
							task.Note = "今回の実行で最大試行回数に到達しました: " + task.Note
							s.recountLocked()
							s.mu.Unlock()
							return s.persist()
						})
						if failure != nil {
							resultMu.Lock()
							if firstErr == nil {
								firstErr = failure
							}
							resultMu.Unlock()
							cancel()
						}
						break
					}
					failure := s.processOne(ctx, index, cfg, cat, budgets, writer)
					resultMu.Lock()
					processed++
					if failure != nil && firstErr == nil {
						firstErr = failure
					}
					resultMu.Unlock()
					if failure != nil {
						cancel()
						return
					}
					s.mu.Lock()
					status := s.state.Tasks[index].Status
					s.mu.Unlock()
					if status != "pending" && status != "failed" {
						break
					}
				}
			}
		}()
	}
	wg.Wait()
	if err := writer.apply(s.persist); err != nil && firstErr == nil {
		firstErr = err
	}
	s.log("info", fmt.Sprintf("今回の処理を終了しました（%dファイル / %d試行・並列数%d）。結果・差分を確認できます", visited, processed, cfg.EffectiveConcurrency()))
	return firstErr
}

func (s *Service) filePhase(file, phase string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.state.FilePhases == nil {
		s.state.FilePhases = map[string]string{}
	}
	if phase == "" {
		delete(s.state.FilePhases, file)
	} else {
		s.state.FilePhases[file] = phase
	}
	s.state.CurrentFiles = make([]string, 0, len(s.state.FilePhases))
	for file := range s.state.FilePhases {
		s.state.CurrentFiles = append(s.state.CurrentFiles, file)
	}
	sort.Strings(s.state.CurrentFiles)
	s.state.CurrentFile = ""
	if len(s.state.CurrentFiles) > 0 {
		s.state.CurrentFile = s.state.CurrentFiles[0]
		s.state.Phase = s.state.FilePhases[s.state.CurrentFile]
	}
}

// Read immutable Git blobs, never a file being adopted by another worker.
// ls-tree rejects symlinks/submodules and bounds reads before loading the blob.
func readSnapshotFile(ctx context.Context, repo, head, path string, maxBytes int) ([]byte, error) {
	if !isImmutableCommitID(head) {
		return nil, fmt.Errorf("参照元のコミットが不正です")
	}
	if err := validatePreviewFilePath(path); err != nil {
		return nil, err
	}
	if forbiddenContext(path) {
		return nil, fmt.Errorf("エージェント設定・認証ファイルは参照できません")
	}
	listing, err := git(ctx, repo, "ls-tree", "-z", head, "--", path)
	if err != nil {
		return nil, err
	}
	entries := zeroLines(listing)
	if len(entries) != 1 {
		return nil, fmt.Errorf("Gitに登録された通常ファイルではありません: %s", path)
	}
	parts := strings.SplitN(entries[0], "\t", 2)
	fields := strings.Fields(parts[0])
	if len(parts) != 2 || parts[1] != path || len(fields) != 3 || (fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
		return nil, fmt.Errorf("参照対象は通常ファイルである必要があります: %s", path)
	}
	size, err := git(ctx, repo, "cat-file", "-s", fields[2])
	if err != nil {
		return nil, err
	}
	var count int64
	if _, err := fmt.Sscan(size, &count); err != nil || count < 0 || count > int64(maxBytes) {
		return nil, fmt.Errorf("参照ファイルが大きすぎます")
	}
	content, err := git(ctx, repo, "cat-file", "blob", fields[2])
	if err != nil {
		return nil, err
	}
	if len(content) > maxBytes {
		return nil, fmt.Errorf("参照ファイルが大きすぎます")
	}
	return []byte(content), nil
}

func readSnapshotContext(ctx context.Context, repo, head, sourceRelative, path string, start, end, maxBytes int) (string, error) {
	if err := validatePreviewFilePath(path); err != nil {
		return "", err
	}
	if start < 1 || end < start || end-start >= 200 {
		return "", fmt.Errorf("行番号は1以上、一度に200行までです")
	}
	relative := filepath.ToSlash(filepath.Join(sourceRelative, path))
	b, err := readSnapshotFile(ctx, repo, head, relative, maxBytes)
	if err != nil {
		return "", err
	}
	source, err := decodeSource(b)
	if err != nil {
		return "", err
	}
	lines := strings.Split(source.text, "\n")
	if start > len(lines) {
		return "", fmt.Errorf("指定行がファイル範囲外です")
	}
	out := strings.Join(lines[start-1:min(end, len(lines))], "\n")
	if len(out) > 64<<10 {
		return "", fmt.Errorf("参照範囲を狭めてください（上限64KB）")
	}
	return out, nil
}
