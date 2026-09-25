import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { HoverTip } from "./HoverTip";
import { Icon } from "./icons";
import { TargetFilePath } from "./TargetFolderBrowser";
import { compareTaskStatus, isCompletedTask, taskDisplayStatus, taskStatusLabel } from "./task-state";
import type { Task } from "./types";
import "./task-file-list.css";

export interface TaskFileListProps {
  tasks: Task[];
  selectedFile: string;
  onSelectFile: (file: string) => void;
  selection?: { files: Set<string>; onChange: (files: Set<string>) => void; disabled: boolean; reason?: string };
  onRetry?: (file: string) => void;
  retryDisabled?: boolean;
  retryDisabledReason?: string;
  completedOpen?: boolean;
  onCompletedOpenChange?: (open: boolean) => void;
  /** Results retain this run's completed files in the main list. Omit for status-only grouping. */
  runTargetFiles?: ReadonlySet<string>;
  emptyMessage?: string;
}

export function partitionTaskFiles(tasks: readonly Task[], runTargetFiles?: ReadonlySet<string>) {
  const active: Task[] = [];
  const completed: Task[] = [];
  for (const task of [...tasks].sort(compareTaskStatus)) {
    (isCompletedTask(task) && !runTargetFiles?.has(task.file) ? completed : active).push(task);
  }
  return { active, completed };
}

/** Identical source/result navigation; only execution settings supply selection. */
export function TaskFileList({ tasks, selectedFile, onSelectFile, selection, onRetry, retryDisabled = false, retryDisabledReason,
  completedOpen, onCompletedOpenChange, runTargetFiles, emptyMessage = "条件に一致するファイルがありません" }: TaskFileListProps) {
  const [localCompletedOpen, setLocalCompletedOpen] = useState(false);
  const completedId = useId();
  const open = completedOpen ?? localCompletedOpen;
  const { active, completed } = useMemo(() => partitionTaskFiles(tasks, runTargetFiles), [tasks, runTargetFiles]);
  const visibleTasks = useMemo(() => open ? [...active, ...completed] : active, [active, completed, open]);
  const rowProps = { selectedFile, onSelectFile, selection, onRetry, retryDisabled, retryDisabledReason };
  function toggleCompleted() {
    if (completedOpen === undefined) setLocalCompletedOpen(!open);
    onCompletedOpenChange?.(!open);
  }
  if (!tasks.length) return <div className="task-list-empty"><Icon name="search" size={23} /><span>{emptyMessage}</span></div>;
  return <div className="task-file-list">
    <VirtualTaskRows id={completedId} tasks={visibleTasks} completedStart={active.length} completedCount={completed.length}
      completedOpen={open} onToggleCompleted={toggleCompleted}
      label="ファイル一覧" {...rowProps} />
  </div>;
}

const ROW_HEIGHT = 34;
function VirtualTaskRows({ id, tasks, label, completedStart, completedCount, completedOpen, onToggleCompleted, selectedFile, onSelectFile, selection, onRetry, retryDisabled, retryDisabledReason }: Pick<TaskFileListProps,
  "tasks" | "selectedFile" | "onSelectFile" | "selection" | "onRetry" | "retryDisabled" | "retryDisabledReason"> & { id: string; label: string; completedStart: number; completedCount: number; completedOpen: boolean; onToggleCompleted: () => void }) {
  const [element, setElement] = useState<HTMLDivElement | null>(null);
  const [viewport, setViewport] = useState({ top: 0, height: 400 });
  const wasCompletedOpen = useRef(false);
  const orderKey = tasks.map(task => task.file).join("\0");
  const selectedIndex = tasks.findIndex(task => task.file === selectedFile);
  const hasBoundary = completedCount > 0;
  const scrollRow = Math.floor(viewport.top / ROW_HEIGHT);
  const taskRow = scrollRow - (hasBoundary && scrollRow > completedStart ? 1 : 0);
  const start = Math.max(0, Math.min(taskRow - 8, tasks.length - 1));
  const end = Math.min(tasks.length, start + Math.ceil(viewport.height / ROW_HEIGHT) + 16);
  const activeStart = Math.min(start, completedStart);
  const activeEnd = Math.min(end, completedStart);
  const completedRowStart = Math.max(start, completedStart);
  const completedRowEnd = Math.max(end, completedStart);
  const rowTop = (index: number) => (index + (hasBoundary && index >= completedStart ? 1 : 0)) * ROW_HEIGHT;
  useEffect(() => {
    if (!element) return;
    const update = () => setViewport(previous => ({ ...previous, top: element.scrollTop, height: element.clientHeight }));
    update();
    const observer = new ResizeObserver(update);
    observer.observe(element);
    return () => observer.disconnect();
  }, [element]);
  useEffect(() => {
    if (!element) return;
    const expanded = completedOpen && !wasCompletedOpen.current;
    const collapsed = !completedOpen && wasCompletedOpen.current;
    wasCompletedOpen.current = completedOpen;
    if (expanded) element.scrollTop = selectedIndex >= completedStart ? rowTop(selectedIndex) - ROW_HEIGHT : completedStart * ROW_HEIGHT;
    else if (collapsed) element.scrollTop = Math.min(element.scrollTop, Math.max(0, element.scrollHeight - element.clientHeight));
    else if (selectedIndex < 0) element.scrollTop = 0;
    else {
      const top = rowTop(selectedIndex);
      const bottomInset = hasBoundary && selectedIndex < completedStart ? ROW_HEIGHT : 0;
      if (top < element.scrollTop) element.scrollTop = top;
      else if (top + ROW_HEIGHT > element.scrollTop + element.clientHeight - bottomInset) element.scrollTop = top + ROW_HEIGHT - element.clientHeight + bottomInset;
    }
    setViewport(previous => previous.top === element.scrollTop ? previous : { ...previous, top: element.scrollTop });
  }, [element, selectedFile, orderKey, completedOpen, completedStart, hasBoundary]);
  function toggle(task: Task, checked: boolean) {
    if (!selection || selection.disabled || isCompletedTask(task)) return;
    const next = new Set(selection.files);
    if (checked) next.add(task.file); else next.delete(task.file);
    selection.onChange(next);
  }
  function navigate(event: KeyboardEvent<HTMLDivElement>) {
    if (event.target instanceof HTMLInputElement || (event.target instanceof HTMLElement && event.target.closest(".task-list-retry, .task-completed-toggle"))) return;
    let index: number;
    if (event.key === "ArrowDown") index = Math.min(tasks.length - 1, selectedIndex + 1);
    else if (event.key === "ArrowUp") index = Math.max(0, selectedIndex - 1);
    else if (event.key === "Home") index = 0;
    else if (event.key === "End") index = tasks.length - 1;
    else return;
    if (!tasks[index]) return;
    event.preventDefault();
    element?.focus({ preventScroll: true });
    onSelectFile(tasks[index].file);
  }
  function renderRows(from: number, to: number) {
    return tasks.slice(from, to).map((task, offset) => <div key={task.file} id={`${id}-${from + offset}`} role="listitem" aria-posinset={from + offset + 1} aria-setsize={tasks.length}
          className={`task-list-row${selectedFile === task.file ? " is-selected" : ""}${isCompletedTask(task) ? " is-completed" : ""}${selection && !isCompletedTask(task) && !selection.files.has(task.file) ? " is-excluded" : ""}`}>
          {isCompletedTask(task) && onRetry ? <HoverTip reason={retryDisabled ? retryDisabledReason || "現在は再試行に追加できません" : ""}>
            <button type="button" className="task-list-retry" aria-label={`${task.file}を再試行に追加`} title="再試行に追加" disabled={retryDisabled} onClick={() => { onSelectFile(task.file); onRetry(task.file); }}><Icon name="undo" size={13} /></button>
          </HoverTip> : selection && !isCompletedTask(task) ? <HoverTip reason={selection.disabled ? selection.reason || "現在は処理対象を変更できません" : ""}>
            <input type="checkbox" aria-label={`${task.file}を処理対象にする`} checked={selection.files.has(task.file)} disabled={selection.disabled} onChange={event => toggle(task, event.target.checked)} />
          </HoverTip> : <Icon name="file" size={13} />}
          <button type="button" className="task-file-open" aria-label={`${task.file}の内容を表示`} aria-pressed={selectedFile === task.file} onClick={() => onSelectFile(task.file)}><TargetFilePath file={task.file} /></button>
          <TaskListStatus task={task} />
        </div>);
  }
  return <div ref={setElement} className="task-list-scroll" role="list" aria-label={label} tabIndex={0} onKeyDown={navigate}
    onScroll={event => { const top = event.currentTarget.scrollTop; setViewport(previous => ({ ...previous, top })); }}>
    <div className="task-list-canvas" style={{ height: (tasks.length + (hasBoundary ? 1 : 0)) * ROW_HEIGHT }}>
      <div className="task-list-group" style={{ height: completedStart * ROW_HEIGHT }}>
        <div className="task-list-window" style={{ top: activeStart * ROW_HEIGHT }}>{renderRows(activeStart, activeEnd)}</div>
      </div>
      {hasBoundary && <>
        <div className="task-completed-boundary">
          <button type="button" className="task-completed-toggle" aria-expanded={completedOpen} aria-controls={id} onClick={onToggleCompleted}>
            <Icon name="chevron" size={13} /><span>完了済み</span><b>{completedCount.toLocaleString()}</b>
          </button>
        </div>
        <div id={id} className="task-list-group" hidden={!completedOpen} style={{ height: (tasks.length - completedStart) * ROW_HEIGHT }}>
          <div className="task-list-window" style={{ top: (completedRowStart - completedStart) * ROW_HEIGHT }}>{renderRows(completedRowStart, completedRowEnd)}</div>
        </div>
      </>}
    </div>
  </div>;
}

export function TaskListStatus({ task }: { task: Task }) {
  return <span className={`task-list-status status-${taskDisplayStatus(task)}`}><i aria-hidden="true" />{taskStatusLabel(task)}</span>;
}
