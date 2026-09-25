import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";
import { HoverTip } from "./HoverTip";
import "./rule-package-actions.css";

export function RulePackageActions({ disabled, exportDisabled, disabledReason, onImport, onExport }: {
  disabled: boolean; exportDisabled: boolean; disabledReason?: string; onImport: () => void; onExport: () => void;
}) {
  const [open, setOpen] = useState(false);
  const [position, setPosition] = useState({ top: 0, left: 0 });
  const trigger = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const id = useId();
  const close = () => { setOpen(false); trigger.current?.focus(); };
  useLayoutEffect(() => {
    if (!open || !trigger.current) return;
    const rect = trigger.current.getBoundingClientRect();
    setPosition({ top: Math.min(rect.bottom + 6, window.innerHeight - 98), left: Math.max(10, Math.min(rect.right - 178, window.innerWidth - 188)) });
    menu.current?.querySelector<HTMLButtonElement>("button:not(:disabled)")?.focus();
  }, [open]);
  useEffect(() => {
    if (!open) return;
    const outside = (event: PointerEvent) => {
      if (!trigger.current?.contains(event.target as Node) && !menu.current?.contains(event.target as Node)) setOpen(false);
    };
    const reposition = () => setOpen(false);
    document.addEventListener("pointerdown", outside);
    window.addEventListener("resize", reposition);
    window.addEventListener("scroll", reposition, true);
    return () => { document.removeEventListener("pointerdown", outside); window.removeEventListener("resize", reposition); window.removeEventListener("scroll", reposition, true); };
  }, [open]);
  useEffect(() => { if (disabled) setOpen(false); }, [disabled]);
  return <>
    <HoverTip reason={disabled ? disabledReason || "現在はルールを変更できません。" : ""}><button ref={trigger} className="icon-button" type="button" disabled={disabled}
      aria-label="ルールパッケージの操作" aria-haspopup="menu" aria-expanded={open} aria-controls={open ? id : undefined}
      onClick={() => setOpen(value => !value)}><Icon name="more" size={17} /></button></HoverTip>
    {open && createPortal(<div ref={menu} id={id} role="menu" aria-label="ルールパッケージ" className="rule-package-menu" style={position}
      onKeyDown={event => {
        if (event.key === "Escape" || event.key === "Tab") { if (event.key === "Escape") event.preventDefault(); close(); }
        else if (["ArrowDown", "ArrowUp", "Home", "End"].includes(event.key)) {
          event.preventDefault();
          const choices = Array.from(menu.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)") || []);
          const current = choices.indexOf(document.activeElement as HTMLButtonElement);
          const next = event.key === "Home" ? 0 : event.key === "End" ? choices.length - 1 : (current + (event.key === "ArrowDown" ? 1 : -1) + choices.length) % choices.length;
          choices[next]?.focus();
        }
      }}>
      <button type="button" role="menuitem" onClick={() => { close(); onImport(); }}><Icon name="folder" size={16} />インポート</button>
      <HoverTip reason={exportDisabled ? "書き出すルールを追加してください。" : ""}><button type="button" role="menuitem" disabled={exportDisabled} onClick={() => { close(); onExport(); }}><Icon name="download" size={16} />書き出す</button></HoverTip>
    </div>, document.body)}
  </>;
}

export function RulePackageImportDialog({ path, busy, error, onCancel, onConfirm }: {
  path: string; busy: boolean; error?: string; onCancel: () => void; onConfirm: (mode: "replace" | "merge") => void;
}) {
  const [mode, setMode] = useState<"replace" | "merge">("merge");
  const form = useRef<HTMLFormElement>(null);
  const heading = useId();
  useEffect(() => {
    const opener = document.activeElement as HTMLElement | null;
    form.current?.querySelector<HTMLButtonElement>("button")?.focus();
    return () => { if (opener?.isConnected) opener.focus(); };
  }, []);
  useEffect(() => {
    if (busy) form.current?.focus();
  }, [busy]);
  return <div className="modal-backdrop" onClick={event => { if (event.target === event.currentTarget && !busy) onCancel(); }}>
    <form ref={form} tabIndex={-1} className="workspace-dialog package-import-dialog" role="dialog" aria-modal="true" aria-labelledby={heading} aria-busy={busy}
      onSubmit={event => { event.preventDefault(); if (!busy) onConfirm(mode); }} onKeyDown={event => {
        if (event.key === "Escape") { event.preventDefault(); event.stopPropagation(); if (!busy) onCancel(); }
        if (event.key !== "Tab") return;
        const choices = Array.from(form.current?.querySelectorAll<HTMLElement>("button:not(:disabled), input:not(:disabled)") || []);
        const first = choices[0], last = choices.at(-1);
        if (!choices.length) { event.preventDefault(); form.current?.focus(); }
        else if (document.activeElement === form.current) { event.preventDefault(); (event.shiftKey ? last : first)?.focus(); }
        else if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last?.focus(); }
        else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first?.focus(); }
      }}>
      <div className="dialog-heading"><h2 id={heading}>ルールをインポート</h2><button type="button" className="icon-button" aria-label="閉じる" disabled={busy} onClick={onCancel}><Icon name="close" size={18} /></button></div>
      <p className="package-import-filename" title={path}>{path.split(/[\\/]/).at(-1)}</p>
      <label className={`package-import-choice${mode === "merge" ? " selected" : ""}`}>
        <input type="radio" name="package-mode" value="merge" checked={mode === "merge"} disabled={busy} onChange={() => setMode("merge")} />
        <span><strong>マージ</strong><span>現在のルールに追加します。同じIDは _2、_3… を付けて残します。絞り込み・検証コマンドは現在の設定を使います。</span></span>
      </label>
      <label className={`package-import-choice${mode === "replace" ? " selected" : ""}`}>
        <input type="radio" name="package-mode" value="replace" checked={mode === "replace"} disabled={busy} onChange={() => setMode("replace")} />
        <span><strong>上書き</strong><span>現在のルールと、絞り込み・検証コマンド・旧シンボル定義をパッケージの内容で置き換えます。</span></span>
      </label>
      {error && <div className="inline-error" role="alert">{error}</div>}
      <div className="dialog-actions"><button type="button" className="button" disabled={busy} onClick={onCancel}>キャンセル</button>
        <button type="submit" className="button primary" disabled={busy}>{busy ? <span className="spinner" /> : <Icon name="upload" size={16} />}{mode === "merge" ? "マージして取り込む" : "上書きして取り込む"}</button></div>
    </form>
  </div>;
}
