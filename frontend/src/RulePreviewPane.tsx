import { RuleMarkdown } from "./RuleMarkdown";
import { RuleScopeIcon } from "./RuleScopeIcon";
import { Icon } from "./icons";
import type { Rule } from "./types";
import "./rule-preview.css";

/** Read-only rule context used beside source and result files. */
export function RulePreviewPane({ rule, loading = false, error = "", caption, onClose }: {
  rule: Rule | null;
  loading?: boolean;
  error?: string;
  caption?: string;
  onClose: () => void;
}) {
  return <aside className="rule-preview-pane" aria-label="ルールの詳細">
    <header className="rule-preview-header">
      {rule ? <>
        <RuleScopeIcon rule={rule} size={15} />
        <span className="rule-preview-id" title={rule.id}>{rule.id}</span>
        <strong title={rule.title}>{rule.title}</strong>
      </> : <strong>ルールの詳細</strong>}
      <button className="icon-button" aria-label="ルールの詳細を閉じる" title="閉じる" onClick={onClose}><Icon name="close" size={16} /></button>
    </header>
    {caption && <p className="rule-preview-caption">{caption}</p>}
    {loading ? <div className="rule-preview-message" role="status"><span className="spinner" />読み込み中…</div>
      : error ? <div className="rule-preview-message is-error" role="alert"><Icon name="warning" size={20} />{error}</div>
      : rule ? <div className="rule-preview-body" tabIndex={0}>
        <p className="rule-preview-description">{rule.summary}</p>
        <RuleMarkdown body={rule.body} />
        <section className="rule-preview-section rule-preview-patterns"><h3>対象ファイル</h3><pre><code>{rule.pathPattern || "全ファイル"}</code></pre></section>
        {rule.contentPattern && <section className="rule-preview-section"><h3>内容の条件</h3><pre><code>{rule.contentPattern}</code></pre></section>}
      </div> : <div className="rule-preview-message">ルールを選択してください</div>}
  </aside>;
}
