package engine

import (
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"onebyone/internal/catalog"
	"onebyone/internal/model"
)

// All inputs are copied under the service lock. Commit objects, rather than
// the mutable worktree or its HEAD, keep one response consistent while an
// editor or validator is preparing the next candidate.
func cumulativeFileDetail(cfg model.Config, m manifest, task model.Task) (model.FileDetail, error) {
	d := model.FileDetail{Task: task, Cumulative: true, Changes: []model.ChangeReportItem{}}
	if err := validatePreviewFilePath(task.File); err != nil {
		return d, err
	}
	if m.BaseCommit == "" {
		if len(task.History) > 0 {
			return d, fmt.Errorf("累積結果の基準コミットが見つかりません")
		}
		path, err := catalog.PathWithin(cfg.Root, task.File)
		if err != nil {
			return d, err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return d, err
		}
		if len(b) > cfg.EffectiveMaxFileBytes() {
			return d, fmt.Errorf("ファイルが表示上限を超えています")
		}
		d.Before, d.After = string(b), string(b)
		return d, nil
	}
	relative := filepath.ToSlash(filepath.Join(m.SourceRelative, task.File))
	if err := validatePreviewFilePath(relative); err != nil {
		return d, err
	}
	repo := m.RepoRoot
	if repo == "" {
		repo = m.Worktree
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	read := func(commit, expectedHash string) (string, error) {
		if !isImmutableCommitID(commit) {
			return "", fmt.Errorf("累積結果のコミットIDが不正です")
		}
		text, err := git(ctx, repo, "show", "--no-ext-diff", "--no-textconv", commit+":"+relative)
		if err != nil {
			return "", fmt.Errorf("採用済みファイルを読み込めません: %w", err)
		}
		if len(text) > cfg.EffectiveMaxFileBytes() {
			return "", fmt.Errorf("ファイルが表示上限を超えています")
		}
		if expectedHash != "" && digest([]byte(text)) != expectedHash {
			return "", fmt.Errorf("採用済みファイルと保存されたハッシュが一致しません")
		}
		return text, nil
	}
	var err error
	d.Before, err = read(m.BaseCommit, "")
	if err != nil {
		return d, err
	}
	d.After = d.Before
	afterCommit := m.BaseCommit
	activeHistory := repairHistory(task)
	for i := len(activeHistory) - 1; i >= 0; i-- {
		h := activeHistory[i]
		if h.Outcome != "done" || h.Commit == "" {
			continue
		}
		d.After, err = read(h.Commit, h.OutputHash)
		if err != nil {
			return d, err
		}
		afterCommit = h.Commit
		break
	}
	if d.Before != d.After {
		d.Diff, err = git(ctx, repo, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", m.BaseCommit, afterCommit, "--", relative)
		if err != nil {
			return d, fmt.Errorf("採用済みの累積差分を取得できません: %w", err)
		}
	}
	changes, err := cumulativeChangeReports(task, d.Before, d.After, func(h model.Attempt) (string, string, error) {
		before, err := read(h.BaseCommit, h.InputHash)
		if err != nil {
			return "", "", err
		}
		after, err := read(h.Commit, h.OutputHash)
		return before, after, err
	})
	if err != nil {
		return d, err
	}
	d.Changes = append(d.Changes, changes...)
	return d, nil
}

func isImmutableCommitID(commit string) bool {
	if len(commit) != 40 && len(commit) != 64 {
		return false
	}
	_, err := hex.DecodeString(commit)
	return err == nil
}
