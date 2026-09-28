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
	"unicode/utf8"

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
	p := model.ResultPublicationPreview{WorkspaceID: snapshot.workspaceID, MessageFileThreshold: publicationMessageFileThreshold, Files: []model.ResultPublicationFile{}, ReportFiles: []model.ResultPublicationFile{}, Publications: []model.ResultPublication{}}
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
	report := publicationReportContext(snapshot)
	p.ReportFiles = publicationReportFiles(p.Files, report)
	p.Message, err = publicationCommitMessage(p.Files, report)
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
	for _, candidate := range preview.ReportFiles {
		if candidate.File != file {
			continue
		}
		rel := filepath.ToSlash(filepath.Join(snapshot.meta.SourceRelative, file))
		diff, err := git(ctx, snapshot.meta.Worktree, "diff", "--no-ext-diff", "--no-textconv", "--no-color", "--no-renames", preview.BaseCommit, preview.SourceCommit, "--", rel)
		if err != nil || diff == "" {
			return diff, err
		}
		before, err := git(ctx, snapshot.meta.Worktree, "cat-file", "blob", preview.BaseCommit+":"+rel)
		if err != nil {
			return "", err
		}
		after, err := git(ctx, snapshot.meta.Worktree, "cat-file", "blob", preview.SourceCommit+":"+rel)
		if err != nil {
			return "", err
		}
		return sourceDiffDisplay([]byte(diff), []byte(before), []byte(after))
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
	if strings.ContainsRune(req.Message, '\x00') || !utf8.ValidString(req.Message) || len(req.Title) > 1024 {
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
	messageAsFile := req.MessageAsFile || utf8.RuneCountInString(req.Message) > publicationMessageFileThreshold
	if len(preview.Files) == 0 && !(messageAsFile && len(preview.ReportFiles) > 0) {
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
	body := strings.TrimSpace(req.Message)
	reportPath, reportBlob := "", ""
	if messageAsFile {
		tree, reportPath, reportBlob, err = createPublicationReport(ctx, snapshot.meta.Worktree, preview.SourceCommit, req.Message, preview.ReportFiles, time.Now())
		if err != nil {
			return result, err
		}
		body = fmt.Sprintf("処理結果の詳細: [%s](%s)", reportPath, reportPath)
	}
	message := req.Title + "\n"
	if body != "" {
		message += "\n" + body + "\n"
	}
	commit, err := publicationGitInput(ctx, snapshot.meta.Worktree, message, "commit-tree", trim(tree), "-p", preview.BaseCommit, "-F", "-")
	if err != nil {
		return result, err
	}
	result = model.ResultPublication{Branch: req.Branch, Commit: trim(commit), BaseCommit: preview.BaseCommit, SourceCommit: preview.SourceCommit, CreatedAt: now(), FileCount: len(preview.Files), Title: req.Title, Message: body, ReportPath: reportPath, ReportBlob: reportBlob}
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
		hasReport := p.ReportPath != "" || p.ReportBlob != ""
		if hasReport && (!publicationReportPathPattern.MatchString(p.ReportPath) || !isImmutableCommitID(p.ReportBlob)) {
			return journal, fmt.Errorf("結果ファイルの記録が不正です")
		}
		if !isImmutableCommitID(p.Commit) || !isImmutableCommitID(p.BaseCommit) || !isImmutableCommitID(p.SourceCommit) || p.BaseCommit != m.BaseCommit || p.Branch == "" || p.FileCount < 0 || (p.FileCount == 0 && !hasReport) {
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
	if p.ReportPath != "" || p.ReportBlob != "" {
		tree, err = publicationTreeWithReport(ctx, repo, p.SourceCommit, p.ReportPath, p.ReportBlob)
		if err != nil {
			return err
		}
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

const publicationMessageFileThreshold = 10000

type publicationRuleReport struct {
	Changes    []string
	Holds      []string
	Unchanged  []string
	Fixed      bool
	Assessment *model.ReviewAssessment
}

type publicationReport struct {
	Tasks           []model.Task
	SourceRelative  string
	Rules           map[string]map[string]publicationRuleReport
	ReviewSummaries map[string]string
}

func publicationReportContext(snapshot resultPublicationSnapshot) publicationReport {
	report := publicationReport{Tasks: snapshot.tasks, SourceRelative: snapshot.meta.SourceRelative, Rules: map[string]map[string]publicationRuleReport{}, ReviewSummaries: map[string]string{}}
	for _, task := range snapshot.tasks {
		rules := map[string]publicationRuleReport{}
		history := repairHistory(task)
		latest := publicationLatestAttempt(history)
		for index, attempt := range history {
			adopted := attempt.AdoptedChanges()
			unchanged := attempt.Outcome == "skipped"
			isLatest := index == latest
			if !adopted && !unchanged && !isLatest {
				continue
			}
			// Applied history accumulates. Failed/rejected candidates never become fixes.
			if adopted {
				if len(attempt.Changes) == 0 {
					for _, id := range attempt.RulesApplied {
						if id == "" {
							continue
						}
						entry := rules[id]
						entry.Fixed = true
						rules[id] = entry
					}
				}
				for _, change := range attempt.Changes {
					if change.Status != "fixed" {
						continue
					}
					entry := rules[change.RuleID]
					entry.Fixed = true
					entry.Changes = publicationAppendUnique(entry.Changes, change.Change)
					rules[change.RuleID] = entry
				}
			}
			// Only the latest attempt contributes unresolved work. A retry which fixes
			// a previous hold must not leave the old hold in the commit report.
			if isLatest {
				for _, change := range attempt.Changes {
					entry := rules[change.RuleID]
					switch change.Status {
					case "needs_human":
						text := strings.TrimSpace(change.Change)
						reason := strings.TrimSpace(change.Reason)
						if reason != "" && reason != text {
							if text != "" {
								text += " — "
							}
							text += reason
						}
						if text == "" {
							text = firstReportText(change.Location, attempt.Note, "判断が保留されています")
						}
						entry.Holds = publicationAppendUnique(entry.Holds, text)
						// A previous approval is not evidence that this new hold was reviewed.
						entry.Assessment = nil
					case "unchanged":
						entry.Unchanged = publicationAppendUnique(entry.Unchanged, firstReportText(change.Reason, "修正不要と判断しました"))
					default:
						continue
					}
					rules[change.RuleID] = entry
				}
			}
			if review := publicationAttemptReview(attempt, isLatest); review != nil {
				report.ReviewSummaries[task.File] = review.Summary
				for _, assessment := range review.Assessments {
					entry := rules[assessment.RuleID]
					if !isLatest && !entry.Fixed {
						continue
					}
					copy := assessment
					entry.Assessment = &copy
					if isLatest && (assessment.Status == "needs_human" || assessment.Status == "needs_changes" || assessment.Status == "violated") && len(entry.Holds) == 0 {
						entry.Holds = publicationAppendUnique(entry.Holds, firstReportText(assessment.Reason, "独立レビューで確認が必要と判断しました"))
					}
					// Assessments of unaffected rules are useful for skipped files too, but
					// are not advertised as applied fixes.
					if !entry.Fixed && len(entry.Holds) == 0 && (assessment.Status == "satisfied" || assessment.Status == "not_applicable") {
						entry.Unchanged = publicationAppendUnique(entry.Unchanged, "修正不要と判断しました")
					}
					rules[assessment.RuleID] = entry
				}
			} else if isLatest && !adopted && !unchanged {
				// An unreviewed final hold must not display an older whole-file approval.
				delete(report.ReviewSummaries, task.File)
			}
		}
		report.Rules[task.File] = rules
	}
	return report
}

func publicationLatestAttempt(history []model.Attempt) int {
	for i := len(history) - 1; i >= 0; i-- {
		switch history[i].Outcome {
		case "", "running", "validated":
			continue
		default:
			return i
		}
	}
	return -1
}

// Select the latest completed review for this attempt's identified candidate.
// Rejections are included only for the current held/failed attempt, never as
// approval evidence for an adopted file. In-flight/error reviews are ignored.
func publicationAttemptReview(attempt model.Attempt, latest bool) *model.IndependentReview {
	for i := len(attempt.Reviews) - 1; i >= 0; i-- {
		review := attempt.Reviews[i]
		if attempt.InputHash == "" || review.BaseHash != attempt.InputHash || review.CandidateHash == "" {
			continue
		}
		accepted := attempt.AdoptedChanges() || attempt.Outcome == "skipped"
		if accepted {
			if attempt.OutputHash == "" || review.CandidateHash != attempt.OutputHash {
				continue
			}
			if review.Verdict != "passed" && !(attempt.Partial && attempt.AdoptedChanges() && review.Verdict == "passed_with_holds") {
				continue
			}
		} else {
			if !latest || (attempt.Outcome != "needs_human" && attempt.Outcome != "failed" && attempt.Outcome != "interrupted") {
				continue
			}
			if review.Verdict != "needs_human" && review.Verdict != "needs_changes" {
				continue
			}
			if attempt.OutputHash != "" && review.CandidateHash != attempt.OutputHash {
				continue
			}
		}
		return &review
	}
	return nil
}

func publicationAppendUnique(values []string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, previous := range values {
		if previous == value {
			return values
		}
	}
	return append(values, value)
}

// Report-only paths are deliberately separate from the actual changed-file
// set used to construct the commit. Unchanged and held-only files still need
// navigable explanations without claiming they were committed changes.
func publicationReportFiles(files []model.ResultPublicationFile, report publicationReport) []model.ResultPublicationFile {
	result := make([]model.ResultPublicationFile, 0, len(files)+len(report.Tasks))
	positions := map[string]int{}
	for _, file := range files {
		if _, exists := positions[file.File]; exists {
			continue
		}
		file.LinkPath = filepath.ToSlash(filepath.Join(report.SourceRelative, file.File))
		positions[file.File] = len(result)
		result = append(result, file)
	}
	for _, task := range report.Tasks {
		history := repairHistory(task)
		latest := publicationLatestAttempt(history)
		status := task.Status
		if latest >= 0 {
			status = history[latest].Outcome
		}
		processed := status == "done" || status == "skipped" || status == "needs_human" || status == "failed" || status == "interrupted"
		position, alreadyChanged := positions[task.File]
		if !alreadyChanged && (!processed || task.Excluded && len(history) == 0) {
			continue
		}
		if !alreadyChanged {
			file := publicationFileSummary(task)
			file.LinkPath = filepath.ToSlash(filepath.Join(report.SourceRelative, task.File))
			position = len(result)
			positions[task.File] = position
			result = append(result, file)
		}
		if latest >= 0 && strings.TrimSpace(history[latest].Note) != "" {
			result[position].Summary = history[latest].Note
		} else if !alreadyChanged {
			result[position].Summary = firstReportText(task.Note, publicationFileFallbackSummary(status))
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].File < result[j].File })
	return result
}

func publicationFileFallbackSummary(status string) string {
	switch status {
	case "skipped":
		return "修正不要と判断しました"
	case "needs_human", "failed", "interrupted":
		return "確認が必要です"
	default:
		return "修正を完了しました"
	}
}

func publicationFileHeld(task model.Task, rules map[string]publicationRuleReport) bool {
	for _, rule := range rules {
		if len(rule.Holds) > 0 {
			return true
		}
	}
	history := repairHistory(task)
	if latest := publicationLatestAttempt(history); latest >= 0 {
		switch history[latest].Outcome {
		case "needs_human", "failed", "interrupted":
			return true
		case "done", "skipped":
			return false
		}
	}
	return task.Status == "needs_human" || task.Status == "failed" || task.Status == "interrupted"
}

func publicationTableCell(text string) string {
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "`", "&#96;", "\\", "\\\\", "|", "&#124;").Replace(text)
	return strings.ReplaceAll(text, "\n", "<br>")
}

func publicationRuleStatus(rule publicationRuleReport) string {
	if len(rule.Holds) > 0 {
		if rule.Fixed {
			return "⚠️一部修正完了/要確認"
		}
		return "⚠️要確認"
	}
	if rule.Fixed {
		return "✅完了"
	}
	if len(rule.Unchanged) > 0 {
		return "☑️修正不要"
	}
	return "⚠️記録なし"
}

func publicationFileStatus(changed, held bool) (int, string) {
	if held {
		if changed {
			return 1, "⚠️一部修正済み要確認"
		}
		return 1, "⚠️要確認"
	}
	if changed {
		return 0, "✅完了"
	}
	return 2, "☑️修正不要"
}

func publicationFileLink(labelPath, targetPath string, table bool) string {
	label := strings.NewReplacer("\\", "\\\\", "[", "\\[", "]", "\\]").Replace(labelPath)
	if table {
		label = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "`", "&#96;", "|", "&#124;", "\r", "&#13;", "\n", "&#10;").Replace(label)
	}
	return fmt.Sprintf("[%s](<%s>)", label, (&url.URL{Path: targetPath}).EscapedPath())
}

func publicationCommitMessage(files []model.ResultPublicationFile, report publicationReport) (string, error) {
	reportFiles := publicationReportFiles(files, report)
	if len(reportFiles) == 0 {
		return "", nil
	}
	changed := map[string]bool{}
	for _, file := range files {
		changed[file.File] = true
	}
	tasks := map[string]model.Task{}
	for _, task := range report.Tasks {
		tasks[task.File] = task
	}
	fileRanks, fileLabels := map[string]int{}, map[string]string{}
	unchanged, held, partial := 0, 0, 0
	for _, file := range reportFiles {
		needsHuman := publicationFileHeld(tasks[file.File], report.Rules[file.File])
		fileRanks[file.File], fileLabels[file.File] = publicationFileStatus(changed[file.File], needsHuman)
		if needsHuman {
			held++
			if changed[file.File] {
				partial++
			}
		} else if !changed[file.File] {
			unchanged++
		}
	}
	sort.SliceStable(reportFiles, func(i, j int) bool {
		if fileRanks[reportFiles[i].File] != fileRanks[reportFiles[j].File] {
			return fileRanks[reportFiles[i].File] < fileRanks[reportFiles[j].File]
		}
		return reportFiles[i].File < reportFiles[j].File
	})
	var body strings.Builder
	heldSummary := fmt.Sprintf("⚠️ 要確認：%d", held)
	if partial > 0 {
		heldSummary += fmt.Sprintf("（うち一部修正済み：%d）", partial)
	}
	summaryLines := []string{
		fmt.Sprintf("📄 対象ファイル数：%d", len(reportFiles)),
		fmt.Sprintf("✅ 修正完了：%d", len(changed)-partial),
		heldSummary,
		fmt.Sprintf("☑️ 修正不要：%d", unchanged),
	}
	// Markdown soft newlines collapse into spaces. Hard breaks also survive when
	// this body is viewed outside the app in the committed Markdown report.
	fmt.Fprintf(&body, "# 全体サマリー\n\n%s\n\n# 修正サマリー\n\n| ファイル名 | ステータス |\n| --- | --- |\n", strings.Join(summaryLines, "  \n"))
	for _, file := range reportFiles {
		fmt.Fprintf(&body, "| %s | %s |\n", publicationFileLink(file.LinkPath, file.LinkPath, true), fileLabels[file.File])
	}
	body.WriteString("\n# 修正一覧\n")
	for _, file := range reportFiles {
		rules := report.Rules[file.File]
		status := fileLabels[file.File]
		path := file.LinkPath
		summary := firstReportText(file.Summary, "修正概要の記録なし")
		reviewSummary := firstReportText(report.ReviewSummaries[file.File], "独立レビューの記録なし")
		fmt.Fprintf(&body, "\n---\n\n## ファイル　%s\n\n%s\n\n### 修正概要\n\n%s\n\n### レビュー結果\n\n%s\n\n### 一覧\n\n| ルールID | ステータス | 修正内容 | レビュー結果 |\n| --- | --- | --- | --- |\n", status, publicationFileLink(path, path, false), summary, reviewSummary)
		rows := map[string]publicationRuleReport{}
		for id, rule := range rules {
			rows[id] = rule
		}
		for _, id := range file.RulesApplied {
			if _, exists := rows[id]; !exists {
				rows[id] = publicationRuleReport{Fixed: changed[file.File]}
			}
		}
		if len(rows) == 0 {
			note := "ルール別の対応記録なし"
			fmt.Fprintf(&body, "| — | %s | %s | 独立レビューの記録なし |\n", status, note)
			continue
		}
		ids := make([]string, 0, len(rows))
		for id := range rows {
			ids = append(ids, id)
		}
		rank := func(rule publicationRuleReport) int {
			if len(rule.Holds) > 0 || (!rule.Fixed && len(rule.Unchanged) == 0) {
				return 1
			}
			if rule.Fixed {
				return 0
			}
			return 2
		}
		sort.Slice(ids, func(i, j int) bool {
			if rank(rows[ids[i]]) != rank(rows[ids[j]]) {
				return rank(rows[ids[i]]) < rank(rows[ids[j]])
			}
			return ids[i] < ids[j]
		})
		for _, id := range ids {
			rule := rows[id]
			changes := append([]string{}, rule.Changes...)
			if rule.Fixed && len(changes) == 0 {
				changes = append(changes, "修正内容の記録なし")
			}
			for _, hold := range rule.Holds {
				changes = append(changes, "保留："+hold)
			}
			if !rule.Fixed && len(rule.Holds) == 0 {
				changes = append(changes, rule.Unchanged...)
			}
			if len(changes) == 0 {
				changes = append(changes, "対応内容の記録なし")
			}
			reason := "独立レビューの記録なし"
			if rule.Assessment != nil {
				reason = rule.Assessment.Reason
			}
			displayID := id
			if displayID == "" {
				displayID = "—"
			}
			fmt.Fprintf(&body, "| %s | %s | %s | %s |\n", publicationTableCell(displayID), publicationRuleStatus(rule), publicationTableCell(strings.Join(changes, "\n")), publicationTableCell(reason))
		}
	}
	return strings.TrimSpace(body.String()), nil
}
