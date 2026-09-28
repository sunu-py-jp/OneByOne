import { Fragment, useEffect, useMemo, useRef, useState } from "react";
import { api } from "./bridge";
import { parseUnifiedDiff } from "./diff";
import { HelpTip } from "./HelpTip";
import { Icon } from "./icons";
import { filterResults, isResultAttention as isAttention, summarizeResults, type ResultFilter } from "./results-model";
import { useResultsSplitter } from "./useResultsSplitter";
import { useResultSnapshot } from "./useResultSnapshot";
import { RulePreviewPane } from "./RulePreviewPane";
import { TaskFileList } from "./TaskFileList";
import { activeFilePhases, attemptStatusLabel, isCompletedTask, isPartialAdoption, taskDisplayStatus, taskStatusLabel } from "./task-state";
import { diffRuleAnnotations, recordedChanges, rulesForLine, ruleOrigins, originLabel, changeNote, heldSourceTarget, type LineRuleAnnotation } from "./result-line-rules";
import type { Attempt, ChangeReportItem, Check, FileDetail, IndependentReview, Rule, State, Task, Usage } from "./types";
import "./results.css";

export type ResultDetailTab = "changes" | "diff" | "checks" | "history";
type CodeView = "diff" | "before" | "after";
interface CodeFocus { key: string; view: "before" | "after"; line: number | null; request: number }
export interface ResultsPanelProps {
  state: State;
  busy: boolean;
  usable: boolean;
  canSetup: boolean;
  selectedFile: string;
  onSelectFile: (file: string) => void;
  detailTab: ResultDetailTab;
  onDetailTabChange: (tab: ResultDetailTab) => void;
  onStop: () => void;
  onExport: (executionId?: string) => void;
  onRetry: (files: string[]) => void;
  onDiscard: (file: string) => void;
  onOpenWorktree: () => void;
  onSetup: () => void;
  execution?: { id: string; targetFiles: ReadonlySet<string>; liveState: State; historical: boolean };
}

const labels: Record<string, string> = {
  pending: taskStatusLabel({ status: "pending" }), retry: taskStatusLabel({ status: "pending", resumeRequested: true }),
  running: taskStatusLabel({ status: "running" }), done: taskStatusLabel({ status: "done" }), skipped: taskStatusLabel({ status: "skipped" }),
  failed: taskStatusLabel({ status: "failed" }), needs_human: taskStatusLabel({ status: "needs_human" }),
  validated: "検証済み", interrupted: "中断", discarded: "破棄済み", discard_pending: "処理中",
};
const num = (n: number) => n.toLocaleString("ja-JP");
const message = (error: unknown) => error instanceof Error ? error.message : String(error);
const fullPath = (root: string, file: string) => `${root.replace(/[\\/]$/, "")}/${file}`;
const dateTime = (date: string) => date ? new Date(date).toLocaleString("ja-JP", { month: "numeric", day: "numeric", hour: "2-digit", minute: "2-digit", second: "2-digit" }) : "—";
const failedCheck = (check: Check) => ["failed", "fail", "error"].includes(check.status);
const reviewLabels: Record<IndependentReview["verdict"], string> = {
  passed: "合格", passed_with_holds: "修正部分は合格・要確認あり", needs_changes: "要修正", needs_human: "判断保留", running: "確認中", error: "エラー",
};
const assessmentLabels: Record<string, string> = {
  satisfied: "適合", not_applicable: "対象外", violated: "要修正", needs_human: "判断保留",
};
const changeLabels: Record<ChangeReportItem["status"], string> = {
  fixed: "修正済み", needs_human: "要確認", not_applied: "未反映", pending: "確認中", unchanged: "変更不要",
};

// Old navigation requests for the separate report now open the integrated changes view.
export function resolveResultDetailTab(requested: ResultDetailTab, _hasReport?: boolean, _manuallySelected?: boolean): ResultDetailTab {
  return requested === "changes" ? "diff" : requested;
}

// Existing histories can acquire reports lazily from their saved checkpoints.
// Only supplement a matching attempt; live snapshot reports remain authoritative.
export function withChangeReport(attempt: Attempt, detailTask?: Task): Attempt {
  if (attempt.changes?.length || !attempt.id) return attempt;
  const hydrated = detailTask?.history?.find(item => item.id === attempt.id);
  if (!hydrated?.changes?.length || hydrated.outcome !== attempt.outcome || hydrated.commit !== attempt.commit) return attempt;
  return { ...attempt, changes: hydrated.changes };
}

// Cumulative line ranges come from the backend's verified accepted snapshots.
// Never place a latest-attempt report on a cumulative diff while it is loading.
export function resultChanges(history: Attempt[], index: number, detail?: FileDetail | null, executionId?: string): ChangeReportItem[] {
  const changes = recordedChanges(index < 0 ? detail?.cumulative ? detail.changes : [] : history[index]?.changes);
  const latestID = history.at(-1)?.id;
  const attempts = new Map(history.map(attempt => [attempt.id, attempt]));
  return changes.map(item => {
    const sourceID = index < 0 ? item.sourceAttemptId : history[index]?.id;
    const source = sourceID ? attempts.get(sourceID) : undefined;
    const origin = item.status === "fixed" && sourceID && latestID
      ? executionId ? source ? source.executionId === executionId ? "latest" : "previous" : undefined
      : sourceID === latestID ? "latest" : "previous" : undefined;
    return origin ? { ...item, origin } : item;
  });
}

export function ResultsPanel({ state, busy, usable, canSetup, selectedFile, onSelectFile, detailTab,
  onDetailTabChange, onStop, onExport, onRetry, onDiscard, onOpenWorktree, onSetup, execution }: ResultsPanelProps) {
  const [filter, setFilter] = useState<ResultFilter>("all");
  const [search, setSearch] = useState("");
  const [completedOpen, setCompletedOpen] = useState(false);
  const [attemptSelection, setAttemptSelection] = useState({ file: "", index: -1 });
  const [codeViewSelection, setCodeViewSelection] = useState<{ key: string; view: CodeView } | null>(null);
  const [codeFocus, setCodeFocus] = useState<CodeFocus | null>(null);
  const [detailRequest, setDetailRequest] = useState(0);
  const [viewedRule, setViewedRule] = useState<Rule | null>(null);
  const [ruleError, setRuleError] = useState("");
  const [readingRule, setReadingRule] = useState("");
  const ruleRequest = useRef(0);
  const localSelection = useRef(selectedFile);
  const detailScroll = useRef<HTMLDivElement>(null);
  const splitter = useResultsSplitter(state.activeWorkspaceId, state.tasks.map(task => task.file), { extraWidth: 118, sampleSelector: ".task-file-open" });
  const liveState = execution?.liveState || state;
  const historical = Boolean(execution?.historical);
  const mutationLocked = busy || liveState.running || liveState.readOnly || !usable || historical;
  const mutationLockedReason = historical ? "過去の実行は閲覧専用です。最新の実行から操作してください" : liveState.readOnly ? "読み取り専用のワークスペースでは変更できません" : liveState.running ? "実行中は再試行を追加できません" : busy ? "処理が完了してから操作してください" : !usable ? "ワークスペースを使用できません" : "";
  const selectedTask = state.tasks.find(task => task.file === selectedFile);
  const liveSelectedTask = liveState.tasks.find(task => task.file === selectedFile);
  const attemptIndex = attemptSelection.file === selectedFile ? attemptSelection.index : -1;
  // In-flight plans, checks and reviews change without updating task.updatedAt.
  // Keep view identity separate from the selected file's changing contents.
  const detailKey = JSON.stringify([state.activeWorkspaceId, execution?.id || "", selectedFile, attemptIndex]);
  const detailRevision = JSON.stringify([selectedTask, state.running, detailRequest]);
  const detailResult = useResultSnapshot<FileDetail>({ key: detailKey, revision: detailRevision, enabled: Boolean(selectedTask),
    poll: !historical && state.running && selectedTask?.status === "running",
    read: () => execution ? api.GetExecutionFileDetail(execution.id, selectedFile, attemptIndex) : api.GetFileDetail(selectedFile, attemptIndex),
  });
  const detail = detailResult.data || null;
  const reportedHistory = selectedTask?.history.map(attempt => withChangeReport(attempt, detail?.task)) || [];
  const currentAttempt = reportedHistory[attemptIndex < 0 ? reportedHistory.length - 1 : attemptIndex];
  const viewedBeforeDiscard = Boolean(attemptIndex >= 0 && currentAttempt && selectedTask?.discards?.some(discard => discard.state === "done" && currentAttempt.number <= discard.throughAttempt));
  const historyRecords = [
    ...reportedHistory.map((attempt, index) => ({ kind: "attempt" as const, order: attempt.number || index + 1, attempt, index })),
    ...(selectedTask?.discards || []).map(discard => ({ kind: "discard" as const, order: discard.throughAttempt + 0.5, discard })),
  ].sort((a, b) => b.order - a.order);
  const currentReviews = currentAttempt?.reviews || [];
  const latestReview = currentReviews[currentReviews.length - 1];
  const reviewNeedsAttention = latestReview && ["passed_with_holds", "needs_changes", "needs_human", "error"].includes(latestReview.verdict);
  const codeViewKey = `${state.activeWorkspaceId}:${execution?.id || ""}:${selectedFile}:${attemptIndex}`;
  const currentChanges = resultChanges(reportedHistory, attemptIndex, detail, execution?.id);
  const activeTab = resolveResultDetailTab(detailTab);
  const hasDiff = detail ? Boolean(detail.diff.trim()) : attemptIndex < 0 ? Boolean(selectedTask?.canDiscardChanges) : Boolean(currentAttempt?.diffPath);
  // Default from the actual artifact: failed attempts can still have a useful
  // proposed diff. A manual toggle remains selected while this file is polled.
  const codeView = codeViewSelection?.key === codeViewKey
    ? codeViewSelection.view
    : hasDiff ? "diff" : "before";
  const detailError = detailResult.error || "";

  useEffect(() => {
    ruleRequest.current++;
    setViewedRule(null);
    setReadingRule("");
    setRuleError("");
    setCodeViewSelection(null);
    setCodeFocus(null);
    detailScroll.current?.scrollTo({ top: 0, left: 0 });
  }, [selectedFile, state.activeWorkspaceId]);
  useEffect(() => { setCompletedOpen(false); setFilter("all"); setSearch(""); }, [state.activeWorkspaceId]);
  useEffect(() => () => { ruleRequest.current++; }, []);

  const rulesByID = useMemo(() => new Map(state.rules.map(rule => [rule.id, rule])), [state.rules]);
  const runTasks = useMemo(() => execution ? state.tasks.filter(task => execution.targetFiles.has(task.file)).map(task => ({ ...task, excluded: false })) : state.tasks, [state.tasks, execution?.targetFiles]);
  const { counts, progress, runLabel } = useMemo(() => summarizeResults(runTasks, state.running, state.phase, state.lastError), [runTasks, state.running, state.phase, state.lastError]);
  const filePhases = historical ? undefined : activeFilePhases(state);
  const filteredTasks = useMemo(() => {
    const tasks = execution ? state.tasks.filter(task => execution.targetFiles.has(task.file) || (filter === "all" && isCompletedTask(task))).map(task => ({ ...task, excluded: false })) : state.tasks;
    return filterResults(tasks, rulesByID, filter, search);
  }, [state.tasks, rulesByID, filter, search, execution?.targetFiles]);
  const attentionFiles = useMemo(() => filteredTasks.filter(task => isAttention(task) && (!execution || execution.targetFiles.has(task.file))).map(task => task.file), [filteredTasks, execution?.targetFiles]);

  useEffect(() => {
    // Reveal explicit navigation from workspace diagnostics, but do not follow background polling.
    if (!selectedTask || localSelection.current === selectedFile) return;
    if (!filteredTasks.some(task => task.file === selectedFile)) {
      const nextFilter = selectedTask.excluded ? "excluded" : "all";
      setSearch("");
      setFilter(nextFilter);
    }
    if (isCompletedTask(selectedTask) && (!execution || !execution.targetFiles.has(selectedTask.file))) setCompletedOpen(true);
  }, [selectedFile, state.activeWorkspaceId]);

  function chooseFile(file: string) {
    localSelection.current = file;
    onSelectFile(file);
    onDetailTabChange("diff");
    setAttemptSelection({ file, index: -1 });
    setViewedRule(null);
    setRuleError("");
    setCodeViewSelection(null);
    setCodeFocus(null);
  }
  function chooseAttempt(index: number, tab?: ResultDetailTab) {
    setAttemptSelection({ file: selectedFile, index });
    setCodeViewSelection(null);
    setCodeFocus(null);
    onDetailTabChange(tab || "diff");
  }
  function showHeldChange(change: ChangeReportItem) {
    if (!detail) return;
    const target = heldChangeDestination(change, detail, codeView, reportedHistory);
    const view = target?.side || "before";
    const key = target?.attemptIndex !== undefined ? `${state.activeWorkspaceId}:${execution?.id || ""}:${selectedFile}:${target.attemptIndex}` : codeViewKey;
    if (target?.attemptIndex !== undefined) setAttemptSelection({ file: selectedFile, index: target.attemptIndex });
    setCodeViewSelection({ key, view });
    setCodeFocus(previous => ({ key, view, line: target?.line || null, request: (previous?.request || 0) + 1 }));
  }
  function retryFiles(files: string[]) {
    // Retrying can keep the same selection while moving it out of the completed
    // or attention filter. Show the requeued row as soon as the snapshot arrives.
    setSearch("");
    setFilter("all");
    onRetry(files);
  }
  async function showRule(id: string) {
    const request = ++ruleRequest.current;
    setRuleError("");
    const existing = rulesByID.get(id);
    if (existing) { setViewedRule(existing); setReadingRule(""); return; }
    setViewedRule(null);
    if (execution) {
      setReadingRule("");
      setRuleError(`ルール ${id} の定義は、この実行の記録に含まれていません。`);
      return;
    }
    setReadingRule(id);
    try {
      const rule = await api.ReadRule(id);
      if (request === ruleRequest.current) {
        if (!rule) throw new Error(`ルール ${id} は現在のルール一覧にありません。`);
        setViewedRule(rule);
      }
    } catch (error) {
      if (request === ruleRequest.current) setRuleError(message(error));
    } finally {
      if (request === ruleRequest.current) setReadingRule("");
    }
  }
  function closeRule() {
    ruleRequest.current++;
    setViewedRule(null);
    setReadingRule("");
    setRuleError("");
  }
  const rulePaneOpen = Boolean(viewedRule || readingRule || ruleError);
  const appliedIDs = attemptIndex >= 0 ? currentAttempt?.commit ? currentAttempt.rulesApplied : [] : selectedTask?.rulesApplied || [];
  const proposedIDs = attemptIndex >= 0 && currentAttempt && !currentAttempt.commit ? currentAttempt.rulesApplied : [];
  // An older attempt with no note must never display the latest attempt's explanation.
  const currentHold = attemptIndex < 0 && selectedTask?.status === "needs_human" && selectedTask.note;
  const pendingNote = attemptIndex < 0 && selectedTask?.status === "pending" && selectedTask.note;
  const unchangedCompletion = attemptIndex < 0 && currentAttempt?.outcome === "skipped" && selectedTask && taskDisplayStatus(selectedTask) === "done";
  const explanation = currentHold || pendingNote || (currentAttempt ? currentAttempt.note : attemptIndex < 0 ? selectedTask?.note : "");
  const explanationNeedsAttention = Boolean(currentHold) || (currentAttempt ? ["failed", "needs_human", "interrupted"].includes(currentAttempt.outcome) : Boolean(selectedTask && isAttention(selectedTask)));
  const changeStats = useMemo(() => {
    const lines = parseUnifiedDiff(detail?.diff || "");
    return { added: lines.filter(line => line.type === "added").length, removed: lines.filter(line => line.type === "removed").length };
  }, [detail?.diff]);

  const detailFeedback = detailError
    ? <div className="results-detail-error" role="alert"><Icon name="warning" size={16} /><span>{detailError}</span><button className="results-text-button" onClick={() => setDetailRequest(value => value + 1)}>再読み込み</button></div>
    : <div className="results-empty small" role="status"><span className="spinner" />読み込み中…</div>;

  return <section className="results-panel" aria-label="実行結果">
    <header className="results-overview">
      <div className="results-status-line">
        <div className={`results-run-label ${state.running ? "is-running" : (counts.attention || state.lastError) ? "has-attention" : ""}`} role="status">
          {state.running ? <span className="spinner" /> : <Icon name={state.lastError ? "warning" : !counts.attempted ? "clock" : counts.attention ? "warning" : counts.pending || counts.retry ? "pause" : "check"} size={17} />}
          <strong>{runLabel}</strong>
        </div>
        <span className="results-progress-count">{num(counts.settled)} / {num(counts.all)}<span> ファイル</span><b>{progress}%</b></span>
        <HelpTip label="進捗">完了・変更不要・要確認のファイルを処理済みとして集計します。停止中は失敗したファイルも含みます。再試行に追加したファイルは、終了するまで進捗に含みません。</HelpTip>
        <div className="results-top-actions">
          {!historical && state.running && liveState.running && <button className="results-button is-danger" disabled={busy || liveState.readOnly || !usable} onClick={onStop}><Icon name="stop" size={14} />停止</button>}
          <button className="results-button" onClick={() => onExport(execution?.id)} disabled={busy || liveState.running || liveState.readOnly || !usable || !state.tasks.length}><Icon name="download" size={14} />出力</button>
          <details className="results-more">
            <summary aria-label="実行情報"><Icon name="info" size={17} /></summary>
            <div className="results-run-facts"><h3>実行情報</h3><UsageFacts usage={state.usage} />
              {state.branch && <div><span>ブランチ</span><code>{state.branch}</code></div>}
              {state.worktree && <button className="results-text-button" disabled={busy || !usable || historical} title={historical ? "過去の実行時点の内容は変更内容から確認できます" : state.worktree} onClick={onOpenWorktree}><Icon name="folder" size={14} />作業コピーを開く</button>}
            </div>
          </details>
        </div>
      </div>
      <div className="results-progress-track" role="progressbar" aria-label="処理の進捗" aria-valuemin={0} aria-valuemax={counts.all || 1} aria-valuenow={counts.settled} aria-valuetext={`${counts.settled} / ${counts.all} ファイル`}>
        <span className="is-done" style={{ width: `${counts.all ? counts.done / counts.all * 100 : 0}%` }} />
        <span className="is-skipped" style={{ width: `${counts.all ? counts.skipped / counts.all * 100 : 0}%` }} />
        <span className="is-attention" style={{ width: `${counts.all ? Math.max(0, counts.settled - counts.done - counts.skipped) / counts.all * 100 : 0}%` }} />
      </div>
      <div className="results-status-filters" role="group" aria-label="状態で絞り込み">
        {([
          ["all", "すべて", counts.all], ["retry", "再試行", counts.retry], ["running", "処理中", counts.running], ["pending", "未修正", counts.pending],
          ["attention", "要確認・失敗", counts.attention], ["skipped", "変更不要", counts.skipped], ["done", "完了", counts.done],
        ] as const).map(([id, label, count]) => <button key={id} className={`results-filter filter-${id}${filter === id ? " active" : ""}`} aria-pressed={filter === id} onClick={() => { setFilter(id); setCompletedOpen(id === "done" || id === "skipped"); }}>
          <span className="results-filter-dot" />{label}<b>{num(count)}</b>
        </button>)}
      </div>
      {state.lastError && <details className="results-error-summary"><summary><Icon name="warning" size={14} /><span>{state.lastError.split("\n")[0]}</span><Icon name="down" size={13} /></summary><pre>{state.lastError}</pre></details>}
    </header>

    {state.tasks.length === 0 ? <div className="results-empty"><Icon name="file" size={28} /><strong>実行結果はまだありません</strong><button className="results-button" disabled={!canSetup} onClick={onSetup}>実行設定へ<Icon name="arrow" size={14} /></button></div> : <div className={`results-content-layout${rulePaneOpen ? " has-rule-pane" : ""}`}><div ref={splitter.ref} className="results-browser" style={splitter.ready ? { gridTemplateColumns: `${splitter.width}px 9px minmax(0, 1fr)` } : undefined}>
      <section className="results-files" id="results-file-list" aria-label="ファイル一覧">
        <div className="results-file-tools">
          <label className="results-search"><Icon name="search" size={15} /><input aria-label="結果のファイル名・適用ルールを検索" value={search} onChange={event => setSearch(event.target.value)} placeholder="ファイル・適用ルールを検索" />{search && <button onClick={() => setSearch("")} aria-label="検索をクリア"><Icon name="close" size={13} /></button>}</label>
          {counts.excluded > 0 && <button className={`results-excluded-toggle ${filter === "excluded" ? "active" : ""}`} aria-pressed={filter === "excluded"} onClick={() => setFilter(filter === "excluded" ? "all" : "excluded")} title="処理対象から外したファイルの履歴を表示">対象外 {num(counts.excluded)}</button>}
        </div>
        <TaskFileList runTargetFiles={execution?.targetFiles} tasks={filteredTasks} selectedFile={selectedFile} onSelectFile={chooseFile} filePhases={filePhases}
          completedOpen={completedOpen} onCompletedOpenChange={setCompletedOpen}
          onRetry={file => retryFiles([file])} retryDisabled={mutationLocked} retryDisabledReason={mutationLockedReason}
          emptyMessage="該当するファイルはありません" />
        <footer className="results-list-footer"><span>{num(filteredTasks.length)} 件</span>
          {!filteredTasks.length && (search || filter !== "all") && <button className="results-text-button" onClick={() => { setSearch(""); setFilter("all"); }}>絞り込みを解除</button>}
          {filter === "attention" && attentionFiles.length > 0 && <button className="results-text-button" disabled={mutationLocked} onClick={() => retryFiles(attentionFiles)} title={mutationLockedReason || "絞り込んだ要確認・失敗ファイルを再試行に追加"}><Icon name="refresh" size={12} />再試行に追加</button>}
        </footer>
      </section>

      <div className={`results-splitter${splitter.resizing ? " is-resizing" : ""}`} role="separator" tabIndex={0}
        aria-label="ファイル一覧の幅" aria-orientation="vertical" aria-controls="results-file-list result-file-detail"
        aria-valuemin={splitter.bounds.min} aria-valuemax={splitter.bounds.max} aria-valuenow={splitter.width}
        aria-valuetext={`${splitter.width} ピクセル`} title="ドラッグまたは左右キーで幅を変更。ダブルクリックまたは Enter で自動調整"
        {...splitter.handleProps}><span aria-hidden="true" /></div>

      <section className="results-detail" id="result-file-detail" tabIndex={-1} aria-label={selectedTask ? `${selectedTask.file}の詳細` : "ファイル詳細"}>
        {selectedTask ? <>
          <header className="results-detail-heading"><Icon name="file" size={16} /><strong title={fullPath(state.config.root, selectedTask.file)}>{selectedTask.file}</strong>
            {appliedIDs.length > 0 && <RuleChips ids={appliedIDs} rules={rulesByID} origins={ruleOrigins(currentChanges)} onOpen={showRule} />}
            <ResultStatus status={taskDisplayStatus(selectedTask)} label={taskStatusLabel(selectedTask, filePhases?.[selectedTask.file])} /></header>
          {explanation && <div key={`${selectedFile}:${attemptIndex}:note`} className={`results-explanation${explanationNeedsAttention ? " attention" : ""}`} role="note" aria-label="処理の説明" tabIndex={0}>
            <Icon name={explanationNeedsAttention ? "warning" : "info"} size={14} /><p>{explanation}</p>
          </div>}
          {viewedBeforeDiscard && <div className="results-inline-note" role="note">表示中の試行は変更破棄前の記録です。コード・対応状況は当時の内容を表示しています。</div>}
          <nav className="results-detail-tabs" role="tablist" aria-label="ファイルの結果詳細">
            {([{ id: "diff", label: "変更内容", icon: "file" }, { id: "checks", label: "検証", icon: "shield" }, { id: "history", label: execution ? "試行履歴" : "履歴", icon: "clock" }] as const).map(tab => <button key={tab.id} role="tab" id={`results-tab-${tab.id}`} aria-controls="results-detail-content" aria-selected={activeTab === tab.id} className={activeTab === tab.id ? "active" : ""} onClick={() => onDetailTabChange(tab.id)}><Icon name={tab.icon} size={14} />{tab.label}{tab.id === "checks" && (currentAttempt?.checks.some(failedCheck) || reviewNeedsAttention) && <i className="results-tab-alert" />}{tab.id === "history" && historyRecords.length > 0 && <small>{historyRecords.length}</small>}</button>)}
          </nav>
          <div ref={detailScroll} className="results-detail-scroll" id="results-detail-content" role="tabpanel" aria-labelledby={`results-tab-${activeTab}`}>
            {activeTab !== "history" && selectedTask.history.length > 0 && (!execution || attemptIndex >= 0) && <div className="results-attempt-selector"><label htmlFor="result-attempt">{activeTab === "checks" ? "検証した試行" : "表示範囲"}</label><select id="result-attempt" value={attemptIndex} onChange={event => chooseAttempt(Number(event.target.value), activeTab === "checks" ? "checks" : undefined)}><option value={-1}>{activeTab === "checks" ? `最新（${selectedTask.history.length} 回目）` : execution ? "全体（開始前 → この実行時点）" : "全体（開始前 → 現在）"}</option>{selectedTask.history.map((attempt, index) => <option value={index} key={attempt.id || index}>{index + 1} 回目 · {attemptStatusLabel(attempt)}</option>)}</select></div>}
            {activeTab === "diff" && <>
              {proposedIDs.length > 0 && <details className="results-note"><summary><Icon name="info" size={14} /><span>この試行で使用したルール（変更は未採用）</span><Icon name="down" size={13} /></summary><div className="results-proposed-rules"><RuleChips ids={proposedIDs} rules={rulesByID} onOpen={showRule} /></div></details>}
              <div className="results-diff-toolbar"><div className="results-view-toggle" role="group" aria-label="コードの表示方法">{([{ id: "diff", label: "差分" }, { id: "before", label: "変更前" }, { id: "after", label: "変更後" }] as const).map(view => <button key={view.id} className={codeView === view.id ? "active" : ""} aria-pressed={codeView === view.id} onClick={() => { setCodeViewSelection({ key: codeViewKey, view: view.id }); setCodeFocus(null); }}>{view.label}</button>)}</div><HeldChangeMenu changes={currentChanges} detail={detail} view={codeView} history={reportedHistory} onChoose={showHeldChange} /><RuleOriginLegend changes={currentChanges} />{detail?.diff && <span className="results-change-count" aria-label={`${changeStats.added}行追加、${changeStats.removed}行削除`}><b>+{num(changeStats.added)}</b><b>−{num(changeStats.removed)}</b></span>}</div>
              {detail && detailError && detailFeedback}
              {!detail ? detailFeedback : (codeView !== "diff" || detail.diff || currentChanges.length > 0) ? <ResultCode content={codeView === "diff" ? detail.diff : codeView === "before" ? detail.before : detail.after} isDiff={codeView === "diff"} view={codeView} changes={currentChanges} rules={rulesByID} onOpenRule={showRule} focus={codeFocus?.key === codeViewKey && codeFocus.view === codeView ? codeFocus : undefined} /> : <div className="results-empty small"><Icon name={selectedTask.status === "pending" ? "clock" : "file"} size={25} /><span>{selectedTask.status === "pending" ? "処理を開始すると変更内容が表示されます" : selectedTask.status === "running" ? "このファイルを処理しています" : attemptIndex < 0 ? "開始前からの差分はありません" : "この試行の差分はありません"}</span></div>}
              {attemptIndex >= 0 && currentAttempt && <details className="results-attempt-facts"><summary>試行の情報<Icon name="down" size={13} /></summary><AttemptFacts attempt={currentAttempt} /></details>}
            </>}
            {activeTab === "checks" && <div className="results-checks">{currentAttempt?.checks.map((check, index) => <CheckInspection key={`${check.name}:${index}`} check={check} />)}
              <ReviewInspections reviews={currentReviews} rules={rulesByID} onOpenRule={showRule} />
              {!currentAttempt?.checks.length && !currentReviews.length && <div className="results-empty small"><Icon name="shield" size={25} /><span>検証結果はまだありません</span></div>}
            </div>}
            {activeTab === "history" && <div className="results-history">{historyRecords.length ? historyRecords.map(record => {
              if (record.kind === "discard") {
                const discard = record.discard;
                return <article className="results-history-item" key={`discard:${discard.id}`}>
                  <div className="results-history-title"><strong>変更破棄</strong><ResultStatus status={discard.state === "done" ? "discarded" : "discard_pending"} /><time>{dateTime(discard.finishedAt || discard.startedAt)}</time></div>
                  <p className="results-discard-note">{discard.state === "done" ? "このファイルを処理開始前の内容に戻しました。以前の試行は記録として残っています。" : "変更破棄の完了を確認しています。"}</p>
                  {discard.commit && <div className="results-discard-commit">コミット <code title={discard.commit}>{discard.commit.slice(0, 10)}</code></div>}
                </article>;
              }
              const { attempt, index } = record;
              return <article className="results-history-item" key={attempt.id || index}><div className="results-history-title"><strong>{index + 1} 回目</strong><ResultStatus status={attempt.outcome || "running"} label={attemptStatusLabel(attempt)} /><time>{dateTime(attempt.startedAt)}</time></div>
                <details className="results-note"><summary><span>{attempt.note?.split("\n")[0] || "試行の情報"}</span><Icon name="down" size={13} /></summary>{attempt.note && <p>{attempt.note}</p>}<AttemptFacts attempt={attempt} /></details>
                <ReviewInspections reviews={attempt.reviews || []} rules={rulesByID} onOpenRule={showRule} />
                <div className="results-history-bottom"><RuleChips ids={attempt.rulesApplied} rules={rulesByID} onOpen={showRule} /><button className="results-text-button" onClick={() => chooseAttempt(index)}>変更内容を見る<Icon name="arrow" size={13} /></button></div>
              </article>;
            }) : <div className="results-empty small"><Icon name="clock" size={25} /><span>試行履歴はありません</span></div>}</div>}
          </div>
          <footer className="results-detail-footer">
            <span>{historical ? "選択した実行時点の記録" : execution && !execution.targetFiles.has(selectedTask.file) ? "この実行より前に完了済み" : execution && liveSelectedTask?.status === "pending" && liveSelectedTask.resumeRequested ? "次の実行の再試行に追加済み" : selectedTask.excluded ? "現在の処理対象から除外" : pendingNote ? selectedTask.resumeRequested ? "再試行に追加済み" : "未修正" : viewedBeforeDiscard ? "変更破棄前の試行記録" : unchangedCompletion ? "追加修正なし・以前の修正を保持して完了" : isPartialAdoption(currentAttempt) ? "確認済みの修正を保存・要確認の箇所は未変更" : currentAttempt?.outcome === "done" ? "検証を通過・変更を保存済み" : currentAttempt?.outcome === "skipped" ? "変更不要として終了" : currentAttempt?.outcome && currentAttempt.outcome !== "running" ? "この試行の変更は未採用" : ""}</span>
            <div className="results-detail-actions">
              <button className="results-text-button" disabled={mutationLocked || liveSelectedTask?.status === "pending" || !liveSelectedTask} title={mutationLockedReason || (liveSelectedTask?.status === "pending" ? "このファイルは次の処理対象です" : "このファイルを再試行に追加")} onClick={() => retryFiles([selectedTask.file])}><Icon name="refresh" size={13} />再試行に追加</button>
              <button className="results-text-button is-danger" disabled={mutationLocked || !liveSelectedTask?.canDiscardChanges} onClick={() => onDiscard(selectedTask.file)}><Icon name="trash" size={13} />変更破棄</button>
            </div>
          </footer>
        </> : <div className="results-empty"><Icon name="file" size={30} /><strong>ファイルを選択</strong><span>適用されたルールと変更内容を確認できます</span></div>}
      </section>
    </div>{rulePaneOpen && <RulePreviewPane rule={viewedRule} loading={Boolean(readingRule)} error={ruleError} caption={execution ? "実行時点のルール定義" : "現在のルール定義"} onClose={closeRule} />}</div>}
  </section>;
}

function ResultStatus({ status, label }: { status: string; label?: string }) {
  return <span className={`results-task-status status-${status}`}><i />{label || labels[status] || status}</span>;
}

export function ChangeReport({ attempt, changes: report, cumulative = false, ruleCaption = "現在のルール", onOpenRule }: { attempt?: Attempt; changes?: ChangeReportItem[]; cumulative?: boolean; ruleCaption?: string; onOpenRule: (id: string) => void }) {
  const changes = recordedChanges(report ?? attempt?.changes);
  if (!changes.length) return null;
  const counts = changes.reduce((result, item) => {
    result[item.status] = (result[item.status] || 0) + 1;
    return result;
  }, {} as Record<string, number>);
  const unadopted = !cumulative && attempt && !attempt.commit && ["failed", "needs_human", "interrupted"].includes(attempt.outcome);
  return <div className="results-change-report" aria-label="修正ごとの対応状況">
    <div className="results-change-summary" aria-label="対応状況の件数">{(Object.keys(changeLabels) as ChangeReportItem["status"][]).filter(status => counts[status]).map(status => <span key={status} className={`results-change-status change-${status}`}>{changeLabels[status]}<b>{num(counts[status])}</b></span>)}<RuleOriginLegend changes={changes} /></div>
    {unadopted && <p className="results-change-unadopted"><Icon name="info" size={13} />この試行の修正は未採用です。</p>}
    <div className="results-change-list">{changes.map((item, index) => <details className="results-change-item" key={item.id || index}>
      <summary>
        <code data-change-origin={item.origin} title={[`${item.ruleId} · ${item.ruleTitle || "名称未記録"}`, originLabel(item.origin)].filter(Boolean).join(" · ")}>{item.ruleId || "—"}</code>
        <strong title={changeNote(item)}>{changeNote(item)}</strong>
        <span className={`results-change-status change-${item.status}`}><Icon name={item.status === "fixed" || item.status === "unchanged" ? "check" : item.status === "needs_human" ? "warning" : item.status === "pending" ? "clock" : "pause"} size={12} />{changeLabels[item.status] || item.status}</span>
        <Icon name="down" size={12} />
      </summary>
      <div className="results-change-detail">
        <div className="results-change-reference">{item.ruleId && <button className="results-text-button" data-change-origin={item.origin} title={[`${ruleCaption}を表示`, originLabel(item.origin)].filter(Boolean).join(" · ")} aria-label={`${item.ruleId} の${ruleCaption}を表示`} onClick={() => onOpenRule(item.ruleId)}><Icon name="rules" size={13} />{item.ruleId} · {item.ruleTitle || "名称未記録"}<Icon name="arrow" size={12} /></button>}</div>
        <dl><div><dt>リスク</dt><dd>{item.risk || "未記録"}</dd></div><div><dt>対応</dt><dd>{item.change || "未記録"}</dd></div><div><dt>期待する結果</dt><dd>{item.expected || "未記録"}</dd></div><div><dt>判断理由</dt><dd>{item.reason || "未記録"}</dd></div></dl>
      </div>
    </details>)}</div>
  </div>;
}

function RuleOriginLegend({ changes }: { changes: ChangeReportItem[] }) {
  if (!changes.some(item => item.origin)) return null;
  return <span className="results-rule-origin-legend" aria-label="ルールIDの色の意味"><span><i data-change-origin="latest" />この実行の修正</span><span><i data-change-origin="previous" />以前の修正</span></span>;
}

function RuleChips({ ids, rules, origins, onOpen }: { ids: string[]; rules: Map<string, Rule>; origins?: Map<string, ChangeReportItem["origin"]>; onOpen: (id: string) => void }) {
  if (!ids.length) return <span className="results-no-rules" title="採用されたルールはありません">—</span>;
  return <span className="results-rule-chips">{ids.map(id => <button key={id} data-change-origin={origins?.get(id)} onClick={event => { event.stopPropagation(); void onOpen(id); }} title={[`${id} · ${rules.get(id)?.title || "ルールの詳細"}`, originLabel(origins?.get(id))].filter(Boolean).join(" · ")} aria-label={`${id} ${rules.get(id)?.title || ""}の詳細`}><span>{id}</span></button>)}</span>;
}

function UsageFacts({ usage }: { usage: Usage }) {
  return <><div><span>推定料金</span><span>{usage.costUsd > 0 ? `$${usage.costUsd.toFixed(3)}` : "—"}{usage.uncertain && "（一部未計上）"}<HelpTip label="推定料金">設定した単価に基づく金額です。{usage.uncertain && "使用量が不明なリクエストの料金は含まれていません。表示額は請求額の上限ではありません。"}</HelpTip></span></div><div><span>トークン</span><span>{num(usage.inputTokens + usage.outputTokens)}<HelpTip label="トークン内訳">入力 {num(usage.inputTokens)} ／ キャッシュ {num(usage.cachedTokens)} ／ 出力 {num(usage.outputTokens)}</HelpTip></span></div><div><span>ターン</span><span>{num(usage.turns)}</span></div></>;
}
function AttemptFacts({ attempt }: { attempt: Attempt }) {
  return <div className="results-facts"><div><span>開始</span><span>{dateTime(attempt.startedAt)}</span></div><div><span>終了</span><span>{dateTime(attempt.finishedAt)}</span></div><UsageFacts usage={attempt.usage} />{attempt.commit && <div><span>コミット</span><code title={attempt.commit}>{attempt.commit.slice(0, 10)}</code></div>}</div>;
}
function CheckInspection({ check }: { check: Check }) {
  const passed = ["passed", "pass", "ok", "success"].includes(check.status);
  const failed = failedCheck(check);
  return <details className={`results-check ${passed ? "passed" : failed ? "failed" : "unrun"}`}><summary><Icon name={passed ? "check" : failed ? "close" : "info"} size={14} /><strong title={check.name}>{check.name}</strong><span>{passed ? "合格" : failed ? "不合格" : check.status === "skipped" ? "未実施" : check.status}</span><small>{check.durationMs >= 1000 ? `${(check.durationMs / 1000).toFixed(1)}s` : `${check.durationMs}ms`}</small><Icon name="down" size={12} /></summary><pre>{check.detail || "詳細な出力はありません。"}</pre></details>;
}
function ReviewInspections({ reviews, rules, onOpenRule }: { reviews: IndependentReview[]; rules: Map<string, Rule>; onOpenRule: (id: string) => void }) {
  return <>{reviews.map((review, index) => {
    const attention = ["needs_changes", "needs_human", "error"].includes(review.verdict);
    const issues = review.issues || [];
    const assessments = review.assessments || [];
    return <details className={`results-check results-review ${["passed", "passed_with_holds"].includes(review.verdict) ? "passed" : attention ? "failed" : "unrun"}`} key={review.id || `${review.candidateId}:${index}`}>
      <summary><Icon name={review.verdict === "running" ? "clock" : "shield"} size={14} /><strong>独立レビュー {index + 1}</strong><span>{reviewLabels[review.verdict] || review.verdict}</span>{issues.length > 0 && <small>指摘 {issues.length}</small>}<Icon name="down" size={12} /></summary>
      <div className="results-review-content">
        {review.summary && <p className="results-review-summary">{review.summary}</p>}
        {issues.length > 0 && <ol className="results-review-issues">{issues.map((issue, issueIndex) => <li key={issueIndex}>
          <div className="results-review-location"><RuleChips ids={issue.ruleId ? [issue.ruleId] : []} rules={rules} onOpen={onOpenRule} /><span>{issue.lineBasis === "before" ? "変更前" : issue.lineBasis === "after" ? "変更後" : ""}{issue.location && ` · ${issue.location}`}</span></div>
          <p>{issue.reason}</p>
          {issue.requestedChange && <p className="results-review-request"><span>修正方針</span>{issue.requestedChange}</p>}
          {issue.excerpt && <pre>{issue.excerpt}</pre>}
        </li>)}</ol>}
        {(review.holdAssessments?.length || 0) > 0 && <details className="results-review-assessments"><summary>保留箇所の確認 <small>{review.holdAssessments!.length}</small><Icon name="down" size={12} /></summary><ul>{review.holdAssessments!.map(assessment => <li key={assessment.itemId}><div className="results-review-location"><span>{assessment.status === "preserved" ? "保留箇所を維持・修正との独立性を確認" : "安全性の確認が必要"}</span></div><p>{assessment.reason}</p></li>)}</ul></details>}
        {assessments.length > 0 && <details className="results-review-assessments"><summary>ルールごとの確認 <small>{assessments.length}</small><Icon name="down" size={12} /></summary><ul>{assessments.map(assessment => <li key={assessment.ruleId}><div className="results-review-location"><RuleChips ids={[assessment.ruleId]} rules={rules} onOpen={onOpenRule} /><span>{assessmentLabels[assessment.status] || assessment.status}</span></div><p>{assessment.reason}</p></li>)}</ul></details>}
        <details className="results-review-facts"><summary>レビュー情報<Icon name="down" size={12} /></summary><div className="results-facts"><div><span>候補</span><code title={review.candidateId}>{review.candidateId.slice(0, 12) || "—"}</code></div><div><span>開始</span><span>{dateTime(review.startedAt)}</span></div><div><span>終了</span><span>{dateTime(review.finishedAt)}</span></div><UsageFacts usage={review.usage} /></div></details>
      </div>
    </details>;
  })}</>;
}
export function heldChangeDestination(change: ChangeReportItem, detail: FileDetail | null, view: CodeView, history: Attempt[]): { side: "before" | "after"; line: number; attemptIndex?: number } | null {
  if (!detail) return null;
  const preferred = view === "before" ? "before" : "after";
  const direct = heldSourceTarget(change, preferred, { before: detail.before ? detail.before.split(/\r?\n/).length : 0, after: detail.after ? detail.after.split(/\r?\n/).length : 0 });
  if (direct || !detail.cumulative || !change.sourceAttemptId) return direct;
  const attemptIndex = history.findIndex(attempt => attempt.id === change.sourceAttemptId);
  const attempt = history[attemptIndex];
  if (!attempt) return null;
  // Cumulative IDs retain the exact original row identity. A held location in
  // a rejected candidate must open that attempt, never the accepted file.
  const original = attempt.changes?.find((item, index) => change.id === `attempt:${attempt.id}:${index}:${item.id}` && item.ruleId === change.ruleId);
  if (!original) return null;
  const target = heldSourceTarget(original, preferred, { before: Infinity, after: Infinity });
  return target ? { ...target, attemptIndex } : null;
}

export function HeldChangeMenu({ changes, detail, view, history = [], onChoose }: { changes: ChangeReportItem[]; detail: FileDetail | null; view: CodeView; history?: Attempt[]; onChoose: (change: ChangeReportItem) => void }) {
  const held = recordedChanges(changes).filter(change => change.status === "needs_human");
  if (!held.length) return null;
  return <details className="results-held-menu" onKeyDown={event => {
    if (event.key === "Escape") { event.preventDefault(); event.currentTarget.open = false; event.currentTarget.querySelector("summary")?.focus(); }
  }}>
    <summary><Icon name="warning" size={13} />要確認 <b>{held.length}</b>件<Icon name="down" size={12} /></summary>
    <ul aria-label="要確認の箇所">{held.map((change, index) => {
      const target = heldChangeDestination(change, detail, view, history);
      return <li key={change.id || index}><button type="button" disabled={!detail} onClick={event => {
        const menu = event.currentTarget.closest("details");
        if (menu) menu.open = false;
        onChoose(change);
      }}>
        <span>{change.ruleId && <code>{change.ruleId}：</code>}{changeNote(change)}</span>
        <small>{target ? `${target.attemptIndex !== undefined ? `試行 ${target.attemptIndex + 1}・` : ""}${target.side === "before" ? "変更前" : "変更後"}の ${target.line} 行目を表示` : "位置未特定・ファイル全体を表示"}<Icon name="arrow" size={12} /></small>
      </button></li>;
    })}</ul>
  </details>;
}

function CodeChangeNotes({ annotations, rules, onOpenRule }: { annotations: LineRuleAnnotation[]; rules: Map<string, Rule>; onOpenRule: (id: string) => void }) {
  return <div className="results-line-rule-chips" aria-label="ルールと修正内容">{annotations.map((item, index) => {
    const title = item.ruleTitle || rules.get(item.ruleId)?.title || "ルールの詳細";
    return <div className="results-line-rule" data-needs-human={item.changes.some(change => change.status === "needs_human") || undefined} key={`${item.ruleId}:${index}`}>
      {item.ruleId && <span className="results-line-rule-id"><button type="button" data-change-origin={item.origin} title={[`${item.ruleId} · ${title}`, originLabel(item.origin)].filter(Boolean).join(" · ")} aria-label={[`${item.ruleId} ${title}の詳細`, originLabel(item.origin)].filter(Boolean).join(" · ")} onClick={() => onOpenRule(item.ruleId)}><code>{item.ruleId}</code></button><span>：</span></span>}
      <div className="results-line-rule-content">{item.changes.map((change, changeIndex) => <div className="results-line-change" key={change.id || changeIndex}>
        <span className="results-line-change-summary"><strong>{changeNote(change)}</strong><span className={`results-change-status change-${change.status}`}>{changeLabels[change.status]}</span></span>
      </div>)}</div>
    </div>;
  })}</div>;
}

export function ResultCode({ content, isDiff, view = "diff", changes = [], rules = new Map(), onOpenRule = () => {}, focus }: {
  content: string;
  isDiff: boolean;
  view?: CodeView;
  changes?: ChangeReportItem[];
  rules?: Map<string, Rule>;
  onOpenRule?: (id: string) => void;
  focus?: Pick<CodeFocus, "line" | "request">;
}) {
  const code = useRef<HTMLDivElement>(null);
  const lines = useMemo(() => isDiff ? parseUnifiedDiff(content).filter(line => line.type !== "meta" || line.text.startsWith("\\ No newline")) : content.split(/\r?\n/).map((text, index) => ({ text, type: "source", oldLine: null, newLine: index + 1 })), [content, isDiff]);
  const start = !isDiff && focus?.line && focus.line > 10000 ? Math.max(0, focus.line - 201) : 0;
  const visible = lines.slice(start, start + 10000);
  const annotations = isDiff
    ? diffRuleAnnotations(changes, visible)
    : visible.map(line => rulesForLine(changes, view === "before" ? "before" : "after", line.newLine));
  const shownChanges = new Set(annotations.flatMap(annotation => annotation.flatMap(item => item.changes)));
  const unplacedChanges = recordedChanges(changes).filter(change => !shownChanges.has(change));
  useEffect(() => {
    if (!focus || isDiff || !code.current) return;
    const target = focus.line ? code.current.querySelector<HTMLElement>(`[data-source-line="${focus.line}"]`) : code.current;
    target?.scrollIntoView({ block: focus.line ? "center" : "start", inline: "nearest" });
    target?.focus({ preventScroll: true });
    // Only an explicit navigation request scrolls. Live refreshes preserve the view.
  }, [focus?.request, isDiff, view]);
  return <div className={`results-code${isDiff ? " is-diff" : ""}`} ref={code} tabIndex={-1} aria-label={isDiff ? "変更前・変更後の行番号付き差分" : "ソースコード"}>
    <table>
      <tbody>
      {unplacedChanges.length > 0 && <tr className="results-code-unplaced-row"><td colSpan={isDiff ? 3 : 2}>
        <p className="results-code-unplaced-label">{unplacedChanges.every(change => change.attributionVersion === 2 && change.lineRanges?.length) ? "表示範囲外の対応記録" : "位置未特定の対応記録"}</p>
        <CodeChangeNotes annotations={unplacedChanges.map(change => ({ ruleId: change.ruleId, ruleTitle: change.ruleTitle, origin: change.origin, changes: [change] }))} rules={rules} onOpenRule={onOpenRule} />
      </td></tr>}
      {visible.map((line, index) => {
        const badges = annotations[index];
        const signature = badges.map(item => `${item.ruleId}:${item.origin || ""}:${item.changes.map(change => change.id).join(",")}`).join("|");
        const previousSignature = index > 0 ? annotations[index - 1].map(item => `${item.ruleId}:${item.origin || ""}:${item.changes.map(change => change.id).join(",")}`).join("|") : "";
        const showBadges = badges.length > 0 && (isDiff || signature !== previousSignature);
        return <Fragment key={index}>
          {showBadges && <tr className="results-code-rules-row"><td colSpan={isDiff ? 3 : 2}><CodeChangeNotes annotations={badges} rules={rules} onOpenRule={onOpenRule} /></td></tr>}
          <tr className={`${line.type}${!isDiff && focus?.line === line.newLine ? " is-held-target" : ""}`} data-source-line={!isDiff ? line.newLine : undefined} tabIndex={!isDiff && focus?.line === line.newLine ? -1 : undefined}>{isDiff && <td className="results-line-number">{line.oldLine}</td>}<td className="results-line-number">{line.newLine}</td><td><pre>{line.text || " "}</pre></td></tr>
        </Fragment>;
      })}</tbody>
    </table>
    {lines.length > 10000 && <p className="results-inline-note">{start ? `${num(start + 1)}〜${num(Math.min(lines.length, start + 10000))}行目を表示しています。` : "先頭10,000行を表示しています。"}全文は作業コピーで確認できます。</p>}
  </div>;
}
