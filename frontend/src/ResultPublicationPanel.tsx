import { useEffect, useId, useRef, useState, type MouseEvent } from "react";
import { api } from "./bridge";
import { Icon } from "./icons";
import { HoverTip } from "./HoverTip";
import { ResultCode } from "./ResultsPanel";
import { RuleMarkdown } from "./RuleMarkdown";
import { usePublicationDrawerResize } from "./usePublicationDrawerResize";
import { publicationBlockReason, publicationMessageFileMode, readPublicationFilePreference, refreshPublicationDraft, savePublicationFilePreference, type PublicationDraft } from "./result-publication";
import type { PublishResultsRequest, ResultPublication, ResultPublicationFile, ResultPublicationPreview, State } from "./types";
import "./result-publication.css";

const messageOf = (error: unknown) => error instanceof Error ? error.message : String(error);

// Match the exact repository-relative destination generated with the message.
// Never infer a file from its label, basename or an untrusted relative traversal.
export function publicationLinkFile(href: string, files: ResultPublicationFile[]): ResultPublicationFile | undefined {
  try {
    const path = decodeURIComponent(href);
    return files.find(file => file.linkPath && file.linkPath === path);
  } catch { return undefined; }
}

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
  const [messageMode, setMessageMode] = useState<"source" | "preview">("preview");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [publishError, setPublishError] = useState("");
  const [refresh, setRefresh] = useState(0);
  const [publishing, setPublishing] = useState(false);
  const [published, setPublished] = useState<ResultPublication>();
  const [selectedFile, setSelectedFile] = useState("");
  const [drawerOpen, setDrawerOpen] = useState(false);
  const [linkError, setLinkError] = useState("");
  const [diffResult, setDiffResult] = useState<{ key: string; diff?: string; error?: string }>();
  const active = useRef(true);
  const publicationInFlight = useRef(false);
  const opener = useRef<HTMLAnchorElement | null>(null);
  const closeButton = useRef<HTMLButtonElement | null>(null);
  const id = useId();
  const files = preview?.workspaceId === workspaceId ? preview.files : [];
  const reportFiles = preview?.workspaceId === workspaceId ? preview.reportFiles ?? files : [];
  const selected = reportFiles.find(file => file.file === selectedFile);
  const drawer = usePublicationDrawerResize(workspaceId);
  const diffKey = `${workspaceId}:${preview?.revision || ""}:${selectedFile}`;
  const currentDiff = diffResult?.key === diffKey ? diffResult : undefined;
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
      const normalizeFiles = (items: ResultPublicationFile[]) => items.map(file => ({ ...file, rulesApplied: file.rulesApplied || [] }));
      const next = { ...result, files: normalizeFiles(result.files || []), reportFiles: normalizeFiles(result.reportFiles ?? result.files ?? []), publications: result.publications || [] };
      setPreview(next);
      setDraft(previous => ({ ...refreshPublicationDraft(previous, next),
        messageAsFile: previous?.workspaceId === workspaceId ? previous.messageAsFile === true : readPublicationFilePreference(workspaceId) }));
      setSelectedFile(previous => next.reportFiles.some(file => file.file === previous) ? previous : "");
    }).catch(reason => { if (!canceled) setError(messageOf(reason)); }).finally(() => { if (!canceled) setLoading(false); });
    return () => { canceled = true; };
  }, [workspaceId, sourceKey, state.running, refresh]);

  useEffect(() => {
    let canceled = false;
    if (!drawerOpen || !selected || !preview || loading || error) return;
    api.GetResultPublicationFileDiff(workspaceId, preview.revision, selected.file).then(diff => {
      if (!canceled) setDiffResult({ key: diffKey, diff });
    }).catch(reason => { if (!canceled) setDiffResult({ key: diffKey, error: messageOf(reason) }); });
    return () => { canceled = true; };
  }, [workspaceId, preview?.revision, selected?.file, diffKey, drawerOpen, loading, error, refresh]);

  useEffect(() => { setDrawerOpen(false); setSelectedFile(""); setLinkError(""); opener.current = null; }, [workspaceId]);
  useEffect(() => {
    if (!drawerOpen) return;
    if (!selected) { setDrawerOpen(false); return; }
    closeButton.current?.focus({ preventScroll: true });
    const onKey = (event: globalThis.KeyboardEvent) => {
      if (event.key !== "Escape" || event.defaultPrevented) return;
      event.preventDefault(); closeDrawer();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [drawerOpen, selected?.file]);

  const reason = publicationBlockReason({ preview, draft, busy: busy || publishing, running: state.running,
    readOnly: state.readOnly, usable, loading, error: error || currentDiff?.error || "" });
  const inputLocked = busy || publishing || state.running || state.readOnly;
  const messageFileMode = publicationMessageFileMode(draft?.message || "", draft?.messageAsFile === true, preview?.messageFileThreshold);
  function edit(field: "branch" | "title" | "message", value: string) {
    setDraft(previous => previous ? { ...previous, [field]: value } : previous);
    setPublishError("");
    if (field === "message") setLinkError("");
  }
  function closeDrawer() {
    drawer.finish(true); setDrawerOpen(false);
    if (opener.current?.isConnected) opener.current.focus({ preventScroll: true });
    else closeButton.current?.closest(".publication-panel")?.querySelector<HTMLElement>(".publication-message-heading button[aria-pressed='true']")?.focus({ preventScroll: true });
  }
  function openMessageLink(href: string, event: MouseEvent<HTMLAnchorElement>) {
    // Safe external links keep Markdown's normal browser behavior.
    if (/^(?:https?:|mailto:|tel:|\/\/)/i.test(href)) return;
    event.preventDefault();
    const file = publicationLinkFile(href, reportFiles);
    if (!file || loading || error) {
      setLinkError(file ? "コミット内容を再読み込みしてから差分を開いてください。" : "このリンクはレポート対象のファイルではないため、差分を表示できません。");
      return;
    }
    setLinkError(""); opener.current = event.currentTarget;
    setSelectedFile(file.file); setDrawerOpen(true);
  }
  async function publish() {
    if (reason || !preview || !draft || publicationInFlight.current) return;
    publicationInFlight.current = true;
    setPublishing(true); setPublishError("");
    try {
      const result = await onPublish({ workspaceId, revision: preview.revision, branch: draft.branch.trim(), title: draft.title.trim(), message: draft.message, messageAsFile: messageFileMode.selected });
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

  return <section className="publication-panel" aria-label="コミット" ref={drawer.ref}>
    <div className="publication-scroll">
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
        <div className="publication-message">
          <div className="publication-message-heading">
            <div className="publication-message-options">
              <span id={`${id}-message-label`}>コミット本文</span>
              <label className="publication-message-file-choice"><input id={`${id}-message-as-file`} type="checkbox" checked={messageFileMode.selected}
                disabled={inputLocked || messageFileMode.required} aria-describedby={messageFileMode.selected ? `${id}-message-file-note` : undefined}
                onChange={event => {
                  const checked = event.target.checked;
                  setDraft(previous => previous ? { ...previous, messageAsFile: checked } : previous);
                  savePublicationFilePreference(workspaceId, checked); setPublishError("");
                }} />本文はファイルとしてコミットに含める</label>
            </div>
            <div className="segmented" role="group" aria-label="コミット本文の表示形式">
              <button type="button" className={messageMode === "source" ? "active" : ""} aria-label="コミット本文の原文" aria-pressed={messageMode === "source"} aria-controls={`${id}-message-content`} onClick={() => setMessageMode("source")}>{"</>"}</button>
              <button type="button" className={messageMode === "preview" ? "active" : ""} aria-label="コミット本文のプレビュー" aria-pressed={messageMode === "preview"} aria-controls={`${id}-message-content`} onClick={() => setMessageMode("preview")}>Preview</button>
            </div>
          </div>
          {messageFileMode.selected && <p id={`${id}-message-file-note`} className="publication-message-file-note" role="status">
            {messageFileMode.required && <>{messageFileMode.threshold.toLocaleString("ja-JP")}文字を超えるため、ファイルに保存します。 </>}
            本文は <code>OneByOne/yyyyMMddHHmmss_results.md</code> に保存し、コミットメッセージには参照先を記載します。
          </p>}
          <div id={`${id}-message-content`}>
            {messageMode === "source" ? <textarea id={`${id}-message`} aria-labelledby={`${id}-message-label`} className="code-input" value={draft.message} rows={8} disabled={inputLocked} onChange={event => edit("message", event.target.value)} spellCheck={false} />
              : <div className="publication-message-preview" aria-labelledby={`${id}-message-label`} role="region" tabIndex={0}>{draft.message ? <RuleMarkdown body={draft.message} onLinkClick={openMessageLink} reportCells /> : <span className="publication-message-empty">本文はありません。</span>}</div>}
          </div>
          {linkError && <div className="publication-link-error" role="status"><Icon name="info" size={14} />{linkError}</div>}
        </div>
        <div className="publication-submit-row">
          {published ? <div className="publication-reflected" role="status"><Icon name="check" size={16} /><div><strong title={published.branch}>{published.branch}</strong><span title="元フォルダをSourceTreeなどで開き、このブランチを選択できます。">反映済み · <code title={published.commit}>{published.commit.slice(0, 12)}</code></span>{published.reportPath && <span title={published.reportPath}>本文: {published.reportPath}</span>}</div></div>
            : <span>変更 {files.length.toLocaleString("ja-JP")}ファイル{messageFileMode.selected && <> ＋ 結果ファイル</>}{preview.baseCommit && <> · 基点 <code title={preview.baseCommit}>{preview.baseCommit.slice(0, 8)}</code></>}</span>}
          <HoverTip reason={reason}><button className="button primary" type="submit" disabled={Boolean(reason)}>{publishing ? <span className="spinner" /> : <Icon name="branch" size={16} />}新規ブランチに反映</button></HoverTip>
        </div>
        {publishError && <div className="publication-error" role="alert"><Icon name="warning" size={16} /><span>{publishError}</span></div>}
      </form>
      {!files.length && !(messageFileMode.selected && reportFiles.length) && <div className="target-browser-message"><Icon name="file" size={24} />コミットする変更がありません</div>}
      {preview.publications.length > 0 && <details className="publication-history"><summary>反映履歴 <span>{preview.publications.length}</span></summary><ul>{[...preview.publications].reverse().map(item => <li key={`${item.branch}:${item.commit}`}><Icon name="branch" size={14} /><div><strong>{item.branch}</strong><span>{item.title}</span></div><code title={item.commit}>{item.commit.slice(0, 12)}</code><time>{new Date(item.createdAt).toLocaleString("ja-JP")}</time></li>)}</ul></details>}
    </>}
    </div>
    <aside className={`publication-diff-drawer${drawerOpen && selected ? " is-open" : ""}${drawer.resizing ? " is-resizing" : ""}`} style={drawer.ready ? { width: drawer.width } : undefined}
      role="dialog" aria-modal="false" aria-label={selected ? `${selected.file} の差分` : "ファイルの差分"} aria-hidden={!drawerOpen || !selected} inert={!drawerOpen || !selected}>
      <div className="publication-drawer-grip" role="separator" tabIndex={drawerOpen ? 0 : -1} aria-label="差分表示の幅" aria-orientation="vertical"
        aria-valuemin={drawer.bounds.min} aria-valuemax={drawer.bounds.max} aria-valuenow={drawer.width} title="ドラッグまたは左右キーで幅を調整" {...drawer.handleProps}><span aria-hidden="true" /></div>
      <header className="publication-drawer-header"><strong title={selected?.file}>{selected?.file}</strong><button ref={closeButton} type="button" className="icon-button" aria-label="差分を閉じる" onClick={closeDrawer}><Icon name="close" size={17} /></button></header>
      <div className="publication-drawer-content">
        {loading ? <div className="target-browser-message" role="status"><span className="spinner" />更新中…</div> : error ? <div className="target-browser-message">コミット内容を再読み込みしてください</div>
          : currentDiff?.error ? <div className="target-browser-message is-error" role="alert">{currentDiff.error}</div>
            : currentDiff?.diff !== undefined ? currentDiff.diff ? <ResultCode content={currentDiff.diff} isDiff /> : <div className="target-browser-message">変更はありません</div>
              : selected && drawerOpen ? <div className="target-browser-message" role="status"><span className="spinner" />差分を読み込み中…</div> : null}
      </div>
    </aside>
  </section>;
}
