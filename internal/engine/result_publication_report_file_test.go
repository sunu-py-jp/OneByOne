package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"onebyone/internal/agent"
	"onebyone/internal/model"
)

func TestPublicationReportFileModesPreserveCheckoutsAndRecover(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"src/A.txt": "Legacy.Save()\n"})
	s.propose = successfulProposal
	st := runTest(t, s, 0)
	p, err := s.GetResultPublicationPreview()
	if err != nil {
		t.Fatal(err)
	}
	if p.MessageFileThreshold != 10000 {
		t.Fatalf("wrong UI threshold: %d", p.MessageFileThreshold)
	}
	sourceHead, workerHead := gitTest(t, cfg.Root, "rev-parse", "HEAD"), gitTest(t, st.Worktree, "rev-parse", "HEAD")
	writeTest(t, filepath.Join(cfg.Root, "personal.txt"), []byte("personal staging\n"))
	gitTest(t, cfg.Root, "add", "personal.txt")
	indexTree := gitTest(t, cfg.Root, "write-tree")
	for _, mode := range []struct {
		name              string
		message           string
		checked, wantFile bool
	}{
		{"manual", "# 編集した本文\n\n[src/A.txt](<src/A.txt>)\n", true, true},
		{"boundary", strings.Repeat("✅", publicationMessageFileThreshold), false, false},
		{"forced", strings.Repeat("✅", publicationMessageFileThreshold+1), false, true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			req := publicationRequest(p)
			req.Branch = "review/" + mode.name
			req.Message, req.MessageAsFile = mode.message, mode.checked
			pub, err := s.PublishResults(req)
			if err != nil {
				t.Fatal(err)
			}
			if (pub.ReportPath != "") != mode.wantFile {
				t.Fatalf("wrong mode: %+v", pub)
			}
			if mode.wantFile {
				if !publicationReportPathPattern.MatchString(pub.ReportPath) || pub.FileCount != 1 {
					t.Fatalf("report path/count: %+v", pub)
				}
				report, err := publicationGitInput(context.Background(), cfg.Root, "", "cat-file", "blob", pub.Commit+":"+pub.ReportPath)
				if err != nil || report != publicationReportContent(mode.message, p.ReportFiles) {
					t.Fatalf("report lost edited content: %v", err)
				}
				if mode.name == "manual" && !strings.Contains(report, "(<../src/A.txt>)") {
					t.Fatal("report link does not resolve from OneByOne/")
				}
				changed := gitTest(t, cfg.Root, "diff", "--name-only", pub.SourceCommit, pub.Commit)
				if changed != pub.ReportPath || !strings.Contains(pub.Message, pub.ReportPath) {
					t.Fatal("published tree contains unintended changes or no report reference")
				}
				if _, err := os.Stat(filepath.Join(st.Worktree, filepath.FromSlash(pub.ReportPath))); !os.IsNotExist(err) {
					t.Fatal("report was written to live worktree")
				}
			} else if pub.Message != mode.message || pub.ReportBlob != "" {
				t.Fatal("unchecked message did not remain in commit body")
			}
			if gitTest(t, cfg.Root, "rev-parse", "HEAD") != sourceHead || gitTest(t, st.Worktree, "rev-parse", "HEAD") != workerHead || gitTest(t, cfg.Root, "write-tree") != indexTree || gitTest(t, st.Worktree, "status", "--porcelain") != "" {
				t.Fatal("publication mutated a checkout or index")
			}
			journal := resultPublicationJournal{Version: 1, Records: []resultPublicationRecord{{State: "prepared", Publication: pub}}}
			if err := writeOutputJSON(cfg, cfg.QueuePath+".publications.json", journal); err != nil {
				t.Fatal(err)
			}
			after, err := s.GetResultPublicationPreview()
			if err != nil || len(after.Publications) != 1 || after.Publications[0] != pub {
				t.Fatalf("report publication recovery: %v", err)
			}
			if mode.wantFile {
				pub.ReportPath = "OneByOne/20000101000000_results.md"
				if err := validatePreparedPublication(context.Background(), st.Worktree, pub); err == nil {
					t.Fatal("recovery accepted a different report path")
				}
			}
		})
	}
}

func TestPublicationReportKeepsExistingFilesAndHandlesTimestampCollision(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", "OneByOne/20260928123456_results.md": "existing report", "OneByOne/notes.txt": "existing notes"})
	defer s.Close()
	source := gitTest(t, cfg.Root, "rev-parse", "HEAD")
	timestamp := time.Date(2026, 9, 28, 12, 34, 56, 0, time.Local)
	tree, reportPath, _, err := createPublicationReport(context.Background(), cfg.Root, source, "new report", nil, timestamp)
	if err != nil || reportPath != "OneByOne/20260928123457_results.md" {
		t.Fatalf("timestamp collision: %s %v", reportPath, err)
	}
	if gitTest(t, cfg.Root, "show", tree+":OneByOne/20260928123456_results.md") != "existing report" || gitTest(t, cfg.Root, "show", tree+":OneByOne/notes.txt") != "existing notes" {
		t.Fatal("existing report files overwritten")
	}
	if gitTest(t, cfg.Root, "diff", "--name-only", source, tree) != reportPath {
		t.Fatal("unrelated tree entry changed")
	}
}

func TestPublicationReportRefusesDirectoryConflicts(t *testing.T) {
	for _, conflict := range []string{"OneByOne", "onebyone/notes.txt"} {
		t.Run(conflict, func(t *testing.T) {
			s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n", conflict: "user content"})
			defer s.Close()
			source := gitTest(t, cfg.Root, "rev-parse", "HEAD")
			if _, _, _, err := createPublicationReport(context.Background(), cfg.Root, source, "new report", nil, time.Now()); err == nil {
				t.Fatal("report creation accepted a conflicting path")
			}
			if gitTest(t, cfg.Root, "rev-parse", "HEAD") != source || gitTest(t, cfg.Root, "status", "--porcelain") != "" {
				t.Fatal("conflict changed checkout")
			}
		})
	}
}

func TestPublicationReportLinksPreserveMarkdownAndEditedWhitespace(t *testing.T) {
	files := []model.ResultPublicationFile{{LinkPath: "src/a b[1].js"}}
	link := "[src/a b\\[1\\].js](<src/a%20b%5B1%5D.js>)"
	message := "  # 編集\r\n" + link + "\r\n\n```md\n" + link + "\n```\n\n末尾  "
	got := publicationReportContent(message, files)
	want := strings.Replace(message, "](<src/", "](<../src/", 1)
	if got != want {
		t.Fatalf("unexpected Markdown edits:\n%q\nwant\n%q", got, want)
	}
}

func TestPublicationReportSummaryTableLinksResolveFromReportDirectory(t *testing.T) {
	const file = "src/a|b [1].js"
	tableLink := publicationFileLink(file, file, true)
	if !strings.Contains(tableLink, "&#124;") || strings.Contains(tableLink, "a|b") {
		t.Fatal("file name breaks the Markdown table columns")
	}
	row := "| " + tableLink + " | ✅完了 |\r\n"
	message := "| ファイル名 | ステータス |\r\n| --- | --- |\r\n" + row + "\n```md\n" + row + "```\n"
	got := publicationReportContent(message, []model.ResultPublicationFile{{LinkPath: file}})
	want := strings.Replace(message, "](<src/", "](<../src/", 1)
	if got != want {
		t.Fatalf("summary link or code example rewritten incorrectly:\n%q\nwant\n%q", got, want)
	}
}

func TestPublicationReportRefusesSymlinkAndCaseDuplicateTrees(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	defer s.Close()
	ctx := context.Background()
	root, err := readPublicationTree(ctx, cfg.Root, "HEAD^{tree}")
	if err != nil {
		t.Fatal(err)
	}
	emptyTree, err := writePublicationTree(ctx, cfg.Root, nil)
	if err != nil {
		t.Fatal(err)
	}
	linkBlob, err := publicationGitInput(ctx, cfg.Root, "../personal", "hash-object", "-w", "--stdin")
	if err != nil {
		t.Fatal(err)
	}
	for _, entries := range [][]publicationTreeEntry{
		{{"120000", "blob", trim(linkBlob), "OneByOne"}},
		{{"040000", "tree", emptyTree, "OneByOne"}, {"040000", "tree", emptyTree, "onebyone"}},
	} {
		// Synthetic Git trees exercise Windows-incompatible names without relying
		// on the test host's filesystem case sensitivity or symlink permissions.
		tree, err := writePublicationTree(ctx, cfg.Root, append(append([]publicationTreeEntry{}, root...), entries...))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := createPublicationReport(ctx, cfg.Root, tree, "report", nil, time.Now()); err == nil || !strings.Contains(err.Error(), "競合") {
			t.Fatalf("conflicting report directory was accepted: %v", err)
		}
	}
}

func TestPublicationReportOnlyCommitForUnchangedResults(t *testing.T) {
	s, cfg := fixture(t, map[string]string{"A.txt": "Legacy.Save()\n"})
	s.propose = func(_ context.Context, in agent.Input) (model.Proposal, error) {
		return reviewedNoChangeProposal(t, in, "追加の変更は不要です")
	}
	runTest(t, s, 0)
	p, err := s.GetResultPublicationPreview()
	if err != nil || len(p.Files) != 0 || len(p.ReportFiles) != 1 {
		t.Fatalf("unchanged report preview: %+v %v", p, err)
	}
	if diff, err := s.GetResultPublicationFileDiff(p.WorkspaceID, p.Revision, "A.txt"); err != nil || diff != "" {
		t.Fatalf("unchanged report diff: %q %v", diff, err)
	}
	req := publicationRequest(p)
	if _, err := s.PublishResults(req); err == nil {
		t.Fatal("empty code commit without a report was allowed")
	}
	req.MessageAsFile = true
	pub, err := s.PublishResults(req)
	if err != nil || pub.ReportPath == "" || pub.FileCount != 0 {
		t.Fatalf("report-only publication: %+v %v", pub, err)
	}
	if gitTest(t, cfg.Root, "diff", "--name-only", p.BaseCommit, pub.Commit) != pub.ReportPath {
		t.Fatal("report-only publication changed code")
	}
	journal := resultPublicationJournal{Version: 1, Records: []resultPublicationRecord{{State: "prepared", Publication: pub}}}
	if err := writeOutputJSON(cfg, cfg.QueuePath+".publications.json", journal); err != nil {
		t.Fatal(err)
	}
	after, err := s.GetResultPublicationPreview()
	if err != nil || len(after.Publications) != 1 || after.Publications[0] != pub {
		t.Fatalf("report-only recovery: %v", err)
	}
}

func TestPublicationReportDiffDisplaysShiftJISWithoutChangingCommittedBytes(t *testing.T) {
	const original = "Legacy.Save()\n// \x93\xfa\x96\x7b\n" // 日本 in Shift_JIS
	s, cfg := fixture(t, map[string]string{"A.txt": original})
	s.propose = successfulProposal
	runTest(t, s, 0)
	p, err := s.GetResultPublicationPreview()
	if err != nil {
		t.Fatal(err)
	}
	diff, err := s.GetResultPublicationFileDiff(p.WorkspaceID, p.Revision, "A.txt")
	if err != nil || !strings.Contains(diff, "日本") || !strings.Contains(diff, "+Modern.Save()") {
		t.Fatalf("Shift_JIS diff display: %q %v", diff, err)
	}
	req := publicationRequest(p)
	req.MessageAsFile = true
	pub, err := s.PublishResults(req)
	if err != nil {
		t.Fatal(err)
	}
	content, err := git(context.Background(), cfg.Root, "cat-file", "blob", pub.Commit+":A.txt")
	if err != nil || content != strings.Replace(original, "Legacy", "Modern", 1) {
		t.Fatalf("publication changed source encoding: %q %v", content, err)
	}
}
