package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
)

func attemptCommitBase(h model.Attempt) string {
	if h.CommitBase != "" {
		return h.CommitBase
	}
	return h.BaseCommit
}

// Normal adoption rollback owns only this file. An unrelated external change
// discovered by scopeCheck must never be erased by reset --hard / clean.
func rollbackAdoption(ctx context.Context, worktree, relative, base, inputHash, outputHash string) error {
	head, err := git(ctx, worktree, "rev-parse", "HEAD")
	if err != nil || trim(head) != base {
		return fmt.Errorf("採用中にHEADが変わったため自動復元を停止しました")
	}
	path, err := catalog.PathWithin(worktree, relative)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("採用中に対象ファイルの種類が変わったため自動復元を停止しました")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if hash := digest(data); hash != inputHash && hash != outputHash {
		return fmt.Errorf("対象ファイルに未知の変更があるため自動復元を停止しました")
	}
	_, err = git(ctx, worktree, "restore", "--source="+base, "--staged", "--worktree", "--", relative)
	return err
}

func recoverPendingWrite(ctx context.Context, m manifest, tasks []model.Task, head, message string, known map[string]bool) error {
	// Do not clean anything until the full branch history is accounted for.
	allowed := map[string]bool{}
	for commit := range known {
		allowed[commit] = true
	}
	for _, task := range tasks {
		if task.Status != "running" || len(task.History) == 0 {
			continue
		}
		h := task.History[len(task.History)-1]
		if h.Outcome == "validated" && h.OutputHash != "" && strings.Contains(message, "OneByOne-Attempt: "+h.ID) {
			allowed[head] = true // content, parent and review are checked by recover
		}
	}
	if _, err := git(ctx, m.Worktree, "merge-base", "--is-ancestor", m.BaseCommit, head); err != nil {
		return fmt.Errorf("作業コピーの履歴が基準コミットと一致しません")
	}
	log, err := git(ctx, m.Worktree, "log", "--format=%H", m.BaseCommit+".."+head)
	if err != nil {
		return err
	}
	for _, commit := range strings.Fields(log) {
		if !allowed[commit] {
			return fmt.Errorf("台帳にないコミットが作業コピーにあります: %s", commit)
		}
	}
	files, err := changedFiles(ctx, m.Worktree)
	if err != nil || len(files) == 0 {
		return err
	}
	if len(files) != 1 {
		return fmt.Errorf("中断後に対象外の変更があります。作業コピーを確認してください: %s", strings.Join(files, ", "))
	}
	for _, task := range tasks {
		if task.Status != "running" || len(task.History) == 0 {
			continue
		}
		h := task.History[len(task.History)-1]
		rel := filepath.ToSlash(filepath.Join(m.SourceRelative, task.File))
		if files[0] != rel || attemptCommitBase(h) != head {
			continue
		}
		path, err := catalog.PathWithin(filepath.Join(m.Worktree, m.SourceRelative), task.File)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if hash := digest(data); hash != h.InputHash && hash != h.OutputHash {
			return fmt.Errorf("中断後にファイルが変更されています: %s", task.File)
		}
		return rollback(ctx, m.Worktree, head)
	}
	return fmt.Errorf("作業コピーに未記録の変更があります: %s", files[0])
}
