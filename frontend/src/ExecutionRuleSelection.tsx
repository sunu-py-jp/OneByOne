import { useEffect, useId, useRef, useState } from "react";
import { Icon } from "./icons";
import { HelpTip } from "./HelpTip";
import type { Rule } from "./types";

export function ExecutionRuleSelection({ rules, excludedIds, disabled, onChange }: {
  rules: Rule[];
  excludedIds: string[];
  disabled: boolean;
  onChange: (excludedIds: string[]) => void;
}) {
  const menu = useRef<HTMLDetailsElement>(null);
  const labelId = useId();
  const [search, setSearch] = useState("");
  const excluded = new Set(excludedIds);
  const selectedCount = rules.filter(rule => !excluded.has(rule.id)).length;
  const query = search.trim().toLocaleLowerCase();
  const visible = rules.filter(rule => `${rule.id} ${rule.title} ${rule.summary}`.toLocaleLowerCase().includes(query));

  useEffect(() => {
    const closeOutside = (event: PointerEvent) => {
      if (event.target instanceof Node && !menu.current?.contains(event.target)) menu.current?.removeAttribute("open");
    };
    const closeOnEscape = (event: KeyboardEvent) => {
      if (event.key !== "Escape" || !menu.current?.open) return;
      menu.current.removeAttribute("open");
      menu.current.querySelector("summary")?.focus();
    };
    document.addEventListener("pointerdown", closeOutside);
    document.addEventListener("keydown", closeOnEscape);
    return () => {
      document.removeEventListener("pointerdown", closeOutside);
      document.removeEventListener("keydown", closeOnEscape);
    };
  }, []);

  return <div className="field execution-rule-selection">
    <div className="field-heading">
      <span id={labelId} className="field-label">適用ルール</span>
      <HelpTip label="適用ルール">選択したルールの条件で対象ファイルを抽出します。選択を変更したら「対象を更新」を押してください。選択していないルールは修正・独立レビューに渡しません。</HelpTip>
    </div>
    <details ref={menu} className="execution-rule-menu">
      <summary aria-labelledby={labelId} aria-disabled={disabled} onClick={event => { if (disabled) event.preventDefault(); }}>
        <span>{selectedCount === rules.length && selectedCount ? "すべてのルール" : `${selectedCount} 件選択`}</span>
        <span className="execution-rule-count">{selectedCount} / {rules.length}</span><Icon name="chevron" size={14} />
      </summary>
      <div className="execution-rule-options">
        <div className="execution-rule-search"><Icon name="search" size={14} /><input aria-label="適用ルールを検索" placeholder="ID・名称で検索" value={search} onChange={event => setSearch(event.target.value)} /></div>
        <div className="execution-rule-actions">
          <button type="button" className="text-button" disabled={disabled || selectedCount === rules.length} onClick={() => onChange([])}>すべて選択</button>
          <button type="button" className="text-button" disabled={disabled || !selectedCount} onClick={() => onChange(rules.map(rule => rule.id))}>選択を解除</button>
        </div>
        <div className="execution-rule-list">
          {visible.map(rule => <label key={rule.id} title={rule.title}>
            <input type="checkbox" disabled={disabled} checked={!excluded.has(rule.id)} onChange={event => {
              const next = new Set(excludedIds);
              if (event.target.checked) next.delete(rule.id); else next.add(rule.id);
              onChange([...next]);
            }} />
            <span className="rule-id">{rule.id}</span><span className="execution-rule-title">{rule.title}</span>
          </label>)}
          {!visible.length && <p className="execution-rule-empty">該当するルールがありません</p>}
        </div>
      </div>
    </details>
  </div>;
}
