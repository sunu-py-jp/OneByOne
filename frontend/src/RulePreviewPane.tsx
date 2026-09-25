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
        <span className={`rule-preview-kind${rule.always ? " is-common" : ""}`} title={rule.always ? "共通ルール" : "個別ルール"}><Icon name={rule.always ? "cube" : "rules"} size={15} /></span>
        <span className="rule-preview-id">{rule.id}</span>
        <strong title={rule.title}>{rule.title}</strong>
      </> : <strong>ルールの詳細</strong>}
      <button className="icon-button" aria-label="ルールの詳細を閉じる" title="閉じる" onClick={onClose}><Icon name="close" size={16} /></button>
    </header>
    {caption && <p className="rule-preview-caption">{caption}</p>}
    {loading ? <div className="rule-preview-message" role="status"><span className="spinner" />読み込み中…</div>
      : error ? <div className="rule-preview-message is-error" role="alert"><Icon name="warning" size={20} />{error}</div>
      : rule ? <div className="rule-preview-body" tabIndex={0}>
        <RuleText label="変更概要" value={rule.overview || rule.summary} />
        {(rule.before || rule.after) && <div className="rule-preview-comparison">
          <RuleText label="変更前" value={rule.before} code />
          <Icon name="arrow" size={17} />
          <RuleText label="変更後" value={rule.after} code />
        </div>}
        <RuleText label="備考" value={rule.notes} />
        <RuleText label="修正を保留すべきケース" value={rule.holdConditions} />
        <section className="rule-preview-section"><h3>適用パターン</h3>{rule.always ? <p className="rule-preview-muted">すべてのファイルに適用</p> : <pre><code>{rule.pattern}</code></pre>}</section>
      </div> : <div className="rule-preview-message">ルールを選択してください</div>}
  </aside>;
}

function RuleText({ label, value, code = false }: { label: string; value: string; code?: boolean }) {
  if (!value) return null;
  return <section className="rule-preview-section"><h3>{label}</h3>{code ? <pre><code>{value}</code></pre> : <p>{value}</p>}</section>;
}
