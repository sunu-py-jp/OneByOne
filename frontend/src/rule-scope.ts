/** Nonempty conditions are combined to select files; no conditions means common. */
export type RuleScope = "common" | "path" | "content" | "both" | "diagnostic" | "path-diagnostic" | "content-diagnostic" | "both-diagnostic";

export function ruleScope(rule: { pathPattern?: string; contentPattern?: string; diagnosticPattern?: string }): RuleScope {
  const path = Boolean(rule.pathPattern?.trim()), content = Boolean(rule.contentPattern?.trim());
  if (rule.diagnosticPattern?.trim()) return path && content ? "both-diagnostic" : path ? "path-diagnostic" : content ? "content-diagnostic" : "diagnostic";
  return path && content ? "both" : path ? "path" : content ? "content" : "common";
}

export const ruleScopeLabels: Record<RuleScope, string> = {
  common: "共通", path: "パス", content: "内容", both: "パス＋内容",
  diagnostic: "診断", "path-diagnostic": "パス＋診断", "content-diagnostic": "内容＋診断", "both-diagnostic": "パス＋内容＋診断",
};
