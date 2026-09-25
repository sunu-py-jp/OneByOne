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

func trim(s string) string { return strings.TrimSpace(s) }

func zeroLines(s string) []string {
	out := []string{}
	for _, p := range strings.Split(s, "\x00") {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// Disable optional locks so status cannot refresh the source repository's
// index while checking a folder in the picker or immediately before a run.
func readSourceGit(ctx context.Context, root string, args ...string) (string, error) {
	out, err := git(ctx, root, append([]string{"--no-optional-locks"}, args...)...)
	if ctx.Err() != nil {
		return "", fmt.Errorf("対象フォルダのGit管理状態を確認できませんでした。もう一度お試しください")
	}
	return out, err
}

func sourceGitRepository(ctx context.Context, root string) (string, error) {
	if installation := CheckGitInstallation(ctx); !installation.Available {
		return "", fmt.Errorf("%s", installation.Message)
	}
	inside, err := readSourceGit(ctx, root, "rev-parse", "--is-inside-work-tree")
	if ctx.Err() != nil {
		return "", fmt.Errorf("対象フォルダのGit管理状態を確認できませんでした。もう一度お試しください")
	}
	if err != nil || trim(inside) != "true" {
		return "", fmt.Errorf("修正処理の対象フォルダはGitで管理されている必要があります。")
	}
	repo, err := readSourceGit(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("対象フォルダのGitリポジトリを確認できません: %w", err)
	}
	return trim(repo), nil
}

// readyGitSource is shared by folder selection and execution so accepting a
// directory means its entire repository has a committed, clean starting point.
func readyGitSource(ctx context.Context, root string) (string, string, error) {
	repo, e := sourceGitRepository(ctx, root)
	if e != nil {
		return "", "", e
	}
	head, e := readSourceGit(ctx, repo, "rev-parse", "--verify", "HEAD^{commit}")
	if ctx.Err() != nil {
		return "", "", fmt.Errorf("対象フォルダのGit管理状態を確認できませんでした。もう一度お試しください")
	}
	if e != nil {
		return "", "", fmt.Errorf("対象フォルダのGitリポジトリに初回コミットがありません。修正対象のソースをGitにコミットしてください")
	}
	head = trim(head)
	dirty, e := readSourceGit(ctx, repo, "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if e != nil {
		return "", "", fmt.Errorf("対象リポジトリの変更を確認できません: %w", e)
	}
	sourceDirty, e := hasSourceChanges(dirty)
	if e != nil {
		return "", "", e
	}
	if sourceDirty {
		return "", "", fmt.Errorf("対象リポジトリ全体に未コミットの変更があります（未追跡ファイルも含みます）。必要な変更をコミットするか、クリーンな作業コピーを指定してください")
	}
	return repo, head, nil
}

func (s *Service) prepareWorktree(ctx context.Context, cfg model.Config) error {
	repo, head, e := readyGitSource(ctx, cfg.Root)
	if e != nil {
		return e
	}
	rel, e := filepath.Rel(repo, cfg.Root)
	if e != nil {
		return e
	}
	if strings.HasPrefix(rel, "..") { // resolve /var and /tmp aliases consistently
		a, _ := filepath.EvalSymlinks(repo)
		b, _ := filepath.EvalSymlinks(cfg.Root)
		rel, e = filepath.Rel(a, b)
		if e != nil || strings.HasPrefix(rel, "..") {
			return fmt.Errorf("対象フォルダがGitリポジトリ外です")
		}
	}
	s.mu.Lock()
	m := s.meta
	s.mu.Unlock()
	wanted := cfg.QueuePath + ".worktree"
	if m.Worktree != "" {
		if !sameRoot(m.Worktree, wanted) || m.BaseCommit != head || m.SourceRelative != rel {
			return fmt.Errorf("元ソースまたは作業コピーの情報が変更されています。設定を複製して、新しいワークスペースで対象を抽出してください")
		}
		if _, e = catalog.PathWithin(m.Worktree, "."); e != nil { // PathWithin intentionally rejects '.', so validate through git below
			if info, err := os.Lstat(m.Worktree); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("作業コピーを確認できません: %s", m.Worktree)
			}
		}
		branch, err := git(ctx, m.Worktree, "branch", "--show-current")
		if err != nil || trim(branch) != m.Branch {
			return fmt.Errorf("作業コピーのブランチがセッションと一致しません")
		}
		common, err := git(ctx, m.Worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return err
		}
		orig, err := git(ctx, repo, "rev-parse", "--path-format=absolute", "--git-common-dir")
		if err != nil {
			return err
		}
		a, _ := filepath.EvalSymlinks(trim(common))
		b, _ := filepath.EvalSymlinks(trim(orig))
		if a == "" || a != b {
			return fmt.Errorf("作業コピーは対象リポジトリに所属していません")
		}
	} else {
		if _, e = os.Lstat(wanted); !os.IsNotExist(e) {
			return fmt.Errorf("作業コピーの保存先が既に存在するため、上書きせず停止しました: %s", wanted)
		}
		branch := "onebyone/" + uid()
		s.log("info", "専用Git作業コピーを作成しています")
		if _, e = git(ctx, repo, "worktree", "add", "-b", branch, wanted, head); e != nil {
			return e
		}
		m = manifest{Version: 1, Root: cfg.Root, RepoRoot: repo, SourceRelative: rel, BaseCommit: head, Worktree: wanted, Branch: branch, RuleHash: s.cat.Hash, RulePackagePath: m.RulePackagePath}
	}
	s.mu.Lock()
	s.meta = m
	s.state.Worktree = m.Worktree
	s.state.Branch = m.Branch
	s.mu.Unlock()
	return s.persist()
}

func changedFiles(ctx context.Context, worktree string) ([]string, error) {
	// Report both sides of renames. Ignoring the deleted side could hide a
	// checker moving a configuration file over the one allowed source target.
	out, e := git(ctx, worktree, "diff", "--no-renames", "--name-only", "-z", "HEAD", "--")
	if e != nil {
		return nil, e
	}
	u, e := git(ctx, worktree, "ls-files", "--others", "--exclude-standard", "-z")
	if e != nil {
		return nil, e
	}
	return append(zeroLines(out), zeroLines(u)...), nil
}

// Every uncommitted source change blocks a run, including both rename paths.
func hasSourceChanges(status string) (bool, error) {
	if status == "" {
		return false, nil
	}
	if !strings.HasSuffix(status, "\x00") {
		return false, fmt.Errorf("Gitの変更一覧が不完全です")
	}
	records := strings.Split(strings.TrimSuffix(status, "\x00"), "\x00")
	dirty := false
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 || record[2] != ' ' {
			return false, fmt.Errorf("Gitの変更一覧を解釈できません")
		}
		dirty = true
		if strings.ContainsAny(record[:2], "RC") {
			// Under -z the first path is the destination and the next NUL
			// record is the original path, without another status prefix.
			i++
			if i >= len(records) || records[i] == "" {
				return false, fmt.Errorf("Gitの移動元情報が不完全です")
			}
		}
	}
	return dirty, nil
}

func scopeCheck(ctx context.Context, worktree, target string) (model.Check, error) {
	c := model.Check{Name: "編集範囲", Status: "passed", Detail: "変更は対象ファイル1つに限定されています"}
	files, e := changedFiles(ctx, worktree)
	if e != nil {
		c.Status = "failed"
		c.Detail = e.Error()
		return c, e
	}
	if len(files) != 1 || files[0] != filepath.ToSlash(target) {
		c.Status = "failed"
		c.Detail = "変更範囲が一致しません: " + strings.Join(files, ", ")
	}
	return c, nil
}

func rollback(ctx context.Context, worktree, expectedHead string) error {
	head, e := git(ctx, worktree, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	if trim(head) != expectedHead {
		return fmt.Errorf("作業コピーに想定外のコミットがあります。自動巻き戻しを停止しました")
	}
	// This directory was created by this runner, is separately locked, and was
	// clean at attempt start. Never run these commands on the user's checkout.
	if _, e = git(ctx, worktree, "reset", "--hard", expectedHead); e != nil {
		return e
	}
	if _, e = git(ctx, worktree, "clean", "-fd"); e != nil {
		return e
	}
	return nil
}

func (s *Service) recover(ctx context.Context) error {
	if err := s.recoverDiscards(ctx); err != nil {
		return err
	}
	s.mu.Lock()
	tasks := copyTasks(s.state.Tasks)
	m := s.meta
	// The in-memory manifest can precede the latest persisted settings. Recovery
	// must locate checkpoints in the active queue, including in the same process.
	m.Config = storedConfig(s.state.Config)
	s.mu.Unlock()
	head, e := git(ctx, m.Worktree, "rev-parse", "HEAD")
	if e != nil {
		return e
	}
	head = trim(head)
	message, e := git(ctx, m.Worktree, "log", "-1", "--format=%B")
	if e != nil {
		return e
	}
	known := map[string]bool{}
	for i, t := range tasks {
		for _, d := range t.Discards {
			if d.State == "done" && d.Commit != "" {
				known[d.Commit] = true
			}
		}
		for _, h := range t.History {
			if h.Commit != "" {
				known[h.Commit] = true
			}
		}
		if t.Status != "running" {
			continue
		}
		if len(t.History) == 0 {
			return fmt.Errorf("%s: 中断記録がありません。手動確認してください", t.File)
		}
		h := t.History[len(t.History)-1]
		var reportCheckpoint *repairCheckpoint
		if strings.Contains(message, "OneByOne-Attempt: "+h.ID) && h.Outcome == "validated" && h.OutputHash != "" {
			parent, err := git(ctx, m.Worktree, "rev-parse", "HEAD^")
			if err != nil || trim(parent) != h.BaseCommit {
				return fmt.Errorf("中断したコミットの親が一致しません")
			}
			files, err := git(ctx, m.Worktree, "diff-tree", "--no-commit-id", "--name-only", "-r", "-z", head)
			if err != nil {
				return err
			}
			rel := filepath.ToSlash(filepath.Join(m.SourceRelative, t.File))
			changed := zeroLines(files)
			p, err := catalog.PathWithin(filepath.Join(m.Worktree, m.SourceRelative), t.File)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil || digest(data) != h.OutputHash || len(changed) != 1 || changed[0] != rel || !checksPass(h.Checks) {
				return fmt.Errorf("中断したコミットの検証記録と内容が一致しません")
			}
			if h.RepairPath != "" {
				checkpoint, err := loadRepairCheckpoint(m.Config, h)
				if err != nil {
					return err
				}
				reportCheckpoint = checkpoint
				// A candidate produced by the agent protocol must carry the exact
				// independent review even if the process exited just after commit.
				if checkpoint.State.LastCandidate != nil && !passedIndependentReview(checkpoint.State.LastCandidate, h.InputHash, h.OutputHash, checkpoint.State.Plan) {
					return fmt.Errorf("中断したコミットに一致する独立レビューの合格記録がありません")
				}
				h.Reviews = copyReviews(checkpoint.State.Reviews)
				h.Usage = usageSince(checkpoint.State.Usage, checkpoint.PriorUsage)
			}
			h.Commit = head
			h.Outcome = "done"
			h.FinishedAt = now()
			t.Status = "done"
			t.RulesApplied = h.RulesApplied
			t.Note = "コミット済みの変更を回復しました"
			known[head] = true
		} else {
			if head != h.BaseCommit {
				return fmt.Errorf("中断した作業のHEADが一致しません。手動確認してください")
			}
			files, err := changedFiles(ctx, m.Worktree)
			if err != nil {
				return err
			}
			target := filepath.ToSlash(filepath.Join(m.SourceRelative, t.File))
			for _, f := range files {
				if f != target {
					return fmt.Errorf("中断後に対象外の変更があります。作業コピーを確認してください: %s", f)
				}
			}
			if len(files) > 0 {
				p, err := catalog.PathWithin(filepath.Join(m.Worktree, m.SourceRelative), t.File)
				if err != nil {
					return err
				}
				data, err := os.ReadFile(p)
				if err != nil {
					return err
				}
				hash := digest(data)
				if hash != h.InputHash && hash != h.OutputHash {
					return fmt.Errorf("中断後にファイルが変更されています: %s", t.File)
				}
			}
			if e = rollback(ctx, m.Worktree, head); e != nil {
				return e
			}
			h.Outcome = "interrupted"
			h.Usage.Uncertain = true
			h.FinishedAt = now()
			h.Note = "前回の処理が中断しました。APIの未報告使用量がある可能性があります"
			t.Status = "needs_human"
			t.Note = h.Note
			if m.Config.MaxCostUSD > 0 {
				t.Status = "needs_human"
			}
			if h.RepairPath != "" {
				checkpoint, err := loadRepairCheckpoint(m.Config, h)
				if err != nil {
					return err
				}
				reportCheckpoint = checkpoint
				checkpoint.State.Usage.Uncertain = checkpoint.State.Usage.Uncertain || checkpoint.State.RequestPending
				h.Usage = usageSince(checkpoint.State.Usage, checkpoint.PriorUsage)
				for j := range checkpoint.State.Reviews {
					r := &checkpoint.State.Reviews[j]
					if r.Verdict == "running" {
						r.Verdict, r.Summary, r.FinishedAt = "error", "独立レビューが中断しました。再実行を指定して復帰できます", now()
						if checkpoint.State.LastCandidate != nil && checkpoint.State.LastCandidate.Review != nil && checkpoint.State.LastCandidate.Review.ID == r.ID {
							copy := *r
							checkpoint.State.LastCandidate.Review = &copy
						}
					}
				}
				h.Reviews = copyReviews(checkpoint.State.Reviews)
				h.Note = "処理が中断しました。再実行を指定すると保存済みの計画から復帰します（計画・履歴を保持し、次の実行の処理上限は0から数えます）"
				if h.Usage.Uncertain {
					h.Note += "。応答待ちだったAPIの使用量は未確認です"
				}
				t.Status, t.Note, t.ResumeRequested = "needs_human", h.Note, false
				if _, err = saveRepairCheckpoint(m.Config, checkpoint); err != nil {
					return err
				}
			}
		}
		if reportCheckpoint != nil {
			h.Changes = buildChangeReport(h, reportCheckpoint)
		}
		t.History[len(t.History)-1] = h
		t.UpdatedAt = now()
		s.mu.Lock()
		s.state.Tasks[i] = copyTask(t)
		s.recountLocked()
		s.mu.Unlock()
	}
	log, e := git(ctx, m.Worktree, "log", "--format=%H", m.BaseCommit+"..HEAD")
	if e != nil {
		return e
	}
	for _, c := range strings.Fields(log) {
		if !known[c] {
			return fmt.Errorf("台帳にないコミットが作業コピーにあります: %s", c)
		}
	}
	files, e := changedFiles(ctx, m.Worktree)
	if e != nil {
		return e
	}
	if len(files) > 0 {
		return fmt.Errorf("作業コピーに未記録の変更があります: %s", strings.Join(files, ", "))
	}
	return s.persist()
}
