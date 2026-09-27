/** How a rule selects files: by path, by content, both, or every file. */
export type RuleScope = "common" | "path" | "content" | "both";

export function ruleScope(rule: { pathPattern?: string; contentPattern?: string }): RuleScope {
  const path = Boolean(rule.pathPattern?.trim()), content = Boolean(rule.contentPattern?.trim());
  return path && content ? "both" : path ? "path" : content ? "content" : "common";
}

export const ruleScopeLabels: Record<RuleScope, string> = { common: "共通", path: "パス", content: "内容", both: "パス＋内容" };
