import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { api } from "./bridge";
import { Icon } from "./icons";
import { HelpTip } from "./HelpTip";
import { HoverTip } from "./HoverTip";
import { TargetFilePath, TargetFileSource } from "./TargetFolderBrowser";
import { RulePreviewPane } from "./RulePreviewPane";
import { TaskFileList, TaskListStatus } from "./TaskFileList";
import { compareTaskStatus, isCompletedTask } from "./task-state";
import { useResultsSplitter } from "./useResultsSplitter";
import type { Rule, TargetFileContent, Task } from "./types";
import "./target-files-panel.css";

const message = (error: unknown) => error instanceof Error ? error.message : String(error);
type Candidate = { id: string; rule?: Rule };

export function TargetFilesPanel({ tasks, rules, workspaceId, root, selected, onSelection, disabled, disabledReason, selectionEnabled = true, onRetry, retryDisabled, retryDisabledReason, action }: {
  tasks: Task[];
  rules: Rule[];
  workspaceId: string;
  root: string;
  selected: Set<string>;
  onSelection: (files: Set<string>) => void;
  onOpenRule?: (id: string) => void;
  disabled: boolean;
  disabledReason?: string;
  selectionEnabled?: boolean;
  action?: ReactNode;
  onRetry?: (file: string) => void;
  retryDisabled?: boolean;
  retryDisabledReason?: string;
}) {
  const [search, setSearch] = useState("");
  const [onlySelected, setOnlySelected] = useState(false);
  const [activeFile, setActiveFile] = useState(() => initialFile(tasks, selected));
  const [detail, setDetail] = useState<TargetFileContent | null>(null);
  const [detailError, setDetailError] = useState("");
  const [detailRevision, setDetailRevision] = useState(0);
  const [ruleId, setRuleId] = useState("");
  const [ruleRevision, setRuleRevision] = useState(0);
  const [ruleDetail, setRuleDetail] = useState<Rule | null>(null);
  const [ruleError, setRuleError] = useState("");
  const selectAllRef = useRef<HTMLInputElement>(null);
  const detailRequest = useRef(0);
  const ruleRequest = useRef(0);
  const context = `${workspaceId}:${root}`;
  const taskKey = tasks.map(task => task.file).join("\0");
  const splitter = useResultsSplitter(context, tasks.map(task => task.file), { extraWidth: selectionEnabled ? 115 : 120, sampleSelector: ".task-file-open" });
  const mappedTasks = useMemo(() => {
    const common = rules.filter(rule => rule.always).map(rule => rule.id);
    const byId = new Map(rules.map(rule => [rule.id, rule]));
    return tasks.map(task => ({ task, candidates: [...new Set([...common, ...task.rules])].map(id => ({ id, rule: byId.get(id) })) }));
  }, [tasks, rules]);
  const filtered = useMemo(() => {
    const query = search.toLocaleLowerCase().trim();
    return mappedTasks.filter(({ task, candidates }) => (!onlySelected || !selectionEnabled || (!isCompletedTask(task) && selected.has(task.file))) &&
      (!query || task.file.toLocaleLowerCase().includes(query) || candidates.some(({ id, rule }) => `${id} ${rule?.title || ""}`.toLocaleLowerCase().includes(query))));
  }, [mappedTasks, search, onlySelected, selected, selectionEnabled]);
  const active = mappedTasks.find(({ task }) => task.file === activeFile);
  const visibleDetail = detail?.workspaceId === workspaceId && detail.root === root && detail.file === activeFile ? detail : null;
  const selectable = filtered.filter(({ task }) => !isCompletedTask(task));
  const allChecked = selectable.length > 0 && selectable.every(({ task }) => selected.has(task.file));
  const someChecked = selectable.some(({ task }) => selected.has(task.file));
  const selectedCount = tasks.filter(task => !isCompletedTask(task) && selected.has(task.file)).length;
  const selectionBlocked = disabled ? disabledReason || "現在は処理対象を変更できません" : "";
  const ruleVersion = JSON.stringify(rules.find(rule => rule.id === ruleId));

  useEffect(() => { if (selectAllRef.current) selectAllRef.current.indeterminate = someChecked && !allChecked; }, [someChecked, allChecked]);
  useEffect(() => {
    setActiveFile(previous => tasks.some(task => task.file === previous) ? previous : initialFile(tasks, selected));
  }, [taskKey, context]);
  useEffect(() => {
    const request = ++detailRequest.current;
    setDetail(null); setDetailError("");
    if (!active || !workspaceId || !root) return;
    api.ReadExecutionFile(workspaceId, root, activeFile).then(result => {
      if (detailRequest.current !== request) return;
      if (result.workspaceId !== workspaceId || result.root !== root || result.file !== activeFile) throw new Error("表示対象が変更されています。再読み込みしてください。");
      setDetail(result);
    }).catch(error => { if (detailRequest.current === request) setDetailError(message(error)); });
    return () => { detailRequest.current++; };
  }, [workspaceId, root, activeFile, Boolean(active), detailRevision]);
  useEffect(() => { ruleRequest.current++; setRuleId(""); setRuleDetail(null); setRuleError(""); }, [workspaceId, root]);
  useEffect(() => {
    if (ruleId && !active?.candidates.some(candidate => candidate.id === ruleId)) {
      ruleRequest.current++; setRuleId(""); setRuleDetail(null); setRuleError("");
    }
  }, [activeFile, taskKey, ruleId, ruleVersion]);
  useEffect(() => {
    const request = ++ruleRequest.current;
    setRuleDetail(null); setRuleError("");
    if (!ruleId) return;
    api.ReadRule(ruleId).then(rule => {
      if (ruleRequest.current !== request) return;
      if (!rule || rule.id !== ruleId) throw new Error("ルールが見つかりません。");
      setRuleDetail(rule);
    }).catch(error => { if (ruleRequest.current === request) setRuleError(message(error)); });
    return () => { ruleRequest.current++; };
  }, [workspaceId, root, ruleId, ruleVersion, ruleRevision]);

  function toggle(files: string[], checked: boolean) {
    const next = new Set(selected);
    files.forEach(file => checked ? next.add(file) : next.delete(file));
    onSelection(next);
  }
  function selectFile(file: string) {
    if (file === activeFile) return;
    detailRequest.current++; ruleRequest.current++;
    setDetail(null); setDetailError(""); setRuleId(""); setRuleDetail(null); setRuleError(""); setActiveFile(file);
  }
  function openRule(id: string) {
    ruleRequest.current++; setRuleDetail(null); setRuleError(""); setRuleId(id); setRuleRevision(value => value + 1);
  }

  return <section className="review-file-panel task-file-panel" aria-label={selectionEnabled ? "処理対象ファイルの選択" : "確定した処理対象"}>
    <div className="task-files-heading">
      <h2>{selectionEnabled ? "対象ファイル" : "確定した処理対象"} <span>{selectionEnabled ? `${selectedCount.toLocaleString()} / ` : ""}{tasks.length.toLocaleString()}</span></h2>
      <div className="target-file-actions">{action}<HelpTip label="対象ファイルと候補ルール">チェックしたファイルのうち、未修正・再試行・失敗を処理します。共通ルールはすべての対象に渡し、個別ルールは候補をもとにAIが内容を判断します。完了済みのファイルは一覧下部の再試行アイコンから処理対象へ戻せます。</HelpTip></div>
    </div>
    <div ref={splitter.ref} className="target-browser task-files-browser" style={splitter.ready ? { gridTemplateColumns: `${splitter.width}px 9px minmax(0, 1fr)` } : undefined}>
      <div className="target-browser-list">
        <div className="target-browser-list-header task-files-list-header">
          {selectionEnabled && <HoverTip reason={selectionBlocked || (!selectable.length ? "選択できるファイルがありません" : "")}><input ref={selectAllRef} type="checkbox" aria-label="検索に一致するファイルをすべて選択" checked={allChecked} disabled={disabled || !selectable.length} onChange={event => toggle(selectable.map(({ task }) => task.file), event.target.checked)} /></HoverTip>}
          <span>{filtered.length.toLocaleString()} ファイル</span>
          {selectionEnabled && <label className="task-selected-filter"><input type="checkbox" checked={onlySelected} onChange={event => setOnlySelected(event.target.checked)} />選択中のみ</label>}
        </div>
        <div className="target-browser-search"><Icon name="search" size={14} /><input aria-label="対象ファイルとルールを検索" placeholder="ファイル名・ルールで検索" value={search} onChange={event => setSearch(event.target.value)} /></div>
        <TaskFileList tasks={filtered.map(({ task }) => task)} selectedFile={activeFile} onSelectFile={selectFile}
          selection={selectionEnabled ? { files: selected, onChange: onSelection, disabled, reason: selectionBlocked } : undefined}
          onRetry={onRetry} retryDisabled={retryDisabled ?? disabled} retryDisabledReason={retryDisabledReason || selectionBlocked}
          emptyMessage={tasks.length ? "条件に一致するファイルがありません" : "対象ファイルがありません。対象フォルダとルールの条件を確認してください。"} />
      </div>
      <div className={`target-browser-splitter${splitter.resizing ? " is-resizing" : ""}`} role="separator" tabIndex={0}
        aria-label="ファイル一覧の幅" aria-orientation="vertical" aria-valuemin={splitter.bounds.min} aria-valuemax={splitter.bounds.max} aria-valuenow={splitter.width}
        title="ドラッグまたは左右キーで幅を変更。ダブルクリックで自動調整" {...splitter.handleProps}><span aria-hidden="true" /></div>
      <div className={`task-file-context${ruleId ? " has-rule-preview" : ""}`}>
        <div className="target-browser-detail">
          <div className="target-browser-detail-header task-content-header">
            <Icon name="file" size={15} />
            {active ? <><div className="task-content-identity"><TargetFilePath file={activeFile} /><div className="task-header-candidates"><CandidateChips candidates={active.candidates} activeId={ruleId} onOpen={openRule} /></div></div><TaskListStatus task={active.task} /></> : <span>ファイルの内容</span>}
          </div>
          {detailError ? <div className="target-browser-message is-error" role="alert"><Icon name="warning" size={18} /><span>{detailError}</span><button className="text-button" onClick={() => setDetailRevision(value => value + 1)}>再読み込み</button></div>
            : visibleDetail?.unavailableReason ? <div className="target-browser-message"><Icon name="file" size={24} /><span>{visibleDetail.unavailableReason}</span></div>
            : visibleDetail ? <TargetFileSource key={`${context}:${activeFile}:${detailRevision}`} content={visibleDetail.content} />
            : active ? <div className="target-browser-message" role="status"><span className="spinner" />読み込み中…</div>
            : <div className="target-browser-message"><Icon name="file" size={26} /><span>ファイルを選択してください</span></div>}
        </div>
        {ruleId && <RulePreviewPane rule={ruleDetail} loading={!ruleDetail && !ruleError} error={ruleError} onClose={() => { ruleRequest.current++; setRuleId(""); }} />}
      </div>
    </div>
  </section>;
}

function initialFile(tasks: Task[], selected: Set<string>) {
  const ordered = [...tasks].sort(compareTaskStatus);
  return ordered.find(task => !isCompletedTask(task) && selected.has(task.file))?.file
    || ordered.find(task => !isCompletedTask(task))?.file || ordered[0]?.file || "";
}

function CandidateChips({ candidates, activeId, onOpen }: { candidates: Candidate[]; activeId: string; onOpen: (id: string) => void }) {
  return <>{candidates.map(({ id, rule }) => <button key={id} className={`rule-tag task-candidate-chip${activeId === id ? " is-active" : ""}`} title={`${rule?.always ? "共通" : "個別"} · ${rule?.title || id}`} aria-label={`${id} ${rule?.title || ""}の詳細`} aria-pressed={activeId === id} onClick={() => onOpen(id)}>{id}</button>)}</>;
}
