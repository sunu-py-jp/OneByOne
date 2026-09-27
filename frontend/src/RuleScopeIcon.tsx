import { useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import { Icon, type IconName } from "./icons";
import { ruleScope, ruleScopeLabels, type RuleScope } from "./rule-scope";
import "./rule-scope.css";

const icons: Record<RuleScope, IconName> = { common: "asterisk", path: "folder", content: "file", both: "cube" };

/** Shows the pattern state at a glance; its label appears as a chip on hover. */
export function RuleScopeIcon({ rule, size = 14 }: { rule: { pathPattern?: string; contentPattern?: string }; size?: number }) {
  const scope = ruleScope(rule);
  const label = ruleScopeLabels[scope];
  const anchor = useRef<HTMLSpanElement>(null);
  const [chip, setChip] = useState<{ left: number; top: number; below: boolean } | null>(null);
  useEffect(() => {
    if (!chip) return;
    const hide = () => setChip(null);
    window.addEventListener("scroll", hide, true);
    return () => window.removeEventListener("scroll", hide, true);
  }, [chip]);
  const show = () => {
    const rect = anchor.current?.getBoundingClientRect();
    if (!rect) return;
    const below = rect.top < 34;
    setChip({ left: rect.left + rect.width / 2, top: below ? rect.bottom : rect.top, below });
  };
  // An empty title keeps a surrounding button's tooltip from covering the chip.
  return <>
    <span ref={anchor} className={`rule-scope-icon scope-${scope}`} role="img" aria-label={`${label}のルール`} title=""
      onPointerEnter={show} onPointerLeave={() => setChip(null)}>
      <Icon name={icons[scope]} size={size} />
    </span>
    {chip && createPortal(<span className={`rule-scope-chip scope-${scope}`} role="tooltip" data-below={chip.below}
      style={{ left: chip.left, top: chip.top }}>{label}</span>, document.body)}
  </>;
}
