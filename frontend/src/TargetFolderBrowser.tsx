import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { api } from "./bridge";
import { Icon } from "./icons";
import { useResultsSplitter } from "./useResultsSplitter";
import type { TargetFileContent, TargetFileList } from "./types";
import "./target-browser.css";

const ROW_HEIGHT = 32;
const MAX_LINES = 20000;
const errorMessage = (error: unknown) => error instanceof Error ? error.message : String(error);
const number = (value: number) => value.toLocaleString("ja-JP");
const sizeLabel = (size: number) => size < 1024 ? `${number(size)} B` : size < 1024 * 1024 ? `${number(Math.ceil(size / 1024))} KB` : `${(size / 1024 / 1024).toFixed(1)} MB`;

export function TargetFilePath({ file }: { file: string }) {
  const boundary = file.lastIndexOf("/") + 1;
  return <span className="target-file-path" title={file}>
    {boundary > 0 && <span className="target-file-directory">{file.slice(0, boundary)}</span>}
    <strong>{file.slice(boundary)}</strong>
  </span>;
}

export function TargetFileSource({ content }: { content: string }) {
  const lines = useMemo(() => {
    const result = content.split(/\r\n|\r|\n/);
    // A final newline terminates the preceding line rather than adding a line.
    if (result.length > 1 && result.at(-1) === "") result.pop();
    return result;
  }, [content]);
  const visible = lines.slice(0, MAX_LINES);
  if (!content) return <div className="target-browser-message">空のファイルです</div>;
  return <>
    {lines.length > MAX_LINES && <div className="target-browser-note">先頭{number(MAX_LINES)}行を表示しています。</div>}
    <div className="target-source-scroll" tabIndex={0} aria-label="ファイルの内容">
      <div className="target-source-lines">
        <pre className="target-source-gutter" aria-hidden="true">{visible.map((_, i) => i + 1).join("\n")}</pre>
        <pre className="target-source-code"><code>{visible.join("\n")}</code></pre>
      </div>
    </div>
  </>;
}

export function TargetFolderBrowser({ workspaceId, root }: { workspaceId: string; root: string }) {
  const [listing, setListing] = useState<TargetFileList | null>(null);
  const [listError, setListError] = useState("");
  const [revision, setRevision] = useState(0);
  const [selectedFile, setSelectedFile] = useState("");
  const [search, setSearch] = useState("");
  const [detail, setDetail] = useState<TargetFileContent | null>(null);
  const [detailError, setDetailError] = useState("");
  const [detailRevision, setDetailRevision] = useState(0);
  const [viewport, setViewport] = useState({ top: 0, height: 400 });
  const [listElement, setListElement] = useState<HTMLDivElement | null>(null);
  const detailRequest = useRef(0);
  const listId = useId();
  const files = listing?.files || [];
  const splitter = useResultsSplitter(`${workspaceId}:${root}`, files.map(item => item.file), { extraWidth: 50, sampleSelector: ".target-browser-file" });
  const filtered = useMemo(() => {
    const query = search.trim().toLocaleLowerCase();
    return files.filter(item => item.file.toLocaleLowerCase().includes(query));
  }, [listing, search]);
  const loading = !listing && !listError;
  const start = Math.max(0, Math.min(Math.floor(viewport.top / ROW_HEIGHT) - 8, filtered.length - 1));
  const end = Math.min(filtered.length, start + Math.ceil(viewport.height / ROW_HEIGHT) + 16);
  const selectedIndex = filtered.findIndex(item => item.file === selectedFile);
  const detailLoading = Boolean(selectedFile && listing && !detail && !detailError);

  useEffect(() => {
    let canceled = false;
    detailRequest.current++;
    setListing(null); setListError(""); setDetail(null); setDetailError("");
    api.ListTargetFiles(workspaceId, root).then(result => {
      if (canceled) return;
      if (result.workspaceId !== workspaceId || result.root !== root) throw new Error("対象フォルダが変更されています。再読み込みしてください。");
      result = { ...result, files: result.files || [] };
      setListing(result);
      setSelectedFile(previous => result.files.some(item => item.file === previous) ? previous : result.files[0]?.file || "");
    }).catch(error => { if (!canceled) setListError(errorMessage(error)); });
    return () => { canceled = true; detailRequest.current++; };
  }, [workspaceId, root, revision]);

  useEffect(() => {
    const request = ++detailRequest.current;
    setDetail(null); setDetailError("");
    if (!selectedFile || !listing) return;
    api.ReadTargetFile(workspaceId, root, selectedFile).then(result => {
      if (detailRequest.current !== request) return;
      if (result.workspaceId !== workspaceId || result.root !== root || result.file !== selectedFile)
        throw new Error("表示対象が変更されています。再読み込みしてください。");
      setDetail(result);
    }).catch(error => { if (detailRequest.current === request) setDetailError(errorMessage(error)); });
    return () => { detailRequest.current++; };
  }, [workspaceId, root, selectedFile, listing, detailRevision]);

  useEffect(() => {
    if (!listElement) return;
    const observer = new ResizeObserver(() => setViewport(previous => ({ ...previous, height: listElement.clientHeight })));
    observer.observe(listElement);
    return () => observer.disconnect();
  }, [listElement]);

  useEffect(() => {
    if (listElement) listElement.scrollTop = 0;
    setViewport(previous => ({ ...previous, top: 0 }));
  }, [search, listing, listElement]);

  function selectFile(file: string) {
    // Clear synchronously so a previously selected file is never shown under a new name.
    detailRequest.current++;
    setDetail(null); setDetailError(""); setSelectedFile(file);
    if (file === selectedFile) setDetailRevision(value => value + 1);
  }
  function navigateList(event: KeyboardEvent<HTMLDivElement>) {
    let index: number;
    if (event.key === "ArrowDown") index = Math.min(filtered.length - 1, selectedIndex + 1);
    else if (event.key === "ArrowUp") index = Math.max(0, selectedIndex - 1);
    else if (event.key === "Home") index = 0;
    else if (event.key === "End") index = filtered.length - 1;
    else return;
    if (!filtered[index]) return;
    event.preventDefault();
    selectFile(filtered[index].file);
    if (!listElement) return;
    const top = index * ROW_HEIGHT;
    if (top < listElement.scrollTop) listElement.scrollTop = top;
    else if (top + ROW_HEIGHT > listElement.scrollTop + listElement.clientHeight)
      listElement.scrollTop = top + ROW_HEIGHT - listElement.clientHeight;
  }

  return <div ref={splitter.ref} className="target-browser" role="region" aria-label="対象フォルダのファイル"
    style={splitter.ready ? { gridTemplateColumns: `${splitter.width}px 9px minmax(0, 1fr)` } : undefined}>
    <div className="target-browser-list">
      <div className="target-browser-list-header">
        <span>{listing ? `${number(filtered.length)}${search.trim() ? ` / ${number(files.length)}` : ""} ファイル` : "ファイル一覧"}</span>
        <button className="icon-button" aria-label="ファイル一覧を更新" title="更新" disabled={loading} onClick={() => setRevision(value => value + 1)}><Icon name="refresh" size={15} /></button>
      </div>
      <div className="target-browser-search"><Icon name="search" size={14} /><input value={search} onChange={event => setSearch(event.target.value)} aria-label="ファイルを検索" placeholder="パス・ファイル名で検索" /></div>
      {listing?.truncated && <div className="target-browser-note">先頭{number(listing.limit)}件を表示しています。</div>}
      {loading ? <div className="target-browser-message" role="status"><span className="spinner" />読み込み中…</div>
        : listError ? <div className="target-browser-message is-error" role="alert"><Icon name="warning" size={18} /><span>{listError}</span><button className="text-button" onClick={() => setRevision(value => value + 1)}>再読み込み</button></div>
        : filtered.length === 0 ? <div className="target-browser-message">{search ? "一致するファイルがありません" : "ファイルがありません"}</div>
        : <div className="target-browser-file-scroll" ref={setListElement} role="listbox" aria-label="ファイル一覧" tabIndex={0}
          aria-activedescendant={selectedIndex >= start && selectedIndex < end ? `${listId}-${selectedIndex}` : undefined}
          onKeyDown={navigateList} onScroll={event => {
            const top = event.currentTarget.scrollTop;
            setViewport(previous => ({ ...previous, top }));
          }}>
          <div style={{ height: filtered.length * ROW_HEIGHT, position: "relative" }}>
            <div style={{ position: "absolute", top: start * ROW_HEIGHT, left: 0, right: 0 }}>
              {filtered.slice(start, end).map((item, offset) => <div key={item.file} id={`${listId}-${start + offset}`} role="option"
                aria-selected={selectedFile === item.file} aria-label={item.file} aria-setsize={filtered.length} aria-posinset={start + offset + 1}
                className={`target-browser-file${selectedFile === item.file ? " is-selected" : ""}`}
                onClick={() => { listElement?.focus({ preventScroll: true }); selectFile(item.file); }}>
                <Icon name="file" size={14} /><TargetFilePath file={item.file} />
              </div>)}
            </div>
          </div>
        </div>}
    </div>
    <div className={`target-browser-splitter${splitter.resizing ? " is-resizing" : ""}`} role="separator" tabIndex={0}
      aria-label="ファイル一覧の幅" aria-orientation="vertical" aria-valuemin={splitter.bounds.min} aria-valuemax={splitter.bounds.max} aria-valuenow={splitter.width}
      title="ドラッグまたは左右キーで幅を変更。ダブルクリックで自動調整" {...splitter.handleProps}><span aria-hidden="true" /></div>
    <div className="target-browser-detail">
      <div className="target-browser-detail-header">
        <Icon name="file" size={15} />
        {selectedFile && listing ? <TargetFilePath file={selectedFile} /> : <span>ファイルの内容</span>}
        {detail && <span className="target-browser-size">{sizeLabel(detail.size)}</span>}
      </div>
      {detailLoading ? <div className="target-browser-message" role="status"><span className="spinner" />読み込み中…</div>
        : detailError ? <div className="target-browser-message is-error" role="alert"><Icon name="warning" size={18} /><span>{detailError}</span><button className="text-button" onClick={() => setDetailRevision(value => value + 1)}>再読み込み</button></div>
        : detail?.unavailableReason ? <div className="target-browser-message"><Icon name="file" size={24} /><span>{detail.unavailableReason}</span></div>
        : detail ? <TargetFileSource key={`${selectedFile}:${detailRevision}:${revision}`} content={detail.content} />
        : <div className="target-browser-message"><Icon name="file" size={26} /><span>ファイルを選択してください</span></div>}
    </div>
  </div>;
}
