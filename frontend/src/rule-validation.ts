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

export function solutionRuleIdError(id: string, takenIds?: ReadonlySet<string>): string {
  id = id.trim();
  if (!id) return "IDを入力してください。";
  if (id.length > 64 || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(id)) return "IDは64文字以内の英小文字・数字・ハイフンで入力してください。先頭・末尾・連続するハイフンは使えません。";
  if (/^(con|prn|aux|nul|com[1-9]|lpt[1-9])$/.test(id)) return "Windowsの予約名はIDに使えません。";
  return takenIds?.has(id) ? "同じIDのルールが既にあります。" : "";
}

export function solutionRuleDraftErrors(rule: Pick<RuleEdit, "id" | "description" | "body">, takenIds?: ReadonlySet<string>): string[] {
  const errors = [solutionRuleIdError(rule.id, takenIds)].filter(Boolean);
  if (!rule.description.trim()) errors.push("説明を入力してください。");
  else if ([...rule.description].length > 1024) errors.push("説明は1024文字以内で入力してください。");
  if (!rule.body.trim()) errors.push("内容を入力してください。");
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

export function isCommonRule(rule: { pathPattern: string; contentPattern: string; diagnosticPattern?: string }): boolean {
  return !rule.pathPattern.trim() && !rule.contentPattern.trim() && !rule.diagnosticPattern?.trim();
}
