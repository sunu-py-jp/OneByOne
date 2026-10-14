import { useEffect, useRef, useState } from "react";
import { api } from "./bridge";
import type { AgentTraceContent, AgentTraceEntry, AgentTracePage } from "./types";
import "./solution-trace.css";

export interface SolutionTraceScope {
  workspaceId: string;
  executionId: string;
  solutionRunId: string;
  running: boolean;
}

const pageSize = 50;
const pollMilliseconds = 3000;
const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error);
const phaseLabel = (phase: string) => ({ repair: "修正", review: "独立レビュー" } as Record<string, string>)[phase] || phase;
const kindLabel = (kind: string) => ({ session: "AIの会話", request: "AIへの要求", tool: "ツール操作", decision: "判定", context: "参照情報" } as Record<string, string>)[kind] || kind;
const bytesLabel = (bytes: number) => bytes < 1024 ? `${bytes} B` : `${(bytes / 1024).toFixed(1)} KiB`;
export const traceScopeKey = (scope: SolutionTraceScope) => JSON.stringify([scope.workspaceId, scope.executionId, scope.solutionRunId]);
export const traceStatusLabel = (status: string, running: boolean) => status === "running" ? running ? "実行中" : "未完了" : ({ completed: "完了", error: "エラー" } as Record<string, string>)[status] || status;
const timeLabel = (value: string) => {
  if (!value) return "時刻の記録なし";
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString("ja-JP", { hour12: false });
};

// Keep every expansion and request scoped to one immutable execution identity.
export function SolutionTracePanel(props: SolutionTraceScope) {
  return <SolutionTraceDisclosure key={traceScopeKey(props)} {...props} />;
}

export function SolutionTraceDisclosure(props: SolutionTraceScope) {
  const [open, setOpen] = useState(false);
  return <details className="solution-trace-panel" open={open} onToggle={event => setOpen(event.currentTarget.open)}>
    <summary><strong>AIの操作履歴</strong><span>ツール・要求・判定の記録</span></summary>
    {open && <div className="solution-trace-panel-body">
      <p className="solution-hint">選択したソリューションの記録です。行を開き、必要な引数・戻り値だけを表示できます。</p>
      <SolutionTraceList {...props} />
    </div>}
  </details>;
}

function mergePage(previous: AgentTracePage | null, page: AgentTracePage): AgentTracePage {
  const entries = new Map((previous?.entries || []).map(entry => [entry.sequence, entry]));
  for (const entry of page.entries || []) entries.set(entry.sequence, entry);
  const nextOffset = Math.max(previous?.nextOffset || 0, page.nextOffset);
  return { entries: [...entries.values()].sort((a, b) => a.sequence - b.sequence), nextOffset, total: page.total, complete: nextOffset >= page.total };
}

export function SolutionTraceList(props: SolutionTraceScope) {
  const [page, setPage] = useState<AgentTracePage | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const snapshot = useRef<AgentTracePage | null>(null);
  const request = useRef<((mode: "more" | "refresh") => void) | null>(null);
  const key = traceScopeKey(props);
  useEffect(() => {
    let canceled = false;
    let fetching = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    async function fetch(mode: "more" | "refresh") {
      if (canceled || fetching) return;
      clearTimeout(timer);
      fetching = true; setLoading(true); setError("");
      const previous = snapshot.current;
      let next = previous;
      try {
        const offsets = new Set<number>();
        if (mode === "more") offsets.add(previous?.nextOffset || 0);
        else {
          // Refresh unfinished rows and the last visible page. Do not reread
          // completed pages or silently load an unbounded backlog.
          for (const [index, entry] of (previous?.entries || []).entries()) {
            if (entry.status === "running") offsets.add(Math.floor(index / pageSize) * pageSize);
          }
          offsets.add(Math.floor(Math.max(0, (previous?.nextOffset || 0) - 1) / pageSize) * pageSize);
        }
        const read = async (offset: number) => {
          const value = await api.ListSolutionTrace(props.workspaceId, props.executionId, props.solutionRunId, offset);
          if (!canceled) next = mergePage(next, value);
        };
        for (const offset of [...offsets].sort((a, b) => a - b)) { await read(offset); if (canceled) return; }
        // If the previously visible tail ended exactly on a page boundary,
        // fetch one new page when new actions arrive. Larger backlogs keep
        // their explicit "show more" control.
        if (mode === "refresh" && previous?.complete && next && next.nextOffset === previous.nextOffset && !next.complete) await read(next.nextOffset);
        if (!canceled) { snapshot.current = next; setPage(next); }
      } catch (failure) {
        if (!canceled) setError(errorMessage(failure));
      } finally {
        fetching = false;
        if (!canceled) {
          setLoading(false);
          if (props.running) timer = setTimeout(() => { void fetch("refresh"); }, pollMilliseconds);
        }
      }
    }
    request.current = mode => { void fetch(mode); };
    void fetch("refresh");
    return () => { canceled = true; clearTimeout(timer); request.current = null; };
  }, [key, props.running]);
  return <div className="solution-trace-list" aria-label="AI操作の一覧" aria-busy={loading}>
    <div className="solution-trace-toolbar"><span>{page ? `${page.entries.length} / ${page.total} 件表示` : "履歴を読み込み中…"}</span>
      <button type="button" className="text-button" disabled={loading} onClick={() => request.current?.("refresh")}>一覧を更新</button></div>
    {props.running && <p className="solution-hint" role="status">開いている間は3秒ごとに更新します。</p>}
    {error && <p className="field-validation-error" role="alert">{error}<button type="button" className="text-button" disabled={loading} onClick={() => request.current?.("refresh")}>再読み込み</button></p>}
    {page?.entries.map(entry => <SolutionTraceRow key={entry.sequence} scope={props} entry={entry} />)}
    {page && !page.entries.length && <p className="solution-hint">{props.running ? "AIの操作が記録されると表示します。" : "操作履歴はありません。この機能の追加前に終了した実行には記録がありません。"}</p>}
    {page && !page.complete && <button type="button" className="button solution-trace-more" disabled={loading} onClick={() => request.current?.("more")}>{loading ? "読み込み中…" : "さらに表示（最大50件）"}</button>}
  </div>;
}

export function SolutionTraceRow({ scope, entry }: { scope: SolutionTraceScope; entry: AgentTraceEntry }) {
  const [open, setOpen] = useState(false);
  return <details className="solution-trace-row" open={open} onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>
      <span className="solution-trace-row-meta">反復 {entry.iteration + 1} · {phaseLabel(entry.phase)} · 会話 {entry.session}{entry.turn > 0 && <> · 応答 {entry.turn}</>}<time dateTime={entry.startedAt}>{timeLabel(entry.startedAt)}</time></span>
      <span className="solution-trace-row-title"><code>{entry.tool || kindLabel(entry.kind)}</code><span className={`solution-trace-status trace-${entry.status}`}>{traceStatusLabel(entry.status, scope.running)}</span></span>
      <span className="solution-trace-summary">{entry.summary || "概要の記録なし"}</span>
    </summary>
    {open && <div className="solution-trace-row-body">
      <dl className="solution-trace-facts"><dt>記録番号</dt><dd>{entry.sequence}</dd><dt>種別</dt><dd>{kindLabel(entry.kind)}</dd>
        {entry.callId && <><dt>呼び出しID</dt><dd>{entry.callId}</dd></>}{entry.finishedAt && <><dt>終了時刻</dt><dd>{timeLabel(entry.finishedAt)}</dd></>}
        {!!entry.usage?.turns && <><dt>使用量</dt><dd>入力 {entry.usage.inputTokens.toLocaleString("ja-JP")} / 出力 {entry.usage.outputTokens.toLocaleString("ja-JP")} tokens</dd></>}
      </dl>
      <SolutionTracePayloadDisclosure scope={scope} entry={entry} section="arguments" />
      <SolutionTracePayloadDisclosure scope={scope} entry={entry} section="output" />
    </div>}
  </details>;
}

export function SolutionTracePayloadDisclosure({ scope, entry, section }: { scope: SolutionTraceScope; entry: AgentTraceEntry; section: "arguments" | "output" }) {
  const [open, setOpen] = useState(false);
  const bytes = section === "arguments" ? entry.argumentsBytes : entry.outputBytes;
  return <details className="solution-trace-payload" open={open} onToggle={event => setOpen(event.currentTarget.open)}>
    <summary>{section === "arguments" ? "引数を表示" : "戻り値を表示"}<span>{bytesLabel(bytes)}</span></summary>
    {open && <SolutionTracePayload key={`${traceScopeKey(scope)}:${entry.sequence}:${section}:${bytes}:${entry.status}`} scope={scope} sequence={entry.sequence} section={section} pending={entry.status === "running" && scope.running} />}
  </details>;
}

export function traceDisplayContent(content: string, complete: boolean): { text: string; embedded?: string } {
  if (!complete) return { text: content };
  try {
    const parsed: unknown = JSON.parse(content);
    if (parsed && !Array.isArray(parsed) && typeof parsed === "object" && "content" in parsed && typeof parsed.content === "string") {
      return { text: JSON.stringify({ ...parsed, content: "（本文は下に表示）" }, null, 2), embedded: parsed.content };
    }
    return { text: JSON.stringify(parsed, null, 2) };
  } catch { return { text: content }; }
}

export function SolutionTracePayload({ scope, sequence, section, pending }: { scope: SolutionTraceScope; sequence: number; section: "arguments" | "output"; pending: boolean }) {
  const [payload, setPayload] = useState<AgentTraceContent | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const snapshot = useRef<AgentTraceContent | null>(null);
  const request = useRef<(() => void) | null>(null);
  useEffect(() => {
    let canceled = false;
    let fetching = false;
    async function fetch() {
      if (canceled || fetching) return;
      fetching = true; setLoading(true); setError("");
      const previous = snapshot.current;
      try {
        const value = await api.ReadSolutionTrace(scope.workspaceId, scope.executionId, scope.solutionRunId, sequence, section, previous?.nextOffset || 0);
        if (!canceled) {
          const next = { ...value, content: (previous?.content || "") + value.content };
          snapshot.current = next; setPayload(next);
        }
      } catch (failure) { if (!canceled) setError(errorMessage(failure)); }
      finally { fetching = false; if (!canceled) setLoading(false); }
    }
    request.current = () => { void fetch(); };
    void fetch();
    return () => { canceled = true; request.current = null; };
  }, [scope.workspaceId, scope.executionId, scope.solutionRunId, sequence, section]);
  const display = payload ? traceDisplayContent(payload.content, payload.complete) : null;
  return <div className="solution-trace-content" aria-busy={loading}>
    {display && (display.text ? <><pre>{display.text}</pre>{display.embedded !== undefined && <><span className="solution-hint">本文（content）</span><pre>{display.embedded}</pre></>}</> : <p className="solution-hint">{pending ? "内容はまだ記録されていません。" : "内容はありません。"}</p>)}
    {loading && <p className="solution-hint" role="status">読み込み中…</p>}
    {error && <p className="field-validation-error" role="alert">{error}<button type="button" className="text-button" disabled={loading} onClick={() => request.current?.()}>再読み込み</button></p>}
    {payload && <div className="solution-trace-toolbar"><span>{bytesLabel(payload.nextOffset)} / {bytesLabel(payload.totalBytes)} 表示</span>
      {!payload.complete && <button type="button" className="text-button" disabled={loading} onClick={() => request.current?.()}>続きを表示</button>}</div>}
  </div>;
}
