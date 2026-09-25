package engine

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
	"onebyone/internal/store"
)

func discardCutoff(t model.Task) int {
	cutoff := 0
	for _, d := range t.Discards {
		if d.State == "done" && d.ThroughAttempt > cutoff {
			cutoff = d.ThroughAttempt
		}
	}
	return cutoff
}

// A discarded plan remains reportable but must never re-enter an agent context.
func repairHistory(t model.Task) []model.Attempt {
	cutoff := discardCutoff(t)
	if cutoff == 0 {
		return t.History
	}
	for i, h := range t.History {
		if h.Number > cutoff {
			return t.History[i:]
		}
	}
	return nil
}

func canDiscardChanges(t model.Task) bool {
	for _, d := range t.Discards {
		if d.State == "prepared" {
			return false
		}
	}
	baseline := ""
	for _, h := range t.History {
		if h.InputHash != "" {
			baseline = h.InputHash
			break
		}
	}
	for _, d := range t.Discards {
		if d.State == "done" {
			baseline = d.OutputHash
		}
	}
	history := repairHistory(t)
	for i := len(history) - 1; i >= 0; i-- {
		if h := history[i]; h.Commit != "" && h.Outcome == "done" {
			return h.OutputHash != baseline || baseline == ""
		}
	}
	return false
}

func hasPreparedDiscard(tasks []model.Task) bool {
	for _, t := range tasks {
		for _, d := range t.Discards {
			if d.State == "prepared" {
				return true
			}
		}
	}
	return false
}

// DiscardFileChanges compensates the selected file's adopted changes without
// rewinding Git history, deleting attempts, or touching the user's checkout.
func (s *Service) DiscardFileChanges(file string) (model.State, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.editable(); err != nil {
		return s.Snapshot(), err
	}
	s.mu.Lock()
	cfg, m, tasks := s.state.Config, s.meta, copyTasks(s.state.Tasks)
	s.mu.Unlock()
	index := -1
	for i, t := range tasks {
		if t.File == file {
			index = i
		}
	}
	if index < 0 {
		return s.Snapshot(), fmt.Errorf("対象一覧にないファイルの変更は破棄できません: %s", file)
	}
	if !canDiscardChanges(tasks[index]) {
		return s.Snapshot(), fmt.Errorf("このファイルには破棄できる採用済みの変更がありません")
	}
	unlock, err := lockSharedQueue(cfg)
	if err != nil {
		return s.Snapshot(), err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err = validateDiscardWorktree(ctx, cfg, m); err != nil {
		return s.Snapshot(), err
	}
	// Never call generic interrupted-attempt rollback here: user-created dirt
	// must be refused, including changes to another queued file.
	if err = requireCleanDiscardWorktree(ctx, m.Worktree); err != nil {
		return s.Snapshot(), err
	}
	if err = validateDiscardHistory(ctx, m, tasks, ""); err != nil {
		return s.Snapshot(), err
	}
	head, err := git(ctx, m.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return s.Snapshot(), err
	}
	path, rel, err := discardTarget(m, file)
	if err != nil {
		return s.Snapshot(), err
	}
	before, err := os.ReadFile(path)
	if err != nil {
		return s.Snapshot(), err
	}
	after, _, err := discardBaseline(ctx, m, rel)
	if err != nil {
		return s.Snapshot(), err
	}
	if digest(before) == digest(after) {
		return s.Snapshot(), fmt.Errorf("このファイルはすでに実行開始前の内容です")
	}
	d := model.DiscardChange{ID: uid(), StartedAt: now(), State: "prepared", BaseCommit: trim(head), RestoreCommit: m.BaseCommit, InputHash: digest(before), OutputHash: digest(after), ThroughAttempt: tasks[index].Attempts}
	tasks[index].Discards = append(tasks[index].Discards, d)
	// The queue is the write-ahead journal. No source write precedes this fsync.
	if err = s.publishDiscardQueue(cfg, tasks); err != nil {
		return s.Snapshot(), err
	}
	if err = s.recoverDiscards(ctx); err != nil {
		return s.Snapshot(), err
	}
	s.log("info", file+" · 変更を破棄し、実行開始前の内容に戻しました")
	return s.Snapshot(), nil
}

func discardTarget(m manifest, file string) (string, string, error) {
	rel := filepath.ToSlash(filepath.Join(m.SourceRelative, file))
	path, err := catalog.PathWithin(m.Worktree, rel)
	if err != nil {
		return "", "", err
	}
	// Validate the original task spelling separately, before Join cleans it.
	if _, err = catalog.PathWithin(filepath.Join(m.Worktree, m.SourceRelative), file); err != nil {
		return "", "", err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", "", fmt.Errorf("変更破棄の対象が通常ファイルではありません: %s", file)
	}
	return path, rel, nil
}

func validateDiscardWorktree(ctx context.Context, cfg model.Config, m manifest) error {
	if m.Worktree == "" || m.BaseCommit == "" || m.Branch == "" || !strings.HasPrefix(m.Branch, "onebyone/") || !sameRoot(m.Worktree, cfg.QueuePath+".worktree") || !sameRoot(m.Root, cfg.Root) || sameRoot(m.Worktree, m.RepoRoot) {
		return fmt.Errorf("変更を破棄する専用作業コピーの所有情報を確認できません")
	}
	if _, err := catalog.PathWithin(filepath.Dir(m.Worktree), filepath.Base(m.Worktree)); err != nil {
		return err
	}
	top, err := git(ctx, m.Worktree, "rev-parse", "--show-toplevel")
	if err != nil || !sameRoot(trim(top), m.Worktree) {
		return fmt.Errorf("専用作業コピーのパスが一致しません")
	}
	branch, err := git(ctx, m.Worktree, "branch", "--show-current")
	if err != nil || trim(branch) != m.Branch {
		return fmt.Errorf("専用作業コピーのブランチが一致しません")
	}
	common, err := git(ctx, m.Worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	original, err := git(ctx, m.RepoRoot, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil || !sameRoot(trim(common), trim(original)) {
		return fmt.Errorf("専用作業コピーが対象リポジトリに所属していません")
	}
	root, err := filepath.EvalSymlinks(cfg.Root)
	want, wantErr := filepath.EvalSymlinks(filepath.Join(m.RepoRoot, m.SourceRelative))
	if err != nil || wantErr != nil || root != want {
		return fmt.Errorf("セッションの対象フォルダが一致しません")
	}
	return nil
}

func requireCleanDiscardWorktree(ctx context.Context, worktree string) error {
	status, err := git(ctx, worktree, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("作業コピーに未コミットの変更があります。変更破棄は行いません")
	}
	return nil
}

// Require the entire known history, not merely a known tip. This also refuses
// an external reset to an older (otherwise valid) application commit.
func validateDiscardHistory(ctx context.Context, m manifest, tasks []model.Task, recoveringCommit string) error {
	if _, err := git(ctx, m.Worktree, "merge-base", "--is-ancestor", m.BaseCommit, "HEAD"); err != nil {
		return fmt.Errorf("作業コピーの履歴が実行開始時の履歴と一致しません")
	}
	known := map[string]bool{}
	for _, t := range tasks {
		for _, h := range t.History {
			if h.Commit != "" {
				known[h.Commit] = true
			}
		}
		for _, d := range t.Discards {
			if d.State == "done" && d.Commit != "" {
				known[d.Commit] = true
			}
		}
	}
	if recoveringCommit != "" {
		known[recoveringCommit] = true
	}
	log, err := git(ctx, m.Worktree, "log", "--format=%H", m.BaseCommit+"..HEAD")
	if err != nil {
		return err
	}
	for _, hash := range strings.Fields(log) {
		if !known[hash] {
			return fmt.Errorf("台帳にないコミットが作業コピーにあります: %s", hash)
		}
		delete(known, hash)
	}
	if len(known) != 0 {
		return fmt.Errorf("採用済みのコミットが作業コピーの履歴から失われています")
	}
	return nil
}

func discardBaseline(ctx context.Context, m manifest, rel string) ([]byte, os.FileMode, error) {
	tree, err := git(ctx, m.Worktree, "ls-tree", m.BaseCommit, "--", rel)
	if err != nil {
		return nil, 0, err
	}
	mode := os.FileMode(0644)
	if strings.HasPrefix(tree, "100755 blob ") {
		mode = 0755
	} else if !strings.HasPrefix(tree, "100644 blob ") {
		return nil, 0, fmt.Errorf("実行開始時の対象が通常ファイルではありません")
	}
	content, err := git(ctx, m.Worktree, "show", m.BaseCommit+":"+rel)
	return []byte(content), mode, err
}

func (s *Service) publishDiscardQueue(cfg model.Config, tasks []model.Task) error {
	if err := writeOutputQueue(cfg, tasks); err != nil {
		return err
	}
	s.mu.Lock()
	s.state.Tasks = copyTasks(tasks)
	s.recountLocked()
	s.mu.Unlock()
	return nil
}

// recoverDiscards finishes only a previously journaled operation. Any changed
// HEAD, other file, or unexpected target bytes causes a refusal without reset.
func (s *Service) recoverDiscards(ctx context.Context) error {
	s.mu.Lock()
	cfg, m, tasks, readOnly := s.state.Config, s.meta, copyTasks(s.state.Tasks), s.state.ReadOnly
	s.mu.Unlock()
	if !hasPreparedDiscard(tasks) {
		return nil
	}
	if readOnly {
		return fmt.Errorf("閲覧専用のワークスペースでは変更破棄を回復できません")
	}
	if err := validateDiscardWorktree(ctx, cfg, m); err != nil {
		return err
	}
	for i := range tasks {
		for j := range tasks[i].Discards {
			d := tasks[i].Discards[j]
			if d.State != "prepared" {
				continue
			}
			if _, err := hex.DecodeString(d.ID); err != nil || len(d.ID) != 24 || d.RestoreCommit != m.BaseCommit || d.ThroughAttempt != tasks[i].Attempts || d.InputHash == d.OutputHash || d.Commit != "" {
				return fmt.Errorf("変更破棄の中断記録が不正です")
			}
			path, rel, err := discardTarget(m, tasks[i].File)
			if err != nil {
				return err
			}
			after, mode, err := discardBaseline(ctx, m, rel)
			if err != nil || digest(after) != d.OutputHash {
				return fmt.Errorf("変更破棄の復元内容と記録が一致しません")
			}
			head, err := git(ctx, m.Worktree, "rev-parse", "HEAD")
			if err != nil {
				return err
			}
			head = trim(head)
			if head == d.BaseCommit {
				if err = validateDiscardHistory(ctx, m, tasks, ""); err != nil {
					return err
				}
				original, err := git(ctx, m.Worktree, "show", head+":"+rel)
				if err != nil || digest([]byte(original)) != d.InputHash {
					return fmt.Errorf("変更破棄前のコミット内容と記録が一致しません")
				}
				files, err := changedFiles(ctx, m.Worktree)
				if err != nil {
					return err
				}
				for _, file := range files {
					if file != rel {
						return fmt.Errorf("変更破棄の中断後に対象外の変更があります: %s", file)
					}
				}
				staged, err := git(ctx, m.Worktree, "diff", "--cached", "--no-renames", "--name-only", "-z")
				if err != nil {
					return err
				}
				for _, file := range zeroLines(staged) {
					if file != rel {
						return fmt.Errorf("変更破棄の中断後に対象外のステージ変更があります: %s", file)
					}
				}
				indexed, err := git(ctx, m.Worktree, "show", ":"+rel)
				if err != nil || (digest([]byte(indexed)) != d.InputHash && digest([]byte(indexed)) != d.OutputHash) {
					return fmt.Errorf("変更破棄の中断後にステージ内容が変更されています")
				}
				current, err := os.ReadFile(path)
				if err != nil || (digest(current) != d.InputHash && digest(current) != d.OutputHash) {
					return fmt.Errorf("変更破棄の中断後に対象ファイルが変更されています")
				}
				if err = store.WriteFile(path, after, mode); err != nil {
					return err
				}
				if err = ctx.Err(); err != nil {
					return err
				}
				message := "OneByOne: discard " + tasks[i].File + "\n\nOneByOne-Discard: " + d.ID
				if _, err = git(ctx, m.Worktree, "commit", "--only", "-m", message, "--", rel); err != nil {
					return err // Journal remains prepared even if commit completed.
				}
				head, err = git(ctx, m.Worktree, "rev-parse", "HEAD")
				if err != nil {
					return err
				}
				head = trim(head)
			}
			message, err := git(ctx, m.Worktree, "log", "-1", "--format=%B")
			if err != nil || !strings.Contains(message, "\nOneByOne-Discard: "+d.ID+"\n") {
				return fmt.Errorf("変更破棄のコミットが中断記録と一致しません")
			}
			parent, err := git(ctx, m.Worktree, "rev-parse", "HEAD^")
			if err != nil || trim(parent) != d.BaseCommit {
				return fmt.Errorf("変更破棄のコミットの親が一致しません")
			}
			files, err := git(ctx, m.Worktree, "diff-tree", "--no-commit-id", "--no-renames", "--name-only", "-r", "-z", head)
			changed := zeroLines(files)
			if err != nil || len(changed) != 1 || changed[0] != rel {
				return fmt.Errorf("変更破棄のコミットが対象ファイルに限定されていません")
			}
			committed, err := git(ctx, m.Worktree, "show", head+":"+rel)
			if err != nil || digest([]byte(committed)) != d.OutputHash {
				return fmt.Errorf("変更破棄のコミット内容が復元内容と一致しません")
			}
			if err = requireCleanDiscardWorktree(ctx, m.Worktree); err != nil {
				return err
			}
			if err = validateDiscardHistory(ctx, m, tasks, head); err != nil {
				return err
			}
			d.State, d.Commit, d.FinishedAt = "done", head, now()
			tasks[i].Discards[j] = d
			// Explicit discard also authorizes requeue, including the same uncertain-
			// billing policy as RetryTasks. repairHistory's cutoff still forces a
			// fresh plan and candidate; cumulative fees are never reset.
			tasks[i].Status, tasks[i].ResumeRequested, tasks[i].Excluded = "pending", true, false
			tasks[i].RulesApplied = []string{}
			tasks[i].InputHash, tasks[i].UpdatedAt = d.OutputHash, d.FinishedAt
			tasks[i].Note = "変更を破棄し、実行開始前の内容に戻しました（再試行）。次の実行では新しい計画を作成します。履歴・使用量は保持しています"
			if err = s.publishDiscardQueue(cfg, tasks); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Service) restorePendingDiscards() error {
	s.mu.Lock()
	cfg, readOnly, pending := s.state.Config, s.state.ReadOnly, hasPreparedDiscard(s.state.Tasks)
	s.mu.Unlock()
	if readOnly || !pending {
		return nil
	}
	unlock, err := lockSharedQueue(cfg)
	if err != nil {
		return err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.recoverDiscards(ctx)
}
