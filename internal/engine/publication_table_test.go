package engine

import (
	"strings"
	"testing"

	"onebyone/internal/model"
)

func publicationTableReview(base, after, verdict, summary string, assessments ...model.ReviewAssessment) model.IndependentReview {
	return model.IndependentReview{BaseHash: base, CandidateHash: after, Verdict: verdict, Summary: summary, Assessments: assessments}
}

func TestPublicationTableIncludesChangedUnchangedAndHeldFilesWithoutInflatingChanges(t *testing.T) {
	tasks := []model.Task{
		{File: "changed.js", Status: "done", History: []model.Attempt{
			{Outcome: "done", Commit: "adopted", InputHash: "old", OutputHash: "new", Changes: []model.ChangeReportItem{{RuleID: "R1", Status: "fixed", Change: "順序を変更した"}}},
			{Outcome: "skipped", InputHash: "new", OutputHash: "new", Note: "再確認でも追加変更は不要", Changes: []model.ChangeReportItem{{RuleID: "R1", Status: "unchanged", Reason: "前回の修正を保持"}}, Reviews: []model.IndependentReview{publicationTableReview("new", "new", "passed", "最終レビュー", model.ReviewAssessment{RuleID: "R1", Status: "satisfied", Reason: "順序を確認した"})}},
		}},
		{File: "unchanged.js", Status: "skipped", History: []model.Attempt{{Outcome: "skipped", InputHash: "same", OutputHash: "same", Note: "既存の実装で問題なし", Changes: []model.ChangeReportItem{{RuleID: "R2", Status: "unchanged", Reason: "既に適合している"}}, Reviews: []model.IndependentReview{publicationTableReview("same", "same", "passed", "変更不要の確認", model.ReviewAssessment{RuleID: "R2", Status: "not_applicable", Reason: "該当APIがない"})}}}},
		{File: "held.js", Status: "needs_human", History: []model.Attempt{{Outcome: "needs_human", Note: "所有者の確認が必要", Changes: []model.ChangeReportItem{{RuleID: "R3", Status: "needs_human", Change: "解放時点を決める", Reason: "契約が不明"}}}}},
		{File: "pending.js", Status: "pending"},
		{File: "excluded.js", Status: "skipped", Excluded: true},
	}
	report := publicationReportContext(resultPublicationSnapshot{meta: manifest{SourceRelative: "project"}, tasks: tasks})
	files := []model.ResultPublicationFile{publicationFileSummary(tasks[0])}
	reportFiles := publicationReportFiles(files, report)
	if len(files) != 1 || len(reportFiles) != 3 {
		t.Fatalf("changed=%d report=%d", len(files), len(reportFiles))
	}
	for _, file := range reportFiles {
		if file.Summary == "" || file.LinkPath != "project/"+file.File {
			t.Fatalf("report link/summary missing: %#v", file)
		}
	}
	body, err := publicationCommitMessage(files, report)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"対象ファイル数：3", "修正完了：1", "修正不要：1", "要確認：1", "## ファイル　✅完了", "## ファイル　☑️修正不要", "## ファイル　⚠️要確認", "### レビュー結果\n\n最終レビュー", "| R1 | ✅完了 | 順序を変更した | 順序を確認した |", "| R2 | ☑️修正不要 |", "| R3 | ⚠️要確認 | 保留：解放時点を決める — 契約が不明 |"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"pending.js", "excluded.js", "satisfied", "not_applicable", "needs_human"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("unwanted %q in report", unwanted)
		}
	}
}

func TestPublicationTableCombinesFixedAndLatestHoldAndEscapesCells(t *testing.T) {
	task := model.Task{File: "mixed.js", Status: "needs_human", History: []model.Attempt{
		{Outcome: "done", Commit: "first", Changes: []model.ChangeReportItem{{RuleID: "R1", Status: "fixed", Change: "初回の修正"}}},
		{Outcome: "needs_human", Partial: true, Commit: "second", InputHash: "base", OutputHash: "candidate", RulesApplied: []string{"R1", "R2"}, Changes: []model.ChangeReportItem{
			{RuleID: "R1", Status: "fixed", Change: "a|b\n2行目"},
			{RuleID: "R1", Status: "needs_human", Change: "<確認>", Reason: "所有者|要確認"},
			{RuleID: "R2", Status: "unchanged", Reason: "元から適合"},
		}, Reviews: []model.IndependentReview{publicationTableReview("base", "candidate", "passed_with_holds", "修正と保留を確認", model.ReviewAssessment{RuleID: "R1", Status: "needs_human", Reason: "安全な範囲のみ\n`a<b`|確認済み"}, model.ReviewAssessment{RuleID: "R2", Status: "satisfied", Reason: "修正不要"})}},
	}}
	report := publicationReportContext(resultPublicationSnapshot{tasks: []model.Task{task}})
	body, err := publicationCommitMessage([]model.ResultPublicationFile{publicationFileSummary(task)}, report)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"## ファイル　⚠️一部修正済み要確認", "| R1 | ⚠️一部修正完了/要確認 | 初回の修正<br>a&#124;b<br>2行目<br>保留：&lt;確認&gt; — 所有者&#124;要確認 | 安全な範囲のみ<br>&#96;a&lt;b&#96;&#124;確認済み |", "| R2 | ☑️修正不要 |"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q:\n%s", want, body)
		}
	}
	if strings.Count(body, "| R1 |") != 1 {
		t.Fatal("same-rule fixed and held items were not consolidated")
	}
}

func TestPublicationTableKeepsAcceptedHistoryButNotOldHoldsOrRejectedReviews(t *testing.T) {
	task := model.Task{File: "resolved.js", Status: "done", History: []model.Attempt{
		{Outcome: "needs_human", Partial: true, Commit: "first", InputHash: "base", OutputHash: "part", Changes: []model.ChangeReportItem{{RuleID: "R1", Status: "fixed", Change: "採用された初回修正"}, {RuleID: "R2", Status: "needs_human", Change: "解決済みの保留"}}, Reviews: []model.IndependentReview{publicationTableReview("base", "part", "passed_with_holds", "古い全体レビュー", model.ReviewAssessment{RuleID: "R1", Status: "satisfied", Reason: "初回承認"}, model.ReviewAssessment{RuleID: "R2", Status: "needs_human", Reason: "古い保留レビュー"})}},
		{Outcome: "failed", Changes: []model.ChangeReportItem{{RuleID: "R1", Status: "fixed", Change: "未採用の修正"}}},
		{Outcome: "done", Commit: "last", InputHash: "part", OutputHash: "final", Changes: []model.ChangeReportItem{{RuleID: "R2", Status: "fixed", Change: "保留箇所を修正"}}, Reviews: []model.IndependentReview{
			publicationTableReview("part", "rejected", "needs_changes", "却下候補レビュー", model.ReviewAssessment{RuleID: "R2", Status: "needs_changes", Reason: "却下理由"}),
			publicationTableReview("part", "final", "passed", "最終全体レビュー", model.ReviewAssessment{RuleID: "R1", Status: "satisfied", Reason: "累積修正も確認"}, model.ReviewAssessment{RuleID: "R2", Status: "satisfied", Reason: "保留解消を確認"}),
			publicationTableReview("wrong", "final", "passed", "異なる入力のレビュー", model.ReviewAssessment{RuleID: "R2", Status: "satisfied", Reason: "異なる入力の理由"}),
		}},
	}}
	report := publicationReportContext(resultPublicationSnapshot{tasks: []model.Task{task}})
	body, err := publicationCommitMessage([]model.ResultPublicationFile{publicationFileSummary(task)}, report)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"採用された初回修正", "保留箇所を修正", "最終全体レビュー", "累積修正も確認", "保留解消を確認"} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in %s", want, body)
		}
	}
	for _, unwanted := range []string{"未採用の修正", "解決済みの保留", "古い保留レビュー", "古い全体レビュー", "却下", "異なる入力", "| ⚠️", "## ファイル　⚠️"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("unwanted %q in %s", unwanted, body)
		}
	}
}

func TestPublicationTableReportsLatestReviewRejectionWithoutClaimingFix(t *testing.T) {
	task := model.Task{File: "reviewed.js", Status: "needs_human", History: []model.Attempt{{Outcome: "needs_human", InputHash: "base", Reviews: []model.IndependentReview{publicationTableReview("base", "candidate", "needs_changes", "未解決の問題あり", model.ReviewAssessment{RuleID: "R1", Status: "needs_changes", Reason: "順序が不正"})}}}}
	body, err := publicationCommitMessage(nil, publicationReportContext(resultPublicationSnapshot{tasks: []model.Task{task}}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "| R1 | ⚠️要確認 | 保留：順序が不正 | 順序が不正 |") || strings.Contains(body, "記録なし | 順序") || strings.Contains(body, "✅完了") {
		t.Fatal(body)
	}
}

func TestPublicationTableOmitsDiscardedChangesFromNewNoChangeResult(t *testing.T) {
	task := model.Task{File: "restored.js", Status: "skipped", Discards: []model.DiscardChange{{State: "done", ThroughAttempt: 1}}, History: []model.Attempt{
		{Number: 1, Outcome: "done", Commit: "discarded", InputHash: "old", OutputHash: "changed", Changes: []model.ChangeReportItem{{RuleID: "R1", Status: "fixed", Change: "破棄された変更"}}, Reviews: []model.IndependentReview{publicationTableReview("old", "changed", "passed", "破棄されたレビュー", model.ReviewAssessment{RuleID: "R1", Status: "satisfied", Reason: "破棄された理由"})}},
		{Number: 2, Outcome: "skipped", InputHash: "old", OutputHash: "old", Changes: []model.ChangeReportItem{{RuleID: "R2", Status: "unchanged", Reason: "新しいルールでは変更不要"}}, Reviews: []model.IndependentReview{publicationTableReview("old", "old", "passed", "新しいレビュー", model.ReviewAssessment{RuleID: "R2", Status: "satisfied", Reason: "現行ルールに適合"})}},
	}}
	body, err := publicationCommitMessage(nil, publicationReportContext(resultPublicationSnapshot{tasks: []model.Task{task}}))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "## ファイル　☑️修正不要") || !strings.Contains(body, "| R2 | ☑️修正不要 |") || strings.Contains(body, "破棄された") || strings.Contains(body, "R1") || strings.Contains(body, "✅完了") {
		t.Fatal(body)
	}
}

func TestPublicationSummaryHardBreaksIconsAndConsistentStatusOrdering(t *testing.T) {
	tasks := []model.Task{
		{File: "a-unchanged.js", Status: "skipped"},
		{File: "b-partial.js", Status: "needs_human", History: []model.Attempt{{Outcome: "needs_human", Partial: true, Commit: "accepted", Changes: []model.ChangeReportItem{
			{RuleID: "R0", Status: "unchanged", Reason: "既に適合"},
			{RuleID: "R1", Status: "needs_human", Change: "所有者を確認"},
			{RuleID: "R2", Status: "fixed", Change: "独立した修正"},
		}}}},
		{File: "d-held.js", Status: "needs_human"},
		{File: "m-fixed.js", Status: "done"},
		{File: "z-fixed.js", Status: "done"},
	}
	files := []model.ResultPublicationFile{{File: "z-fixed.js"}, {File: "b-partial.js"}, {File: "m-fixed.js"}}
	body, err := publicationCommitMessage(files, publicationReportContext(resultPublicationSnapshot{tasks: tasks}))
	if err != nil {
		t.Fatal(err)
	}
	counts := "📄 対象ファイル数：5  \n✅ 修正完了：2  \n⚠️ 要確認：2（うち一部修正済み：1）  \n☑️ 修正不要：1"
	if !strings.Contains(body, "# 全体サマリー\n\n"+counts+"\n\n# 修正サマリー\n") {
		t.Fatalf("summary did not use hard breaks/icons: %s", body)
	}
	_, tableAndDetails, ok := strings.Cut(body, "# 修正サマリー\n")
	if !ok {
		t.Fatal("missing compact summary")
	}
	table, details, ok := strings.Cut(tableAndDetails, "# 修正一覧\n")
	if !ok || !strings.Contains(table, "| ファイル名 | ステータス |\n| --- | --- |") {
		t.Fatal("summary columns or placement changed")
	}
	wantOrder := []string{"m-fixed.js", "z-fixed.js", "b-partial.js", "d-held.js", "a-unchanged.js"}
	for _, section := range []string{table, details} {
		last := -1
		for _, file := range wantOrder {
			link := "[" + file + "](<" + file + ">)"
			position := strings.Index(section, link)
			if position <= last || strings.Count(section, link) != 1 {
				t.Fatalf("wrong file order for %s:\n%s", file, section)
			}
			last = position
		}
	}
	if !strings.Contains(table, "| [b-partial.js](<b-partial.js>) | ⚠️一部修正済み要確認 |") || !strings.Contains(table, "| [a-unchanged.js](<a-unchanged.js>) | ☑️修正不要 |") {
		t.Fatal("summary lost file status icons")
	}
	if !(strings.Index(details, "| R2 | ✅完了 |") < strings.Index(details, "| R1 | ⚠️要確認 |") && strings.Index(details, "| R1 | ⚠️要確認 |") < strings.Index(details, "| R0 | ☑️修正不要 |")) {
		t.Fatal("rule rows are not ordered fixed, held, unchanged")
	}
}
