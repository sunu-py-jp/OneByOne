package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"onebyone/internal/model"
	"onebyone/internal/store"
)

// The journal precedes the atomic branch creation. A crash after update-ref and
// before the final write is reconciled by comparing the exact recorded commit;
// recovery never creates or rewinds any branch on its own.
type resultPublicationJournal struct {
	Version    int                       `json:"version"`
	Records    []resultPublicationRecord `json:"records"`
	reconciled bool
}
type resultPublicationRecord struct {
	State       string                  `json:"state"`
	Publication model.ResultPublication `json:"publication"`
}

type resultPublicationSnapshot struct {
	config      model.Config
	meta        manifest
	workspaceID string
	tasks       []model.Task
	rules       []model.Rule
}

func (s *Service) publicationSnapshot() resultPublicationSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return resultPublicationSnapshot{s.state.Config, s.meta, s.state.ActiveWorkspaceID, copyTasks(s.state.Tasks), append([]model.Rule{}, s.state.Rules...)}
}

func (s *Service) GetResultPublicationPreview() (model.ResultPublicationPreview, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return model.ResultPublicationPreview{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snapshot := s.publicationSnapshot()
	if snapshot.meta.Worktree != "" && s.editable() == nil {
		unlock, err := lockSharedQueue(snapshot.config)
		if err != nil {
			return model.ResultPublicationPreview{}, err
		}
		defer unlock()
		journal, err := loadResultPublications(ctx, snapshot.config, snapshot.meta)
		if err != nil {
			return model.ResultPublicationPreview{}, err
		}
		if journal.reconciled {
			if err = writeOutputJSON(snapshot.config, snapshot.config.QueuePath+".publications.json", journal); err != nil {
				return model.ResultPublicationPreview{}, err
			}
		}
	}
	return buildResultPublicationPreview(ctx, snapshot)
}

func buildResultPublicationPreview(ctx context.Context, snapshot resultPublicationSnapshot) (model.ResultPublicationPreview, error) {
	cfg, m := snapshot.config, snapshot.meta
	p := model.ResultPublicationPreview{WorkspaceID: snapshot.workspaceID, Files: []model.ResultPublicationFile{}, Publications: []model.ResultPublication{}}
	if m.Worktree == "" {
		return p, nil
	}
	if err := validatePublicationWorktree(ctx, snapshot); err != nil {
		return p, err
	}
	head, err := git(ctx, m.Worktree, "rev-parse", "HEAD")
	if err != nil {
		return p, err
	}
	p.BaseCommit, p.SourceCommit = m.BaseCommit, trim(head)
	// Immutable commit arguments keep the preview coherent even if an external
	// Git client moves a ref after this read. Publication revalidates the snapshot.
	changed, err := git(ctx, m.Worktree, "diff", "--no-ext-diff", "--no-textconv", "--no-renames", "--name-only", "-z", p.BaseCommit, p.SourceCommit, "--")
	if err != nil {
		return p, err
	}
	tasks := make(map[string]model.Task, len(snapshot.tasks))
	for _, task := range snapshot.tasks {
		tasks[filepath.ToSlash(filepath.Join(m.SourceRelative, task.File))] = task
	}
	for _, path := range zeroLines(changed) {
		task, ok := tasks[path]
		if !ok {
			return p, fmt.Errorf("採用済みの対象一覧にない変更があります: %s", path)
		}
		file := publicationFileSummary(task)
		file.LinkPath = filepath.ToSlash(filepath.Join(m.SourceRelative, task.File))
		p.Files = append(p.Files, file)
	}
	sort.Slice(p.Files, func(i, j int) bool { return p.Files[i].File < p.Files[j].File })
	p.Message, err = publicationCommitMessage(p.Files, publicationReportContext(snapshot))
	if err != nil {
		return p, err
	}
	binding, err := json.Marshal(struct {
		WorkspaceID, BaseCommit, SourceCommit string
		Files                                 []model.ResultPublicationFile
		Message                               string
	}{p.WorkspaceID, p.BaseCommit, p.SourceCommit, p.Files, p.Message})
	if err != nil {
		return p, err
	}
	p.Revision = digest(binding)
	journal, err := loadResultPublications(ctx, cfg, m)
	if err != nil {
		return p, err
	}
	for _, r := range journal.Records {
		if r.State == "done" {
			p.Publications = append(p.Publications, r.Publication)
		}
	}
	refs, err := git(ctx, m.Worktree, "for-each-ref", "--format=%(refname:short)", "refs/heads/")
	if err != nil {
		return p, err
	}
	existing := map[string]bool{}
	for _, ref := range strings.Split(refs, "\n") {
		existing[ref] = true
	}
	base := "onebyone/review-" + time.Now().Format("20060102")
	p.SuggestedBranch = base
	for suffix := 2; existing[p.SuggestedBranch]; suffix++ {
		p.SuggestedBranch = fmt.Sprintf("%s-%d", base, suffix)
	}
	return p, nil
}

func validatePublicationWorktree(ctx context.Context, snapshot resultPublicationSnapshot) error {
	if err := validateDiscardWorktree(ctx, snapshot.config, snapshot.meta); err != nil {
		return fmt.Errorf("結果反映: %w", err)
	}
	status, err := git(ctx, snapshot.meta.Worktree, "--no-optional-locks", "status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none")
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("作業コピーに未コミットの変更があるため、結果を反映できません")
	}
	if hasPreparedDiscard(snapshot.tasks) {
		return fmt.Errorf("変更破棄が完了していません。ワークスペースを開き直してください")
	}
	return validateDiscardHistory(ctx, snapshot.meta, snapshot.tasks, "")
}

func publicationFileSummary(task model.Task) model.ResultPublicationFile {
	file := model.ResultPublicationFile{File: task.File, RulesApplied: []string{}}
	notes, seenNotes, rules := []string{}, map[string]bool{}, map[string]bool{}
	// This is attribution to actually adopted history, not a claim that every
	// historical edit span remains unchanged. Whole-file reverts are removed by
	// the tree diff; explicit discards cut off their earlier attempts. This avoids
	// loading every historical blob for potentially ten thousand preview files.
	for _, attempt := range repairHistory(task) {
		if !attempt.AdoptedChanges() {
			continue
		}
		if len(attempt.Changes) > 0 {
			for _, change := range attempt.Changes {
				if change.Status != "fixed" {
					continue
				}
				if change.RuleID != "" {
					rules[change.RuleID] = true
				}
				description := strings.TrimSpace(change.Change)
				if description == "" {
					description = strings.TrimSpace(change.RuleTitle)
				}
				if description == "" {
					description = "修正を適用"
				}
				line := "- " + change.RuleID + ": " + description
				if !seenNotes[line] {
					notes, seenNotes[line] = append(notes, line), true
				}
			}
		} else {
			// Without change rows (for example, an unavailable checkpoint), the
			// recorded rulesApplied is the only attribution; never use task.Rules.
			for _, id := range attempt.RulesApplied {
				if id == "" {
					continue
				}
				rules[id] = true
				line := "- " + id + ": 修正を適用"
				if !seenNotes[line] {
					notes, seenNotes[line] = append(notes, line), true
				}
			}
		}
	}
	for id := range rules {
		file.RulesApplied = append(file.RulesApplied, id)
	}
	sort.Strings(file.RulesApplied)
	if len(notes) == 0 {
		notes = append(notes, "- 採用済みの変更（ルール別の対応記録なし）")
	}
	file.Summary = strings.Join(notes, "\n")
	// Match the latest accepted result explanation, preserving every line.
	for _, attempt := range repairHistory(task) {
		if (attempt.AdoptedChanges() || attempt.Outcome == "skipped") && strings.TrimSpace(attempt.Note) != "" {
			file.Summary = attempt.Note
		}
	}
	return file
}

func (s *Service) GetResultPublicationFileDiff(workspaceID, revision, file string) (string, error) {
	s.op.Lock()
	defer s.op.Unlock()
	if err := s.idle(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	snapshot := s.publicationSnapshot()
	preview, err := buildResultPublicationPreview(ctx, snapshot)
	if err != nil {
		return "", err
	}
	if workspaceID != preview.WorkspaceID || revision == "" || revision != preview.Revision {
		return "", fmt.Errorf("反映内容が変更されています。プレビューを更新してください")
	}
	for _, candidate := range preview.Files {
		if candidate.File != file {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(snapshot.meta.SourceRelative, file))
		return git(ctx, snapshot.meta.Worktree, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", preview.BaseCommit, preview.SourceCommit, "--", rel)
	}
	return "", fmt.Errorf("反映対象にないファイルです: %s", file)
}

func (s *Service) PublishResults(req model.PublishResultsRequest) (model.ResultPublication, error) {
	s.op.Lock()
	defer s.op.Unlock()
	var result model.ResultPublication
	if err := s.editable(); err != nil {
		return result, err
	}
	if strings.ContainsAny(req.Title, "\r\n\x00") {
		return result, fmt.Errorf("コミットタイトルを1行で入力してください")
	}
	req.Title = strings.TrimSpace(req.Title)
	req.Branch = strings.TrimSpace(req.Branch)
	if req.Title == "" || strings.ContainsAny(req.Title, "\r\n\x00") {
		return result, fmt.Errorf("コミットタイトルを1行で入力してください")
	}
	if strings.ContainsRune(req.Message, '\x00') || len(req.Message) > maxPublicationMessageBytes || len(req.Title) > 1024 {
		return result, fmt.Errorf("コミットメッセージが不正、または長すぎます")
	}
	snapshot := s.publicationSnapshot()
	if req.WorkspaceID != snapshot.workspaceID || req.Revision == "" {
		return result, fmt.Errorf("ワークスペースまたはプレビューが一致しません。反映内容を更新してください")
	}
	unlock, err := lockSharedQueue(snapshot.config)
	if err != nil {
		return result, err
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if req.Branch == "" || strings.HasPrefix(req.Branch, "-") {
		return result, fmt.Errorf("新規ブランチ名を入力してください")
	}
	if _, err = git(ctx, snapshot.meta.Worktree, "check-ref-format", "--branch", req.Branch); err != nil {
		return result, fmt.Errorf("ブランチ名が不正です: %s", req.Branch)
	}
	preview, err := buildResultPublicationPreview(ctx, snapshot)
	if err != nil {
		return result, err
	}
	if req.Revision != preview.Revision {
		return result, fmt.Errorf("反映内容が変更されています。プレビューを更新してください")
	}
	if len(preview.Files) == 0 {
		return result, fmt.Errorf("反映できる採用済みの変更がありません")
	}
	exists, err := publicationBranchExists(ctx, snapshot.meta.Worktree, req.Branch)
	if err != nil {
		return result, err
	}
	if exists {
		return result, fmt.Errorf("同名のブランチが既に存在します: %s", req.Branch)
	}
	// Only immutable objects are read below. The atomic ref transaction checks
	// the internal branch's expected commit and creates the new branch, so an
	// external checkout cannot alter the reviewed result. No checkout/index/HEAD
	// lock is needed and no manual Git lock can be left behind by a crash.
	if err = validatePublicationWorktree(ctx, snapshot); err != nil {
		return result, err
	}
	head, err := git(ctx, snapshot.meta.Worktree, "rev-parse", "HEAD")
	if err != nil || trim(head) != preview.SourceCommit {
		return result, fmt.Errorf("作業コピーが変更されています。プレビューを更新してください")
	}
	tree, err := git(ctx, snapshot.meta.Worktree, "rev-parse", preview.SourceCommit+"^{tree}")
	if err != nil {
		return result, err
	}
	message := req.Title + "\n"
	if strings.TrimSpace(req.Message) != "" {
		message += "\n" + strings.TrimSpace(req.Message) + "\n"
	}
	commit, err := publicationGitInput(ctx, snapshot.meta.Worktree, message, "commit-tree", trim(tree), "-p", preview.BaseCommit, "-F", "-")
	if err != nil {
		return result, err
	}
	result = model.ResultPublication{Branch: req.Branch, Commit: trim(commit), BaseCommit: preview.BaseCommit, SourceCommit: preview.SourceCommit, CreatedAt: now(), FileCount: len(preview.Files), Title: req.Title, Message: strings.TrimSpace(req.Message)}
	journal, err := loadResultPublications(ctx, snapshot.config, snapshot.meta)
	if err != nil {
		return model.ResultPublication{}, err
	}
	journal.Records = append(journal.Records, resultPublicationRecord{State: "prepared", Publication: result})
	if err = writeOutputJSON(snapshot.config, snapshot.config.QueuePath+".publications.json", journal); err != nil {
		return model.ResultPublication{}, err
	}
	if err = createResultPublicationBranch(ctx, snapshot.meta, result); err != nil {
		// A definite transaction rejection (including an externally-created name)
		// did not publish this intent. Do not leave a conflicting prepared record.
		// Cancellation keeps the journal because the transaction may have committed.
		var exit *exec.ExitError
		if ctx.Err() == nil && errors.As(err, &exit) {
			journal.Records = journal.Records[:len(journal.Records)-1]
			if saveErr := writeOutputJSON(snapshot.config, snapshot.config.QueuePath+".publications.json", journal); saveErr != nil {
				return model.ResultPublication{}, fmt.Errorf("ブランチ作成に失敗し中断記録を更新できません: %v: %w", err, saveErr)
			}
		}
		return model.ResultPublication{}, fmt.Errorf("ブランチを作成できません。別のGit操作で変更された可能性があります: %w", err)
	}
	journal.Records[len(journal.Records)-1].State = "done"
	if err = writeOutputJSON(snapshot.config, snapshot.config.QueuePath+".publications.json", journal); err != nil {
		return model.ResultPublication{}, fmt.Errorf("ブランチ %s は作成済みですが履歴の保存が中断しました。プレビューを更新すると回復します: %w", result.Branch, err)
	}
	s.log("info", "結果を "+result.Branch+" に1コミットで反映しました")
	return result, nil
}

func loadResultPublications(ctx context.Context, cfg model.Config, m manifest) (resultPublicationJournal, error) {
	journal := resultPublicationJournal{Version: 1, Records: []resultPublicationRecord{}}
	path := cfg.QueuePath + ".publications.json"
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return journal, nil
	}
	if err != nil {
		return journal, err
	}
	if !info.Mode().IsRegular() {
		return journal, fmt.Errorf("結果反映履歴が通常のファイルではありません")
	}
	data, err := store.ReadFile(path)
	if err != nil {
		return journal, err
	}
	if err = json.Unmarshal(data, &journal); err != nil || journal.Version != 1 {
		return journal, fmt.Errorf("結果反映履歴を読み込めません")
	}
	recovered := journal.Records[:0]
	for _, record := range journal.Records {
		p := record.Publication
		if !isImmutableCommitID(p.Commit) || !isImmutableCommitID(p.BaseCommit) || !isImmutableCommitID(p.SourceCommit) || p.BaseCommit != m.BaseCommit || p.Branch == "" || p.FileCount < 1 {
			return journal, fmt.Errorf("結果反映履歴の記録が不正です")
		}
		switch record.State {
		case "done":
		case "prepared":
			exists, e := publicationBranchExists(ctx, m.Worktree, p.Branch)
			if e != nil {
				return journal, e
			}
			journal.reconciled = true
			if !exists {
				continue
			} // the atomic transaction did not publish a ref
			ref, e := git(ctx, m.Worktree, "rev-parse", "--verify", "refs/heads/"+p.Branch)
			if e != nil {
				return journal, e
			}
			if e = validatePreparedPublication(ctx, m.Worktree, p); e != nil {
				return journal, e
			}
			if trim(ref) != p.Commit {
				return journal, fmt.Errorf("結果反映中のブランチ %s が外部で変更されています", p.Branch)
			}
			record.State = "done"
		default:
			return journal, fmt.Errorf("結果反映履歴の状態が不正です")
		}
		recovered = append(recovered, record)
	}
	journal.Records = recovered
	return journal, nil
}

func publicationGitInput(ctx context.Context, dir, input string, args ...string) (string, error) {
	return publicationGitInputLimit(ctx, dir, input, 2<<20, args...)
}

func publicationGitInputLimit(ctx context.Context, dir, input string, limit int, args ...string) (string, error) {
	base := []string{"--literal-pathspecs", "-c", "core.hooksPath=" + os.DevNull, "-c", "commit.gpgsign=false", "-c", "i18n.commitEncoding=UTF-8", "-c", "i18n.logOutputEncoding=UTF-8", "-c", "core.autocrlf=false", "-c", "core.fsmonitor=false", "-c", "user.name=OneByOne", "-c", "user.email=onebyone@localhost"}
	cmd, err := runtimeCommand(ctx, "git", append(base, args...)...)
	if err != nil {
		return "", err
	}
	cmd.Dir, cmd.Stdin, cmd.WaitDelay = dir, strings.NewReader(input), 3*time.Second
	out, stderr := limitedBuffer{max: limit}, limitedBuffer{max: 2 << 20}
	cmd.Stdout, cmd.Stderr = &out, &stderr
	if err = cmd.Run(); err != nil {
		return "", fmt.Errorf("git: %w\n%s", err, stderr.String())
	}
	if out.truncated || stderr.truncated {
		return "", fmt.Errorf("Gitの出力が上限を超えました")
	}
	return out.String(), nil
}

func publicationBranchExists(ctx context.Context, repo, branch string) (bool, error) {
	_, err := git(ctx, repo, "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func validatePreparedPublication(ctx context.Context, repo string, p model.ResultPublication) error {
	// Raw object bytes avoid local log-output encoding and allow the entire
	// accepted message plus object headers without truncation.
	content, err := publicationGitInputLimit(ctx, repo, "", len(p.Message)+len(p.Title)+65536, "cat-file", "commit", p.Commit)
	if err != nil {
		return err
	}
	headers, body, ok := strings.Cut(content, "\n\n")
	parent, recordedTree, parents := "", "", 0
	for _, line := range strings.Split(headers, "\n") {
		if strings.HasPrefix(line, "parent ") {
			parent = strings.TrimPrefix(line, "parent ")
			parents++
		}
		if strings.HasPrefix(line, "tree ") {
			recordedTree = strings.TrimPrefix(line, "tree ")
		}
	}
	tree, err := git(ctx, repo, "rev-parse", p.SourceCommit+"^{tree}")
	if err != nil {
		return err
	}
	message := p.Title
	if strings.TrimSpace(p.Message) != "" {
		message += "\n\n" + strings.TrimSpace(p.Message)
	}
	if !ok || parents != 1 || parent != p.BaseCommit || recordedTree != trim(tree) || strings.TrimSpace(body) != message {
		return fmt.Errorf("結果反映のコミットと中断記録が一致しません: %s", p.Branch)
	}
	return nil
}

func createResultPublicationBranch(ctx context.Context, m manifest, p model.ResultPublication) error {
	common, err := git(ctx, m.Worktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	transaction := fmt.Sprintf("start\nverify refs/heads/%s %s\ncreate refs/heads/%s %s\nprepare\ncommit\n", m.Branch, p.SourceCommit, p.Branch, p.Commit)
	_, err = publicationGitInput(ctx, m.RepoRoot, transaction, "--git-dir="+trim(common), "update-ref", "--stdin")
	return err
}

const maxPublicationMessageBytes = 64 << 20

type publicationRuleReport struct {
	Title      string
	Changes    []string
	Assessment *model.ReviewAssessment
}

type publicationReport struct {
	Tasks          []model.Task
	SourceRelative string
	Rules          map[string]map[string]publicationRuleReport
}

func publicationReportContext(snapshot resultPublicationSnapshot) publicationReport {
	report := publicationReport{Tasks: snapshot.tasks, SourceRelative: snapshot.meta.SourceRelative, Rules: map[string]map[string]publicationRuleReport{}}
	current := map[string]model.Rule{}
	for _, rule := range snapshot.rules {
		current[rule.ID] = rule
	}
	// Cache only rule metadata, not entire per-execution queues. The immutable
	// execution snapshot keeps titles correct after subsequent rule edits.
	byExecution := map[string]map[string]model.Rule{}
	for _, task := range snapshot.tasks {
		rules := map[string]publicationRuleReport{}
		for _, attempt := range repairHistory(task) {
			if !attempt.AdoptedChanges() && attempt.Outcome != "skipped" {
				continue
			}
			definitions := current
			if attempt.ExecutionID != "" {
				if _, exists := byExecution[attempt.ExecutionID]; !exists {
					byExecution[attempt.ExecutionID] = map[string]model.Rule{}
					if record, err := readExecutionRecord(snapshot.config.QueuePath, attempt.ExecutionID); err == nil {
						for _, rule := range record.State.Rules {
							byExecution[attempt.ExecutionID][rule.ID] = rule
						}
					}
				}
				definitions = byExecution[attempt.ExecutionID]
			}
			for _, id := range attempt.RulesApplied {
				entry := rules[id]
				if rule, ok := definitions[id]; ok {
					entry.Title = rule.Title
				}
				rules[id] = entry
			}
			for _, change := range attempt.Changes {
				if !attempt.AdoptedChanges() || change.Status != "fixed" || change.RuleID == "" {
					continue
				}
				entry := rules[change.RuleID]
				if rule, ok := definitions[change.RuleID]; ok {
					entry.Title = rule.Title
				}
				if strings.TrimSpace(change.Change) != "" {
					entry.Changes = append(entry.Changes, strings.TrimSpace(change.Change))
				}
				rules[change.RuleID] = entry
			}
			for _, review := range attempt.Reviews {
				if (review.Verdict != "passed" && !(attempt.Partial && attempt.AdoptedChanges() && review.Verdict == "passed_with_holds")) || review.CandidateHash != attempt.OutputHash || review.BaseHash != attempt.InputHash || attempt.OutputHash == "" {
					continue
				}
				for _, assessment := range review.Assessments {
					entry := rules[assessment.RuleID]
					entry.Assessment = &assessment
					rules[assessment.RuleID] = entry
				}
			}
		}
		report.Rules[task.File] = rules
	}
	return report
}

func publicationCommitMessage(files []model.ResultPublicationFile, report publicationReport) (string, error) {
	if len(files) == 0 {
		return "", nil
	}
	changed := map[string]bool{}
	for _, file := range files {
		changed[file.File] = true
	}
	total, unchanged, partialHeld := len(files), 0, 0
	tasksByFile := make(map[string]model.Task, len(report.Tasks))
	for _, task := range report.Tasks {
		tasksByFile[task.File] = task
		if changed[task.File] && task.Status != "done" && task.Status != "skipped" {
			history := repairHistory(task)
			if len(history) > 0 {
				for _, change := range history[len(history)-1].Changes {
					if change.Status == "needs_human" {
						partialHeld++
						break
					}
				}
			}
		}
		if changed[task.File] || (task.Excluded && len(repairHistory(task)) == 0) {
			continue
		}
		total++
		if task.Status == "skipped" {
			unchanged++
		}
	}
	var body strings.Builder
	fmt.Fprintf(&body, "# 全体サマリー\n\n対象ファイル数：%d\n修正済みファイル数：%d\n修正不要ファイル数：%d\n", total, len(files), unchanged)
	if partialHeld > 0 {
		fmt.Fprintf(&body, "修正済みのうち要確認が残るファイル数：%d\n", partialHeld)
	}
	if remaining := total - len(files) - unchanged; remaining > 0 {
		fmt.Fprintf(&body, "未完了・要確認ファイル数：%d\n", remaining)
	}
	body.WriteString("\n# 修正一覧\n")
	for _, file := range files {
		path := filepath.ToSlash(filepath.Join(report.SourceRelative, file.File))
		label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(path)
		link := (&url.URL{Path: path}).EscapedPath()
		fmt.Fprintf(&body, "\n---\n\n## 修正ファイル\n\n[%s](<%s>)\n\n### 修正概要\n\n%s\n\n### 適用ルール一覧\n", label, link, file.Summary)
		if len(file.RulesApplied) == 0 {
			body.WriteString("\nルール別の対応記録なし\n")
		}
		for _, id := range file.RulesApplied {
			rule := report.Rules[file.File][id]
			title := strings.TrimSpace(rule.Title)
			if title == "" {
				title = "Titleの記録なし"
			}
			title = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(title)
			fmt.Fprintf(&body, "\n#### %s：%s\n\n**修正の内容**\n\n", id, title)
			if len(rule.Changes) == 0 {
				body.WriteString("修正内容の記録なし\n")
			} else {
				for _, change := range rule.Changes {
					fmt.Fprintf(&body, "- %s\n", strings.ReplaceAll(change, "\n", "\n  "))
				}
			}
			body.WriteString("\n**修正後レビュー結果**\n\n")
			if rule.Assessment == nil {
				body.WriteString("独立レビューの記録なし\n")
			} else {
				fmt.Fprintf(&body, "%s\n", rule.Assessment.Reason)
			}
		}
		// A published safe subset is not a claim that the whole file is complete.
		// Include its outstanding decisions separately from the applied rules.
		if task, exists := tasksByFile[file.File]; exists && task.Status != "done" && task.Status != "skipped" {
			history := repairHistory(task)
			printed := false
			var outstanding []model.ChangeReportItem
			if len(history) > 0 {
				outstanding = history[len(history)-1].Changes
			}
			for _, change := range outstanding {
				if change.Status != "needs_human" {
					continue
				}
				if !printed {
					body.WriteString("\n### 要確認の箇所\n\n")
					printed = true
				}
				fmt.Fprintf(&body, "- %s（%s）：%s\n", change.RuleID, change.Location, strings.ReplaceAll(change.Reason, "\n", "\n  "))
			}
		}
		if body.Len() > maxPublicationMessageBytes {
			return "", fmt.Errorf("コミット本文が上限（64 MiB）を超えています。省略せず出力するため、対象を分割してください")
		}
	}
	return strings.TrimSpace(body.String()), nil
}
