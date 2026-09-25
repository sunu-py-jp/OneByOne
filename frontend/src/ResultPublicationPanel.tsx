import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { api } from "./bridge";
import { Icon } from "./icons";
import { HoverTip } from "./HoverTip";
import { TargetFilePath } from "./TargetFolderBrowser";
import { ResultCode } from "./ResultsPanel";
import { useResultsSplitter } from "./useResultsSplitter";
import { publicationBlockReason, refreshPublicationDraft, type PublicationDraft } from "./result-publication";
import type { PublishResultsRequest, ResultPublication, ResultPublicationFile, ResultPublicationPreview, State } from "./types";
import "./result-publication.css";

const messageOf = (error: unknown) => error instanceof Error ? error.message : String(error);

export function ResultPublicationPanel({ state, busy, usable, initialDraft, onDraftChange, onPublish }: {
  state: State;
  busy: boolean;
  usable: boolean;
  initialDraft?: PublicationDraft;
  onDraftChange: (draft: PublicationDraft) => void;
  onPublish: (request: PublishResultsRequest) => Promise<ResultPublication>;
}) {
  const workspaceId = state.activeWorkspaceId;
  const [preview, setPreview] = useState<ResultPublicationPreview>();
  const [draft, setDraft] = useState<PublicationDraft | undefined>(initialDraft);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [publishError, setPublishError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [publishing, setPublishing] = useState(false);
  const [published, setPublished] = useState<ResultPublication>();
  const [selectedFile, setSelectedFile] = useState("");
  const [search, setSearch] = useState("");
  const [diffResult, setDiffResult] = useState<{ key: string; diff?: string; error?: string }>();
  const active = useRef(true);
  const publicationInFlight = useRef(false);
  const id = useId();
  const files = preview?.workspaceId === workspaceId ? preview.files : [];
  const selected = files.find(file => file.file === selectedFile);
  const filtered = useMemo(() => files.filter(file => file.file.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())), [files, search]);
  const splitter = useResultsSplitter(workspaceId, files.map(file => file.file), { extraWidth: 45, sampleSelector: ".publication-files-row" });
  const diffKey = `${workspaceId}:${preview?.revision || ""}:${selectedFile}`;
  const currentDiff = diffResult?.key === diffKey ? diffResult : undefined;
  const rules = useMemo(() => new Map(state.rules.map(rule => [rule.id, rule])), [state.rules]);
  const latestRun = state.executionRuns.at(-1);
  const sourceKey = `${state.worktree}:${latestRun?.id || ""}:${latestRun?.finishedAt || ""}:${state.tasks.map(task => `${task.updatedAt}:${task.discards?.length || 0}`).join("|")}`;

  useEffect(() => { active.current = true; return () => { active.current = false; }; }, []);
  useEffect(() => { if (draft?.workspaceId === workspaceId) onDraftChange(draft); }, [draft, workspaceId, onDraftChange]);
  useEffect(() => {
    let canceled = false;
    setLoading(true); setError("");
    if (state.running) { setLoading(false); return; }
    api.GetResultPublicationPreview().then(result => {
      if (canceled) return;
      if (result.workspaceId !== workspaceId) throw new Error("ワークスペースが変更されています。再読み込みしてください。");
      const next = { ...result, files: (result.files || []).map(file => ({ ...file, rulesApplied: file.rulesApplied || [] })), publications: result.publications || [] };
      setPreview(next);
      setDraft(previous => refreshPublicationDraft(previous, next));
      setSelectedFile(previous => next.files.some(file => file.file === previous) ? previous : next.files[0]?.file || "");
    }).catch(reason => { if (!canceled) setError(messageOf(reason)); }).finally(() => { if (!canceled) setLoading(false); });
    return () => { canceled = true; };
  }, [workspaceId, sourceKey, state.running, refresh]);

  useEffect(() => {
    let canceled = false;
    if (!selected || !preview || loading || error) return;
    api.GetResultPublicationFileDiff(workspaceId, preview.revision, selected.file).then(diff => {
      if (!canceled) setDiffResult({ key: diffKey, diff });
    }).catch(reason => { if (!canceled) setDiffResult({ key: diffKey, error: messageOf(reason) }); });
    return () => { canceled = true; };
  }, [workspaceId, preview?.revision, selected?.file, diffKey, loading, error, refresh]);

  const reason = publicationBlockReason({ preview, draft, busy: busy || publishing, running: state.running,
    readOnly: state.readOnly, usable, loading, error: error || currentDiff?.error || "" });
  const inputLocked = busy || publishing || state.running || state.readOnly;
  function edit(field: "branch" | "title" | "message", value: string) {
    setDraft(previous => previous ? { ...previous, [field]: value } : previous);
    setPublishError("");
  }
  async function publish() {
    if (reason || !preview || !draft || publicationInFlight.current) return;
    publicationInFlight.current = true;
    setPublishing(true); setPublishError("");
    try {
      const result = await onPublish({ workspaceId, revision: preview.revision, branch: draft.branch.trim(), title: draft.title.trim(), message: draft.message });
      if (!active.current) return;
      setPublished(result);
      setPreview(previous => previous ? { ...previous, publications: [...previous.publications, result] } : previous);
    } catch (failure) {
      if (active.current) setPublishError(messageOf(failure));
    } finally {
      publicationInFlight.current = false;
      if (active.current) setPublishing(false);
    }
  }

  return <section className="publication-panel" aria-label="結果反映">
    <div className="publication-intro">
      <span>処理開始前から最新の採用済み変更までを、新規ブランチの1コミットにまとめます。</span>
      <HoverTip reason={busy || publishing || state.running ? "処理が終わるまでお待ちください。" : ""}><button className="text-button" disabled={busy || publishing || state.running || loading} onClick={() => { setDiffResult(undefined); setRefresh(value => value + 1); }}><Icon name="refresh" size={14} />再読み込み</button></HoverTip>
    </div>
    {error && <div className="publication-error" role="alert"><Icon name="warning" size={16} /><span>{error}</span></div>}
    {loading && !preview ? <div className="target-browser-message" role="status"><span className="spinner" />反映内容を読み込み中…</div> : preview && draft && <>
      <form className="publication-form" onSubmit={event => { event.preventDefault(); void publish(); }}>
        <div className="publication-fields">
          <label htmlFor={`${id}-branch`}>新規ブランチ名<input id={`${id}-branch`} value={draft.branch} autoComplete="off" spellCheck={false} disabled={inputLocked} onChange={event => edit("branch", event.target.value)} required /></label>
          <label htmlFor={`${id}-title`}>コミットタイトル <span className="publication-required">必須</span><input id={`${id}-title`} value={draft.title} placeholder="変更内容をひとことで" disabled={inputLocked} onChange={event => edit("title", event.target.value)} required /></label>
        </div>
        <label htmlFor={`${id}-message`} className="publication-message-label">コミット本文<textarea id={`${id}-message`} value={draft.message} rows={5} disabled={inputLocked} onChange={event => edit("message", event.target.value)} /></label>
        <div className="publication-submit-row">
          {published ? <div className="publication-reflected" role="status"><Icon name="check" size={16} /><div><strong title={published.branch}>{published.branch}</strong><span title="元フォルダをSourceTreeなどで開き、このブランチを選択できます。">反映済み · <code title={published.commit}>{published.commit.slice(0, 12)}</code></span></div></div>
            : <span>{files.length.toLocaleString("ja-JP")}ファイル{preview.baseCommit && <> · 基点 <code title={preview.baseCommit}>{preview.baseCommit.slice(0, 8)}</code></>}</span>}
          <HoverTip reason={reason}><button className="button primary" type="submit" disabled={Boolean(reason)}>{publishing ? <span className="spinner" /> : <Icon name="branch" size={16} />}新規ブランチに反映</button></HoverTip>
        </div>
        {publishError && <div className="publication-error" role="alert"><Icon name="warning" size={16} /><span>{publishError}</span></div>}
      </form>
      {!files.length ? <div className="target-browser-message"><Icon name="file" size={24} />反映する変更がありません</div> : <div ref={splitter.ref} className="target-browser publication-browser" style={splitter.ready ? { gridTemplateColumns: `${splitter.width}px 9px minmax(0, 1fr)` } : undefined}>
        <div className="target-browser-list">
          <div className="target-browser-list-header">反映するファイル <span>{files.length.toLocaleString("ja-JP")}</span></div>
          <div className="target-browser-search"><Icon name="search" size={14} /><input aria-label="反映するファイルを検索" placeholder="ファイルを検索" value={search} onChange={event => setSearch(event.target.value)} /></div>
          <PublicationFiles files={filtered} selected={selectedFile} onSelect={setSelectedFile} />
        </div>
        <div className={`target-browser-splitter${splitter.resizing ? " is-resizing" : ""}`} role="separator" tabIndex={0} aria-label="反映するファイル一覧の幅" aria-orientation="vertical" aria-valuemin={splitter.bounds.min} aria-valuemax={splitter.bounds.max} aria-valuenow={splitter.width} {...splitter.handleProps}><span aria-hidden="true" /></div>
        <div className="target-browser-detail publication-detail">
          <div className="target-browser-detail-header"><Icon name="file" size={15} />{selected ? <TargetFilePath file={selected.file} /> : <span>変更内容</span>}</div>
          {selected && <div className="publication-file-summary">{selected.rulesApplied.length > 0 && <div className="publication-rule-chips" aria-label="適用したルール">{selected.rulesApplied.map(rule => <span key={rule} title={rules.get(rule)?.title || rule}>{rule}</span>)}</div>}{selected.summary && <p>{selected.summary}</p>}</div>}
          {loading ? <div className="target-browser-message" role="status"><span className="spinner" />更新中…</div> : error ? <div className="target-browser-message">反映内容を再読み込みしてください</div>
            : currentDiff?.error ? <div className="target-browser-message is-error" role="alert">{currentDiff.error}</div>
              : currentDiff?.diff !== undefined ? currentDiff.diff ? <ResultCode content={currentDiff.diff} isDiff /> : <div className="target-browser-message">表示できる差分がありません</div>
                : selected ? <div className="target-browser-message" role="status"><span className="spinner" />差分を読み込み中…</div> : <div className="target-browser-message">ファイルを選択してください</div>}
        </div>
      </div>}
      {preview.publications.length > 0 && <details className="publication-history"><summary>反映履歴 <span>{preview.publications.length}</span></summary><ul>{[...preview.publications].reverse().map(item => <li key={`${item.branch}:${item.commit}`}><Icon name="branch" size={14} /><div><strong>{item.branch}</strong><span>{item.title}</span></div><code title={item.commit}>{item.commit.slice(0, 12)}</code><time>{new Date(item.createdAt).toLocaleString("ja-JP")}</time></li>)}</ul></details>}
    </>}
  </section>;
}

const ROW_HEIGHT = 32;
function PublicationFiles({ files, selected, onSelect }: { files: ResultPublicationFile[]; selected: string; onSelect: (file: string) => void }) {
  const [element, setElement] = useState<HTMLDivElement | null>(null);
  const [viewport, setViewport] = useState({ top: 0, height: 320 });
  const id = useId();
  const index = files.findIndex(file => file.file === selected);
  const start = Math.max(0, Math.min(Math.floor(viewport.top / ROW_HEIGHT) - 6, files.length - 1));
  const end = Math.min(files.length, start + Math.ceil(viewport.height / ROW_HEIGHT) + 12);
  useEffect(() => {
    if (!element) return;
    const observer = new ResizeObserver(() => setViewport(previous => ({ ...previous, height: element.clientHeight })));
    observer.observe(element);
    return () => observer.disconnect();
  }, [element]);
  useEffect(() => { if (element) element.scrollTop = 0; setViewport(previous => ({ ...previous, top: 0 })); }, [files, element]);
  function navigate(event: KeyboardEvent<HTMLDivElement>) {
    let next: number;
    if (event.key === "ArrowDown") next = Math.min(files.length - 1, index + 1);
    else if (event.key === "ArrowUp") next = Math.max(0, index - 1);
    else if (event.key === "Home") next = 0;
    else if (event.key === "End") next = files.length - 1;
    else return;
    if (!files[next]) return;
    event.preventDefault(); onSelect(files[next].file);
    if (element) {
      const top = next * ROW_HEIGHT;
      if (top < element.scrollTop) element.scrollTop = top;
      else if (top + ROW_HEIGHT > element.scrollTop + element.clientHeight) element.scrollTop = top + ROW_HEIGHT - element.clientHeight;
    }
  }
  if (!files.length) return <div className="target-browser-message">一致するファイルがありません</div>;
  return <div className="target-browser-file-scroll" ref={setElement} role="listbox" aria-label="反映するファイル一覧" tabIndex={0} onKeyDown={navigate}
    aria-activedescendant={index >= start && index < end ? `${id}-${index}` : undefined}
    onScroll={event => { const top = event.currentTarget.scrollTop; setViewport(previous => ({ ...previous, top })); }}>
    <div style={{ height: files.length * ROW_HEIGHT, position: "relative" }}><div style={{ position: "absolute", top: start * ROW_HEIGHT, left: 0, right: 0 }}>
      {files.slice(start, end).map((file, offset) => <div key={file.file} id={`${id}-${start + offset}`} className={`target-browser-file publication-files-row${selected === file.file ? " is-selected" : ""}`} role="option" aria-selected={selected === file.file} aria-label={file.file} aria-posinset={start + offset + 1} aria-setsize={files.length}
        onClick={() => { element?.focus({ preventScroll: true }); onSelect(file.file); }}><Icon name="file" size={14} /><TargetFilePath file={file.file} /></div>)}
    </div></div>
  </div>;
}
