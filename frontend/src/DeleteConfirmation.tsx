import { useEffect, useId, useLayoutEffect, useRef } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";
import "./delete-confirmation.css";

export function DeleteConfirmation({
  kind,
  name,
  busy,
  error,
  onCancel,
  onConfirm,
}: {
  kind: "workspace" | "rule" | "changes";
  name: string;
  busy: boolean;
  error?: string;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  const id = useId();
  const dialog = useRef<HTMLDivElement>(null);
  const cancel = useRef<HTMLButtonElement>(null);
  const opener = useRef<HTMLElement | null>(null);
  const unmounted = useRef(false);
  const label = kind === "workspace" ? "ワークスペース" : "ルール";
  const discarding = kind === "changes";

  useLayoutEffect(() => {
    unmounted.current = false;
    if (!opener.current && document.activeElement instanceof HTMLElement) opener.current = document.activeElement;
    if (cancel.current && !cancel.current.disabled) cancel.current.focus();
    else dialog.current?.focus();
    return () => {
      unmounted.current = true;
      const previous = opener.current;
      // Wait for React to remove a deleted target before deciding where focus returns.
      queueMicrotask(() => {
        if (unmounted.current && previous?.isConnected) previous.focus();
      });
    };
  }, []);

  useLayoutEffect(() => {
    if (busy) dialog.current?.focus();
    else if (document.activeElement === dialog.current) cancel.current?.focus();
  }, [busy]);

  useEffect(() => {
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        event.preventDefault();
        event.stopPropagation();
        if (!busy) onCancel();
      } else if (event.key === "Tab") {
        const buttons = dialog.current?.querySelectorAll<HTMLButtonElement>("button:not(:disabled)");
        if (!buttons?.length) {
          event.preventDefault();
          dialog.current?.focus();
          return;
        }
        const first = buttons[0];
        const last = buttons[buttons.length - 1];
        const outside = !dialog.current?.contains(document.activeElement);
        if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.current || outside)) {
          event.preventDefault();
          last.focus();
        } else if (!event.shiftKey && (document.activeElement === last || document.activeElement === dialog.current || outside)) {
          event.preventDefault();
          first.focus();
        }
      }
    };
    const onFocusIn = (event: FocusEvent) => {
      if (unmounted.current) return;
      if (dialog.current?.contains(event.target as Node)) return;
      if (busy) dialog.current?.focus();
      else cancel.current?.focus();
    };
    document.addEventListener("keydown", onKeyDown, true);
    document.addEventListener("focusin", onFocusIn);
    return () => {
      document.removeEventListener("keydown", onKeyDown, true);
      document.removeEventListener("focusin", onFocusIn);
    };
  }, [busy, onCancel]);

  return createPortal(
    <div
      className="delete-confirmation-backdrop"
      onClick={(event) => {
        if (event.target === event.currentTarget && !busy) onCancel();
      }}
    >
      <div
        ref={dialog}
        className="delete-confirmation-dialog"
        role="alertdialog"
        aria-modal="true"
        aria-labelledby={`${id}-title`}
        aria-describedby={`${id}-target ${id}-description`}
        aria-busy={busy || undefined}
        tabIndex={-1}
      >
        <div className="delete-confirmation-heading">
          <span className="delete-confirmation-icon"><Icon name="trash" size={22} /></span>
          <h2 id={`${id}-title`}>{discarding ? "変更を破棄" : `${label}を削除`}</h2>
        </div>
        <div id={`${id}-target`} className="delete-confirmation-target">{name}</div>
        <p id={`${id}-description`} className="delete-confirmation-description">
          {discarding
            ? "このファイルの作業コピーを、最初の処理を始める前の内容に戻し、再試行に追加します。過去の試行・差分・料金の記録は残ります。"
            : kind === "workspace"
            ? "このワークスペースを一覧から削除します。処理対象フォルダとソースファイルは削除されません。"
            : "このルールをワークスペースから削除します。実行済みの結果と元のルールパッケージは保持されます。"}
        </p>
        {error && <p className="delete-confirmation-error" role="alert">{error}</p>}
        <div className="delete-confirmation-actions">
          <button ref={cancel} type="button" className="delete-confirmation-cancel" disabled={busy} onClick={onCancel}>キャンセル</button>
          <button type="button" className="delete-confirmation-submit" disabled={busy} onClick={onConfirm}>
            <Icon name="trash" size={15} />{discarding ? busy ? "破棄中…" : "変更を破棄" : busy ? "削除中…" : "削除"}
          </button>
        </div>
      </div>
    </div>,
    document.body,
  );
}
