import { useEffect, useMemo, useState } from "react";
import { api } from "./bridge";
import { Icon } from "./icons";
import { ResultsPanel, type ResultsPanelProps } from "./ResultsPanel";
import { isCompletedTask } from "./task-state";
import type { ExecutionRun, ExecutionRunResult } from "./types";

const statusLabels: Record<ExecutionRun["status"], string> = {
  running: "実行中", completed: "終了", stopped: "停止", failed: "エラー",
};
export function executionLabel(run: ExecutionRun): string {
  const date = new Date(run.startedAt);
  const time = Number.isNaN(date.getTime()) ? run.startedAt : date.toLocaleString("ja-JP", { year: "numeric", month: "2-digit", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
  return `${time} · ${statusLabels[run.status]} · ${run.targetCount} ファイル`;
}

export function ExecutionResultsPanel(props: ResultsPanelProps) {
  const runs = props.state.executionRuns || [];
  const latest = runs.at(-1);
  const [selection, setSelection] = useState("");
  const [fileChoice, setFileChoice] = useState({ key: "", file: "" });
  const run = runs.find(item => item.id === selection) || latest;
  const [requestNumber, setRequestNumber] = useState(0);
  const [result, setResult] = useState<{ key: string; data?: ExecutionRunResult; error?: string }>();
  const key = `${props.state.activeWorkspaceId}:${run?.id || ""}`;
  const data = result?.key === key ? result.data : undefined;
  const error = result?.key === key ? result.error : undefined;
  const targets = useMemo(() => new Set(data?.targetFiles || []), [data?.targetFiles]);
  const preferredFile = fileChoice.key === key ? fileChoice.file : targets.has(props.selectedFile) ? props.selectedFile : "";
  const selectedFile = data?.state.tasks.some(task => task.file === preferredFile && (targets.has(task.file) || isCompletedTask(task)))
    ? preferredFile : data?.targetFiles[0] || data?.state.tasks.find(isCompletedTask)?.file || "";

  useEffect(() => {
    if (!run) return;
    let canceled = false;
    let timer: ReturnType<typeof setTimeout>;
    async function refresh() {
      try {
        const snapshot = await api.GetExecutionRun(run!.id);
        if (!canceled) setResult({ key, data: snapshot });
      } catch (reason) {
        if (!canceled) setResult({ key, error: reason instanceof Error ? reason.message : String(reason) });
      }
      // Sequential polling avoids canceling a large snapshot request each time
      // the main workspace refreshes. Completed and older runs never poll.
      if (!canceled && run!.status === "running") timer = setTimeout(() => { void refresh(); }, 1000);
    }
    void refresh();
    return () => { canceled = true; clearTimeout(timer); };
  }, [key, run?.status, run?.finishedAt, requestNumber]);

  if (!run) return <ResultsPanel {...props} />;
  return <section className="execution-results" aria-label="実行履歴と結果">
    <div className="execution-run-selector">
      <label htmlFor="execution-run"><Icon name="clock" size={15} />実行履歴</label>
      <select id="execution-run" value={selection && runs.some(item => item.id === selection) ? selection : ""} onChange={event => setSelection(event.target.value)}>
        <option value="">最新の実行 · {latest && executionLabel(latest)}</option>
        {[...runs].reverse().map(item => <option key={item.id} value={item.id}>{executionLabel(item)}</option>)}
      </select>
    </div>
    {error ? <div className="results-detail-error" role="alert"><Icon name="warning" size={16} /><span>{error}</span><button className="results-text-button" onClick={() => setRequestNumber(value => value + 1)}>再読み込み</button></div>
      : data ? <ResultsPanel {...props} key={key} state={data.state} selectedFile={selectedFile}
        onSelectFile={file => { setFileChoice({ key, file }); props.onSelectFile(file); }}
        execution={{ id: run.id, targetFiles: targets, liveState: props.state, historical: run.id !== latest?.id }} />
        : <div className="results-empty" role="status"><span className="spinner" />実行結果を読み込み中…</div>}
  </section>;
}
