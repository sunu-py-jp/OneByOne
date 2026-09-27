import type { RuleEdit } from "./types";

const ruleIdPattern = /^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$/;

/** `takenIds` (lowercase) is checked only for a new rule; saved IDs are fixed. */
export function ruleIdError(id: string, takenIds?: ReadonlySet<string>): string {
  id = id.trim();
  if (!id) return "IDを入力してください。";
  if (!ruleIdPattern.test(id)) return "IDは英数字で始まる64文字以内の英数字・ハイフン・アンダースコアで入力してください。";
  return takenIds?.has(id.toLowerCase()) ? "同じIDのルールが既にあります。" : "";
}

export function ruleDraftErrors(rule: Pick<RuleEdit, "id" | "name" | "description">, takenIds?: ReadonlySet<string>): string[] {
  const errors = [ruleIdError(rule.id, takenIds)].filter(Boolean);
  if (!rule.name.trim()) errors.push("名称を入力してください。");
  if (!rule.description.trim()) errors.push("説明を入力してください。");
  return errors;
}

/** Continues the numeric sequence; non-numeric IDs are skipped but never reused. */
export function nextRuleId(ids: Iterable<string>): string {
  const taken = new Set<string>();
  let next = 1;
  for (const id of ids) {
    taken.add(id.toLowerCase());
    if (/^[0-9]+$/.test(id)) next = Math.max(next, Number(id) + 1);
  }
  while (taken.has(String(next))) ++next;
  return String(next);
}

export function isCommonRule(rule: { pathPattern: string; contentPattern: string }): boolean {
  return !rule.pathPattern.trim() && !rule.contentPattern.trim();
}
