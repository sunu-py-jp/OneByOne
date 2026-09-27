import type { ChangeLineRange, ChangeReportItem } from "./types";

export interface LineRuleAnnotation {
  ruleId: string;
  ruleTitle: string;
  changes: ChangeReportItem[];
  origin?: ChangeReportItem["origin"];
}

export function originLabel(origin?: ChangeReportItem["origin"]): string {
  return origin === "latest" ? "選択した実行で修正" : origin === "previous" ? "過去の実行で修正済み" : origin === "mixed" ? "選択した実行と以前の実行で修正" : "";
}

function mergeOrigin(a?: ChangeReportItem["origin"], b?: ChangeReportItem["origin"]): ChangeReportItem["origin"] {
  return !a ? b : !b || a === b ? a : "mixed";
}

export function ruleOrigins(changes: ChangeReportItem[]): Map<string, ChangeReportItem["origin"]> {
  const origins = new Map<string, ChangeReportItem["origin"]>();
  for (const change of recordedChanges(changes)) {
    if (change.status === "fixed" && change.origin) origins.set(change.ruleId, mergeOrigin(origins.get(change.ruleId), change.origin));
  }
  return origins;
}

export function recordedChanges(changes?: ChangeReportItem[] | null): ChangeReportItem[] {
  return (changes || []).filter(item => item.status === "needs_human" || (typeof item.change === "string" && item.change.trim().length > 0));
}

export function changeNote(change: ChangeReportItem): string {
  return change.status === "needs_human"
    ? change.reason?.trim() || change.change?.trim() || "確認事項の詳細は記録されていません。"
    : change.change;
}

export function heldSourceTarget(change: ChangeReportItem, preferred: "before" | "after", lineCounts: { before: number; after: number }): { side: "before" | "after"; line: number } | null {
  if (change.status !== "needs_human" || change.attributionVersion !== 2) return null;
  for (const side of [preferred, preferred === "after" ? "before" : "after"] as const) {
    for (const range of change.lineRanges || []) {
      const span = validRange(range, side);
      if (span && span[1] <= lineCounts[side]) return { side, line: span[0] };
    }
  }
  return null;
}

function validRange(range: ChangeLineRange, side: "before" | "after"): [number, number] | null {
  const start = side === "before" ? range.beforeStart : range.afterStart;
  const end = side === "before" ? range.beforeEnd : range.afterEnd;
  return Number.isSafeInteger(start) && Number.isSafeInteger(end) && start > 0 && end >= start ? [start, end] : null;
}

// The runner persists these exact-edit coordinates. Never interpret a prose
// location, an old line hint, or a rule-level claim as a verified source span.
export function rulesForLine(changes: ChangeReportItem[], side: "before" | "after", line: number | null): LineRuleAnnotation[] {
  if (!line || !Number.isSafeInteger(line)) return [];
  const seen = new Map<string, LineRuleAnnotation>();
  const matches: LineRuleAnnotation[] = [];
  for (const change of recordedChanges(changes)) {
    if ((!change.ruleId && change.status !== "needs_human") || change.attributionVersion !== 2) continue;
    const matched = (change.lineRanges || []).some(range => {
      const span = validRange(range, side);
      return span !== null && line >= span[0] && line <= span[1];
    });
    if (matched) {
      const existing = seen.get(change.ruleId);
      const origin = mergeOrigin(existing?.origin, change.origin);
      if (existing) {
        if (origin) existing.origin = origin;
        if (!existing.changes.includes(change)) existing.changes.push(change);
      } else {
        const annotation = { ruleId: change.ruleId, ruleTitle: change.ruleTitle, changes: [change], ...(origin ? { origin } : {}) };
        seen.set(change.ruleId, annotation);
        matches.push(annotation);
      }
    }
  }
  return matches;
}

// Removed and added rows form one change block. Show each item's explanation
// at its own first coordinate, rather than repeating it for both sides or
// pulling another item of the same rule up to an unrelated line.
// Context and hunk boundaries start a new block; newline markers do not.
export function diffRuleAnnotations(changes: ChangeReportItem[], lines: readonly {
  type: string;
  text: string;
  oldLine: number | null;
  newLine: number | null;
}[]): LineRuleAnnotation[][] {
  const annotations: LineRuleAnnotation[][] = lines.map(() => []);
  let block: number[] = [];
  function flushBlock() {
    const matches = new Map(block.map(index => {
      const line = lines[index];
      return [index, rulesForLine(changes, line.type === "removed" ? "before" : "after", line.type === "removed" ? line.oldLine : line.newLine)] as const;
    }));
    const before = new Map<ChangeReportItem, number>();
    for (const index of block) {
      if (lines[index].type !== "removed") continue;
      for (const rule of matches.get(index) || []) for (const change of rule.changes) {
        if (!before.has(change)) before.set(change, index);
      }
    }
    const shown = new Set<ChangeReportItem>();
    function add(index: number, change: ChangeReportItem) {
      shown.add(change);
      const existing = annotations[index].find(rule => rule.ruleId === change.ruleId);
      if (existing) { existing.changes.push(change); existing.origin = mergeOrigin(existing.origin, change.origin); }
      else annotations[index].push({ ruleId: change.ruleId, ruleTitle: change.ruleTitle, changes: [change], origin: change.origin });
    }
    // Explain a replacement before its removed/added pair. Items which share
    // the same changed output row share one header; unrelated fixes stay local.
    for (const index of block) {
      if (lines[index].type !== "added") continue;
      const local = (matches.get(index) || []).flatMap(rule => rule.changes).filter(change => !shown.has(change));
      const anchor = local.reduce((first, change) => Math.min(first, before.get(change) ?? index), index);
      for (const change of local) add(anchor, change);
    }
    // Pure deletions have no output row.
    for (const index of block) {
      if (lines[index].type !== "removed") continue;
      for (const rule of matches.get(index) || []) for (const change of rule.changes) {
        if (!shown.has(change)) add(index, change);
      }
    }
    block = [];
  }
  for (const [index, line] of lines.entries()) {
    if (line.type === "removed" || line.type === "added") block.push(index);
    else if (line.type !== "meta" || !line.text.startsWith("\\ No newline at end of file")) flushBlock();
  }
  flushBlock();
  // A held construct may be unchanged and only appear as context in this diff.
  // Do not hide it just because no edit was adopted at that location. Keep one
  // note for each continuous visible context span, independently of fix badges.
  let previous = new Set<ChangeReportItem>();
  for (const [index, line] of lines.entries()) {
    if (line.type !== "context") { previous.clear(); continue; }
    const current = new Set([...rulesForLine(changes, "before", line.oldLine), ...rulesForLine(changes, "after", line.newLine)]
      .flatMap(rule => rule.changes).filter(change => change.status === "needs_human"));
    for (const change of current) {
      if (previous.has(change)) continue;
      const existing = annotations[index].find(rule => rule.ruleId === change.ruleId);
      if (existing) existing.changes.push(change);
      else annotations[index].push({ ruleId: change.ruleId, ruleTitle: change.ruleTitle, changes: [change], origin: change.origin });
    }
    previous = current;
  }
  return annotations;
}

export function changeLineLabel(ranges?: ChangeLineRange[]): string {
  const labels = (ranges || []).map(range => {
    const describe = (side: "before" | "after") => {
      const span = validRange(range, side);
      return span ? `${side === "before" ? "前" : "後"} ${span[0] === span[1] ? span[0] : `${span[0]}–${span[1]}`}` : "";
    };
    return [describe("before"), describe("after")].filter(Boolean).join(" → ");
  }).filter(Boolean);
  return [...new Set(labels)].join("、");
}
