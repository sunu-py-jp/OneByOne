import { HelpTip } from "./HelpTip";
import { HoverTip } from "./HoverTip";
import { Icon } from "./icons";
import type { DiagnosticsSummary } from "./types";
import "./diagnostics.css";

export function DiagnosticsSettings({ diagnostics, path, only, disabled, disabledReason, error, onImport, onClear, onOnlyChange }: {
  diagnostics?: DiagnosticsSummary;
  path?: string;
  only: boolean;
  disabled: boolean;
  disabledReason?: string;
  error?: string;
  onImport: () => void;
  onClear: () => void;
  onOnlyChange: (only: boolean) => void;
}) {
  const imported = Boolean(path || diagnostics);
  const hasDiagnostics = Boolean(diagnostics?.count);
  return <details className="diagnostics-settings" data-issue-section="diagnostics" tabIndex={-1}>
    <summary><Icon name="warning" size={14} /><strong>診断JSONL</strong><span>{diagnostics ? `${diagnostics.count.toLocaleString()} 件 · ${diagnostics.fileCount.toLocaleString()} ファイル` : imported ? "読込内容を確認してください" : "未取込"}</span><Icon name="down" size={13} /></summary>
    <div className="diagnostics-settings-body">
      {error && <p className="diagnostics-error" role="alert">{error}</p>}
      <div className="diagnostics-import-row">
        <span className="diagnostics-file-name" title={path}>{diagnostics?.name || (path ? path.split(/[\\/]/).at(-1) : "JSONLファイルを取り込むと、診断を対象の絞り込みとAIへの情報に使えます。")}</span>
        <div className="diagnostics-import-actions">
          <HoverTip reason={disabled ? disabledReason : ""}><button type="button" className="text-button" disabled={disabled} onClick={onImport}><Icon name="upload" size={13} />{imported ? "差し替え" : "取り込む"}</button></HoverTip>
          {imported && <HoverTip reason={disabled ? disabledReason : ""}><button type="button" className="text-button" disabled={disabled} onClick={onClear}>解除</button></HoverTip>}
          <HelpTip label="診断JSONLの形式"><p>UTF-8のJSONL形式で、1行に1件のJSONを記載します。file（対象フォルダからの相対パス）と message（診断内容）は必須、line（1始まりの行番号）と code は任意です。</p><pre>{'{"file":"src/app.ts","message":"名前が見つかりません","line":12,"code":"TS2304"}'}</pre><p>各ルールの「診断の条件」で、取り込んだ診断に合うルールを絞り込めます。</p></HelpTip>
        </div>
      </div>
      <HoverTip reason={disabled ? disabledReason : !hasDiagnostics ? "診断を取り込むと選択できます。" : ""}>
        <label className="diagnostics-only"><input type="checkbox" checked={only} disabled={disabled || !hasDiagnostics} onChange={event => onOnlyChange(event.currentTarget.checked)} />診断のあるファイルだけを対象にする</label>
      </HoverTip>
    </div>
  </details>;
}
