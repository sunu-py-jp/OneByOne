package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func publicationRequest(p model.ResultPublicationPreview) model.PublishResultsRequest {
	return model.PublishResultsRequest{WorkspaceID: p.WorkspaceID, Revision: p.Revision, Branch: p.SuggestedBranch, Title: "Adopt reviewed improvements", Message: p.Message}
}

func TestResultPublicationSquashesCumulativeChangesWithoutCheckout(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n", "src/B.txt": "Legacy.Save()\n", "src/deep/C.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	if st.LastError != "" {
		t.Fatal(st.LastError)
	}
	// Distinguish actual adopted fixes from common-rule compliance. A no-op
	// retry must retain the prior adopted rule and must not claim skipped rules.
	s.mu.Lock()
	for i := range s.state.Tasks {
		h := &s.state.Tasks[i].History[0]
		h.Changes = []model.ChangeReportItem{{ID: "fix", RuleID: "R019", RuleTitle: "Save update", Change: "Replace the obsolete save call", Status: "fixed"}, {ID: "rule:R001", RuleID: "R001", Status: "unchanged"}}
	}
	s.mu.Unlock()
	if err := s.persist(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RetryTasks([]string{"src/A.txt"}); err != nil {
		t.Fatal(err)
	}
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		return reviewedNoChangeProposal(t, in, "already updated")
	}
	st = runTest(t, s, 0)
	if st.LastError != "" {
		t.Fatal(st.LastError)
	}
	if len(st.Tasks[0].History) != 2 || st.Tasks[0].History[1].Outcome != "skipped" || st.Tasks[0].Status != "done" {
		t.Fatalf("retry must really be a no-op: %+v", st.Tasks[0])
	}
	if _, err := s.DiscardFileChanges("src/B.txt"); err != nil {
		t.Fatal(err)
	}
	preview, err := s.GetResultPublicationPreview()
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Files) != 2 || preview.Revision == "" || len(preview.Publications) != 0 || preview.Files[0].File != "src/A.txt" || preview.Files[1].File != "src/deep/C.txt" {
		t.Fatalf("preview: %+v", preview)
	}
	for _, f := range preview.Files {
		if f.LinkPath != f.File || !strings.Contains(preview.Message, "](<"+f.LinkPath+">)") {
			t.Fatalf("preview link is not the generated message destination: %+v", f)
		}
		if !reflect.DeepEqual(f.RulesApplied, []string{"R019"}) || f.Diff != "" || (f.Summary != "migrated" && f.Summary != "already updated") {
			t.Fatalf("wrong attribution: %+v", f)
		}
	}
	if strings.Contains(preview.Message, "- R019: - R019:") {
		t.Fatal("duplicated rule ID in default message")
	}
	if strings.Contains(preview.Message, "src/B.txt") || strings.Contains(preview.Message, "R001") || strings.Contains(preview.Message, "R999") {
		t.Fatal(preview.Message)
	}
	diff, err := s.GetResultPublicationFileDiff(preview.WorkspaceID, preview.Revision, "src/A.txt")
	if err != nil || !strings.Contains(diff, "+Modern.Save()") || !strings.Contains(diff, "-Legacy.Save()") {
		t.Fatalf("lazy diff: %s %v", diff, err)
	}
	if _, err = s.GetResultPublicationFileDiff(preview.WorkspaceID, preview.Revision, "src/B.txt"); err == nil {
		t.Fatal("discarded file diff allowed")
	}
	originalHead := gitTest(t, cfg.Root, "rev-parse", "HEAD")
	originalBranch := gitTest(t, cfg.Root, "branch", "--show-current")
	originalStatus := gitTest(t, cfg.Root, "status", "--porcelain")
	worktreeHead := gitTest(t, st.Worktree, "rev-parse", "HEAD")
	worktreeBranch := gitTest(t, st.Worktree, "branch", "--show-current")
	// Source user changes do not enter the publication and must not be touched.
	writeTest(t, filepath.Join(cfg.Root, "private-notes.txt"), []byte("user note\n"))
	result, err := s.PublishResults(publicationRequest(preview))
	if err != nil {
		t.Fatal(err)
	}
	if result.BaseCommit != originalHead || result.SourceCommit != worktreeHead || result.FileCount != 2 || result.Commit == "" {
		t.Fatalf("publication: %+v", result)
	}
	if got := gitTest(t, cfg.Root, "rev-list", "--count", result.BaseCommit+".."+result.Commit); got != "1" {
		t.Fatalf("not one commit: %s", got)
	}
	if got := gitTest(t, cfg.Root, "show", "-s", "--format=%P", result.Commit); got != originalHead {
		t.Fatalf("wrong parent: %s", got)
	}
	if got := gitTest(t, cfg.Root, "rev-parse", result.Commit+"^{tree}"); got != gitTest(t, st.Worktree, "rev-parse", "HEAD^{tree}") {
		t.Fatal("publication tree differs from accepted final tree")
	}
	if got := gitTest(t, cfg.Root, "log", "-1", "--format=%B", result.Branch); got != result.Title+"\n\n"+result.Message {
		t.Fatalf("message: %q", got)
	}
	if gitTest(t, cfg.Root, "rev-parse", "HEAD") != originalHead || gitTest(t, cfg.Root, "branch", "--show-current") != originalBranch || gitTest(t, st.Worktree, "rev-parse", "HEAD") != worktreeHead || gitTest(t, st.Worktree, "branch", "--show-current") != worktreeBranch {
		t.Fatal("source or internal checkout changed")
	}
	if gitTest(t, st.Worktree, "status", "--porcelain") != "" || readTest(t, filepath.Join(cfg.Root, "src/A.txt")) != "Legacy.Save()\n" || readTest(t, filepath.Join(cfg.Root, "private-notes.txt")) != "user note\n" || originalStatus != "" {
		t.Fatal("files/index modified")
	}
	if strings.Contains(gitTest(t, cfg.Root, "worktree", "list", "--porcelain"), "branch refs/heads/"+result.Branch+"\n") {
		t.Fatal("publication branch is checked out")
	}
	// It is a normal unoccupied branch, so the source checkout can select it.
	gitTest(t, cfg.Root, "switch", result.Branch)
	if readTest(t, filepath.Join(cfg.Root, "src/A.txt")) != "Modern.Save()\n" || readTest(t, filepath.Join(cfg.Root, "src/B.txt")) != "Legacy.Save()\n" {
		t.Fatal("external Git checkout sees wrong result")
	}
	gitTest(t, cfg.Root, "switch", originalBranch)
	if err = os.Remove(filepath.Join(cfg.Root, "private-notes.txt")); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened := New(s.configPath)
	t.Cleanup(reopened.Close)
	after, err := reopened.GetResultPublicationPreview()
	if err != nil || len(after.Publications) != 1 || after.Publications[0] != result || after.SuggestedBranch == result.Branch {
		t.Fatalf("restart lost publication: %+v %v", after, err)
	}
}

func TestResultPublicationRefusesUnsafeState(t *testing.T) {
	for _, condition := range []string{"blank-title", "multiline-title", "trailing-newline-title", "invalid-branch", "existing-branch", "stale", "wrong-workspace", "running", "readonly", "dirty", "staged", "untracked", "foreign-commit", "reset", "wrong-worktree"} {
		t.Run(condition, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
			s.propose = successfulProposal
			st := runTest(t, s, 0)
			if st.LastError != "" {
				t.Fatal(st.LastError)
			}
			preview, err := s.GetResultPublicationPreview()
			if err != nil {
				t.Fatal(err)
			}
			req := publicationRequest(preview)
			switch condition {
			case "blank-title":
				req.Title = "  "
			case "multiline-title":
				req.Title = "first\nsecond"
			case "trailing-newline-title":
				req.Title = "first\n"
			case "invalid-branch":
				req.Branch = "not a branch"
			case "existing-branch":
				req.Branch = gitTest(t, cfg.Root, "branch", "--show-current")
			case "stale":
				req.Revision = "old"
			case "wrong-workspace":
				req.WorkspaceID = "other"
			case "running":
				s.state.Running = true
			case "readonly":
				s.state.ReadOnly = true
			case "dirty":
				writeTest(t, filepath.Join(st.Worktree, "A.txt"), []byte("personal edit\n"))
			case "staged":
				writeTest(t, filepath.Join(st.Worktree, "A.txt"), []byte("personal edit\n"))
				gitTest(t, st.Worktree, "add", "A.txt")
				writeTest(t, filepath.Join(st.Worktree, "A.txt"), []byte("Modern.Save()\n"))
			case "untracked":
				writeTest(t, filepath.Join(st.Worktree, "notes.txt"), []byte("personal edit\n"))
			case "foreign-commit":
				gitTest(t, st.Worktree, "commit", "--allow-empty", "-m", "external")
			case "reset":
				gitTest(t, st.Worktree, "reset", "--hard", "HEAD^")
			case "wrong-worktree":
				s.meta.Worktree = cfg.Root
			}
			head, refs, status := gitTest(t, st.Worktree, "rev-parse", "HEAD"), gitTest(t, cfg.Root, "show-ref"), gitTest(t, st.Worktree, "status", "--porcelain")
			if _, err := s.PublishResults(req); err == nil {
				t.Fatal("unsafe publication accepted")
			}
			s.state.Running = false
			if head != gitTest(t, st.Worktree, "rev-parse", "HEAD") || refs != gitTest(t, cfg.Root, "show-ref") || status != gitTest(t, st.Worktree, "status", "--porcelain") {
				t.Fatal("refused publication mutated Git state")
			}
		})
	}
}

func TestResultPublicationPreviewBecomesStaleAfterDiscard(t *testing.T) {
	s, _ := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "B.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	runTest(t, s, 0)
	preview, err := s.GetResultPublicationPreview()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.DiscardFileChanges("B.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.PublishResults(publicationRequest(preview)); err == nil {
		t.Fatal("stale preview accepted")
	}
	if _, err = s.GetResultPublicationFileDiff(preview.WorkspaceID, preview.Revision, "A.txt"); err == nil {
		t.Fatal("stale lazy diff accepted")
	}
}

func TestResultPublicationJournalRecoveryAndHistoricalBranches(t *testing.T) {
	for _, boundary := range []string{"before-ref", "after-ref", "different-ref", "deleted-done", "moved-done"} {
		t.Run(boundary, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
			s.propose = successfulProposal
			runTest(t, s, 0)
			preview, err := s.GetResultPublicationPreview()
			if err != nil {
				t.Fatal(err)
			}
			pub, err := s.PublishResults(publicationRequest(preview))
			if err != nil {
				t.Fatal(err)
			}
			state := "prepared"
			switch boundary {
			case "before-ref":
				gitTest(t, cfg.Root, "branch", "-D", pub.Branch)
			case "different-ref":
				gitTest(t, cfg.Root, "update-ref", "refs/heads/"+pub.Branch, pub.BaseCommit)
			case "deleted-done":
				state = "done"
				gitTest(t, cfg.Root, "branch", "-D", pub.Branch)
			case "moved-done":
				state = "done"
				gitTest(t, cfg.Root, "update-ref", "refs/heads/"+pub.Branch, pub.BaseCommit)
			}
			journal := resultPublicationJournal{Version: 1, Records: []resultPublicationRecord{{State: state, Publication: pub}}}
			if err = writeOutputJSON(cfg, cfg.QueuePath+".publications.json", journal); err != nil {
				t.Fatal(err)
			}
			s.Close()
			reopened := New(s.configPath)
			t.Cleanup(reopened.Close)
			recovered, err := reopened.GetResultPublicationPreview()
			if boundary == "different-ref" {
				if err == nil {
					t.Fatal("mismatching recovered ref accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := 1
			if boundary == "before-ref" {
				want = 0
			}
			if len(recovered.Publications) != want {
				t.Fatalf("recovered: %+v", recovered.Publications)
			}
			if want == 1 && recovered.Publications[0] != pub {
				t.Fatal("historical publication changed")
			}
		})
	}
}

func TestResultPublicationRecoveryIsDurableBeforeBranchMoves(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	runTest(t, s, 0)
	preview, err := s.GetResultPublicationPreview()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := s.PublishResults(publicationRequest(preview))
	if err != nil {
		t.Fatal(err)
	}
	journal := resultPublicationJournal{Version: 1, Records: []resultPublicationRecord{{State: "prepared", Publication: pub}}}
	if err = writeOutputJSON(cfg, cfg.QueuePath+".publications.json", journal); err != nil {
		t.Fatal(err)
	}
	s.Close()
	reopened := New(s.configPath)
	t.Cleanup(reopened.Close)
	recovered, err := reopened.GetResultPublicationPreview()
	if err != nil || len(recovered.Publications) != 1 {
		t.Fatalf("recover: %+v %v", recovered, err)
	}
	gitTest(t, cfg.Root, "update-ref", "refs/heads/"+pub.Branch, pub.BaseCommit)
	moved, err := reopened.GetResultPublicationPreview()
	if err != nil || len(moved.Publications) != 1 || moved.Publications[0] != pub {
		t.Fatalf("reconciled record wasn't persisted: %+v %v", moved, err)
	}
}

func TestResultPublicationAtomicRefTransactionRejectsRaces(t *testing.T) {
	for _, change := range []string{"source", "destination"} {
		t.Run(change, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
			s.propose = successfulProposal
			runTest(t, s, 0)
			preview, err := s.GetResultPublicationPreview()
			if err != nil {
				t.Fatal(err)
			}
			tree := gitTest(t, s.meta.Worktree, "rev-parse", preview.SourceCommit+"^{tree}")
			commit, err := publicationGitInput(context.Background(), s.meta.Worktree, "test\n", "commit-tree", tree, "-p", preview.BaseCommit, "-F", "-")
			if err != nil {
				t.Fatal(err)
			}
			p := model.ResultPublication{Branch: preview.SuggestedBranch, SourceCommit: preview.SourceCommit, Commit: trim(commit)}
			if change == "source" {
				gitTest(t, s.meta.Worktree, "commit", "--allow-empty", "-m", "external")
			} else {
				gitTest(t, cfg.Root, "branch", p.Branch, preview.BaseCommit)
			}
			refs := gitTest(t, cfg.Root, "show-ref")
			if err = createResultPublicationBranch(context.Background(), s.meta, p); err == nil {
				t.Fatal("racing ref update succeeded")
			}
			if refs != gitTest(t, cfg.Root, "show-ref") {
				t.Fatal("failed transaction changed refs")
			}
		})
	}
}

func TestResultPublicationRejectsTamperedPreparedCommit(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	runTest(t, s, 0)
	preview, err := s.GetResultPublicationPreview()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := s.PublishResults(publicationRequest(preview))
	if err != nil {
		t.Fatal(err)
	}
	pub.Title = "different message"
	journal := resultPublicationJournal{Version: 1, Records: []resultPublicationRecord{{State: "prepared", Publication: pub}}}
	if err = writeOutputJSON(cfg, cfg.QueuePath+".publications.json", journal); err != nil {
		t.Fatal(err)
	}
	if _, err = s.GetResultPublicationPreview(); err == nil {
		t.Fatal("inconsistent recovery accepted")
	}
}

func TestResultPublicationUTF8LargeMessageRecovery(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	runTest(t, s, 0)
	gitTest(t, cfg.Root, "config", "i18n.commitEncoding", "Shift_JIS")
	gitTest(t, cfg.Root, "config", "i18n.logOutputEncoding", "Shift_JIS")
	preview, err := s.GetResultPublicationPreview()
	if err != nil {
		t.Fatal(err)
	}
	req := publicationRequest(preview)
	req.Title = "日本語の修正結果"
	req.Message = strings.Repeat("x", (4<<20)+1024) + "\nファイルごとの変更概要"
	pub, err := s.PublishResults(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := publicationGitInputLimit(context.Background(), cfg.Root, "", 6<<20, "cat-file", "commit", pub.Commit)
	if err != nil || strings.Contains(raw, "encoding Shift_JIS") || !strings.Contains(raw, req.Title+"\n\n"+req.Message) {
		t.Fatalf("wrong raw UTF-8 commit encoding: %v", err)
	}
	journal := resultPublicationJournal{Version: 1, Records: []resultPublicationRecord{{State: "prepared", Publication: pub}}}
	if err = writeOutputJSON(cfg, cfg.QueuePath+".publications.json", journal); err != nil {
		t.Fatal(err)
	}
	recovered, err := s.GetResultPublicationPreview()
	if err != nil || len(recovered.Publications) != 1 || recovered.Publications[0].Message != req.Message {
		t.Fatalf("large-message recovery failed: %v", err)
	}
}

func TestResultPublicationFullSummaryPreservesEveryFileAndRule(t *testing.T) {
	files := []model.ResultPublicationFile{}
	summary := strings.Repeat("長い説明", 300) + "末尾まで保存"
	for i := 0; i < 10000; i++ {
		files = append(files, model.ResultPublicationFile{File: fmt.Sprintf("src/deep/component%05d.js", i), RulesApplied: []string{"R001", "R019"}, Summary: summary})
	}
	message, err := publicationCommitMessage(files, publicationReport{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(message, "\n---\n\n## 修正ファイル\n") != len(files) || strings.Count(message, "修正内容の記録なし") != len(files)*2 || strings.Count(message, summary) != len(files) || strings.Count(message, "#### R001：") != len(files) || strings.Count(message, "#### R019：") != len(files) {
		t.Fatal("large report silently dropped descriptions or rules")
	}
	for _, index := range []int{0, 4999, 9999} {
		if !strings.Contains(message, "["+files[index].File+"](<"+files[index].File+">)") {
			t.Fatalf("lost file link: %s", files[index].File)
		}
	}
}

func TestResultPublicationMarkdownIncludesSummaryTitlesReviewsAndCounts(t *testing.T) {
	const note = "複数行の修正概要\n次の行も保持する"
	const description = "見出しには表示しないDescription"
	const title = "旧APIを移行し\n契約を維持する"
	const reason = "呼び出し順序と戻り値を確認した。\n追加の削除は不要。"
	task := model.Task{File: "src/file (a)#1.ts", Status: "done", History: []model.Attempt{{Outcome: "done", Commit: "commit", Note: note, InputHash: "before", OutputHash: "after", RulesApplied: []string{"R001"}, Reviews: []model.IndependentReview{
		{Verdict: "passed", BaseHash: "before", CandidateHash: "after", Assessments: []model.ReviewAssessment{{RuleID: "R001", Status: "satisfied", Reason: reason}}},
		{Verdict: "needs_changes", BaseHash: "before", CandidateHash: "rejected", Assessments: []model.ReviewAssessment{{RuleID: "R001", Status: "needs_changes", Reason: "採用してはいけないレビュー"}}},
	}}}}
	task.History[0].Changes = []model.ChangeReportItem{
		{RuleID: "R001", Status: "fixed", Change: "古いReportWriterのimportを削除する。\n既存のreports importとの重複を避ける。"},
		{RuleID: "R001", Status: "fixed", Change: "出力成功後だけ完了イベントを送信する。"},
		{RuleID: "R001", Status: "unapplied", Change: "未反映なので記載しない修正"},
	}
	task.History = append(task.History,
		model.Attempt{Outcome: "done", Commit: "later-commit", Changes: []model.ChangeReportItem{{RuleID: "R001", Status: "fixed", Change: "例外時もリソースを解放する。"}}},
		model.Attempt{Outcome: "failed", Commit: "failed-commit", Changes: []model.ChangeReportItem{{RuleID: "R001", Status: "fixed", Change: "失敗案なので記載しない修正"}}},
		model.Attempt{Outcome: "done", Changes: []model.ChangeReportItem{{RuleID: "R001", Status: "fixed", Change: "未コミットなので記載しない修正"}}},
		model.Attempt{Outcome: "skipped", Changes: []model.ChangeReportItem{{RuleID: "R001", Status: "fixed", Change: "変更不要なので記載しない修正"}}},
	)
	snapshot := resultPublicationSnapshot{meta: manifest{SourceRelative: "project"}, tasks: []model.Task{task, {File: "unchanged.ts", Status: "skipped"}, {File: "held.ts", Status: "needs_human"}, {File: "excluded.ts", Status: "pending", Excluded: true}}, rules: []model.Rule{{ID: "R001", Title: title, Summary: description}}}
	// Editing the current rule must not rewrite the title of an adopted result.
	snapshot.config.QueuePath = filepath.Join(t.TempDir(), "queue.jsonl")
	snapshot.tasks[0].History[0].ExecutionID = strings.Repeat("a", 24)
	snapshot.tasks[0].History[1].ExecutionID = snapshot.tasks[0].History[0].ExecutionID
	record := executionRecord{Version: 1, Run: model.ExecutionRun{ID: snapshot.tasks[0].History[0].ExecutionID}, State: model.State{Config: snapshot.config, Rules: snapshot.rules}}
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := executionDirectory(snapshot.config.QueuePath, record.Run.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeTest(t, filepath.Join(directory, "snapshot.json"), data)
	snapshot.rules = []model.Rule{{ID: "R001", Title: "後から編集された名称", Summary: "後から編集された説明"}}
	file := publicationFileSummary(task)
	message, err := publicationCommitMessage([]model.ResultPublicationFile{file}, publicationReportContext(snapshot))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{"# 全体サマリー", "対象ファイル数：3", "修正済みファイル数：1", "修正不要ファイル数：1", "未完了・要確認ファイル数：1", "# 修正一覧", "[project/src/file (a)#1.ts](<project/src/file%20%28a%29%231.ts>)", "### 修正概要\n\n" + note, "\n---\n\n## 修正ファイル\n", "#### R001：旧APIを移行し 契約を維持する\n", "\n**修正の内容**\n\n", "古いReportWriterのimportを削除する。", "既存のreports importとの重複を避ける。", "出力成功後だけ完了イベントを送信する。", "例外時もリソースを解放する。", "\n**修正後レビュー結果**\n\n" + reason} {
		if !strings.Contains(message, expected) {
			t.Fatalf("missing %q in %s", expected, message)
		}
	}
	if strings.Contains(message, "記載しない修正") || strings.Contains(message, "採用してはいけないレビュー") || strings.Contains(message, "後から編集された説明") || strings.Contains(message, "後から編集された名称") || strings.Contains(message, description) || strings.Contains(message, "satisfied：") {
		t.Fatal("rejected candidate review entered commit report")
	}
}
