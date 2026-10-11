import { Icon } from "./icons";
import type { Rule, SourceDiagnostic } from "./types";
import "./diagnostics.css";

export function SourceDiagnostics({ diagnostics = [], rules = [], onOpenRule, caption }: {
  diagnostics?: SourceDiagnostic[];
  rules?: Rule[];
  onOpenRule?: (id: string) => void;
  caption?: string;
}) {
  if (!diagnostics.length) return null;
  const titles = new Map(rules.map(rule => [rule.id, rule.title]));
  return <details className="source-diagnostics">
    <summary><Icon name="warning" size={13} /><strong>取り込んだ診断</strong><span>{diagnostics.length.toLocaleString()} 件</span>{caption && <small>{caption}</small>}<Icon name="down" size={12} /></summary>
    <ul aria-label="取り込んだ診断の一覧">{diagnostics.map((diagnostic, index) => <li key={index}>
      <div className="source-diagnostic-location"><code>{diagnostic.file}{diagnostic.line ? `:${diagnostic.line}` : ""}</code>{diagnostic.code && <b>{diagnostic.code}</b>}</div>
      <p>{diagnostic.message}</p>
      <div className="source-diagnostic-rules">{diagnostic.ruleIds?.length ? diagnostic.ruleIds.map(id => onOpenRule
        ? <button key={id} type="button" className="rule-tag" title={`${id} · ${titles.get(id) || "ルールの詳細"}`} aria-label={`${id} ${titles.get(id) || ""}の詳細`} onClick={() => onOpenRule(id)}>{id}</button>
        : <span key={id} className="rule-tag">{id}</span>) : <span className="source-diagnostic-unmatched">診断条件に一致するルールなし</span>}</div>
    </li>)}</ul>
  </details>;
}
