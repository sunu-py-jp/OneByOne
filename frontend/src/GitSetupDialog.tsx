import { useId, useLayoutEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon } from "./icons";
import type { GitInstallation } from "./types";
import "./git-setup-dialog.css";

export function GitSetupDialog({
  installation,
  checking,
  error,
  onRetry,
  onDownload,
  onDismiss,
}: {
  installation: GitInstallation;
  checking: boolean;
  error: string;
  onRetry: () => void;
  onDownload: () => void;
  onDismiss: () => void;
}) {
  const id = useId();
  const dialog = useRef<HTMLDialogElement>(null);
  const retry = useRef<HTMLButtonElement>(null);
  const download = useRef<HTMLButtonElement>(null);
  const [guideOpened, setGuideOpened] = useState(false);
  const open = !installation.available && installation.status !== "ready";
  const bundledError = installation.status === "bundle_error";
  const retryIsPrimary = bundledError || guideOpened || installation.status === "unusable";

  useLayoutEffect(() => {
    if (!open || !dialog.current) return;
    const element = dialog.current;
    const opener = document.activeElement instanceof HTMLElement ? document.activeElement : null;
    element.showModal();
    if (checking) element.focus();
    else if (bundledError || installation.status === "unusable") retry.current?.focus();
    else download.current?.focus();
    return () => {
      element.close();
      if (opener?.isConnected) opener.focus();
    };
  }, [open, bundledError]);

  useLayoutEffect(() => {
    if (!open || !dialog.current) return;
    if (checking) dialog.current.focus();
    else if (document.activeElement === dialog.current) retry.current?.focus();
  }, [checking, open]);

  if (!open) return null;

  return createPortal(
    <dialog
      ref={dialog}
      className="git-setup-dialog"
      aria-labelledby={`${id}-title`}
      aria-describedby={`${id}-description`}
      tabIndex={-1}
      onCancel={(event) => {
        event.preventDefault();
        onDismiss();
      }}
    >
      <div className="git-setup-heading">
        <span className="git-setup-icon"><Icon name="branch" size={23} /></span>
        <h2 id={`${id}-title`}>{bundledError ? "アプリの修復が必要です" : "Gitの準備が必要です"}</h2>
      </div>
      <p id={`${id}-description`} className="git-setup-description">
        ソースの変更を管理し、修正ごとにコミットを保存するためにGitを使用します。
      </p>
      <div className="git-setup-status">
        <Icon name="warning" size={17} />
        <div>
          <p>{installation.message || (installation.status === "missing" ? "Gitが見つかりません。" : "Gitを実行できません。")}</p>
          {installation.path && <code>{installation.path}</code>}
        </div>
      </div>
      <p className="git-setup-instruction">
        {bundledError ? "OneByOneを終了し、配布ファイル一式を再展開するか再インストールしてください。Gitを別途インストールする必要はありません。" : <>{installation.status === "missing"
          ? "公式の案内に沿ってGitをインストールし、「再確認」を押してください。"
          : "Gitの設定や実行権限を確認してから、「再確認」を押してください。"}
        インストールやPATHの変更が反映されない場合は、OneByOneを再起動してください。</>}
      </p>
      {error && <p className="git-setup-error" role="alert">{error}</p>}
      <div className="git-setup-actions">
        <button type="button" className="git-setup-later" onClick={onDismiss}>後で</button>
        <button
          ref={retry}
          type="button"
          className={retryIsPrimary ? "git-setup-primary" : "git-setup-secondary"}
          disabled={checking}
          onClick={onRetry}
        >
          <Icon name="refresh" size={15} />{checking ? "確認中…" : "再確認"}
        </button>
        {!bundledError && <button
          ref={download}
          type="button"
          className={retryIsPrimary ? "git-setup-secondary" : "git-setup-primary"}
          disabled={checking}
          onClick={() => {
            setGuideOpened(true);
            onDownload();
          }}
        >
          <Icon name="link" size={15} />公式の案内を開く
        </button>}
      </div>
      <p className="git-setup-footnote">LLM接続の設定や保存済みの結果確認は、このまま利用できます。</p>
    </dialog>,
    document.body,
  );
}
