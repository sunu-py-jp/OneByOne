import type { ChangeLineRange, ChangeReportItem } from "./types";

export interface LineRuleAnnotation {
  ruleId: string;
  ruleTitle: string;
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
  return (changes || []).filter(item => typeof item.change === "string" && item.change.trim().length > 0);
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
    if (!change.ruleId) continue;
    const matched = (change.lineRanges || []).some(range => {
      const span = validRange(range, side);
      return span !== null && line >= span[0] && line <= span[1];
    });
    if (matched) {
      const existing = seen.get(change.ruleId);
      const origin = mergeOrigin(existing?.origin, change.origin);
      if (existing) {
        if (origin) existing.origin = origin;
      } else {
        const annotation = { ruleId: change.ruleId, ruleTitle: change.ruleTitle, ...(origin ? { origin } : {}) };
        seen.set(change.ruleId, annotation);
        matches.push(annotation);
      }
    }
  }
  return matches;
}

// Removed and added rows form one change block. Show each rule at its first
// recorded coordinate in that block, rather than repeating it for both sides.
// Context and hunk boundaries start a new block; newline markers do not.
export function diffRuleAnnotations(changes: ChangeReportItem[], lines: readonly {
  type: string;
  text: string;
  oldLine: number | null;
  newLine: number | null;
}[]): LineRuleAnnotation[][] {
  const annotations: LineRuleAnnotation[][] = lines.map(() => []);
  const block = new Map<string, LineRuleAnnotation>();
  for (const [index, line] of lines.entries()) {
    if (line.type !== "removed" && line.type !== "added") {
      if (line.type !== "meta" || !line.text.startsWith("\\ No newline at end of file")) block.clear();
      continue;
    }
    const side = line.type === "removed" ? "before" : "after";
    for (const rule of rulesForLine(changes, side, side === "before" ? line.oldLine : line.newLine)) {
      const existing = block.get(rule.ruleId);
      if (existing) {
        const origin = mergeOrigin(existing.origin, rule.origin);
        if (origin) existing.origin = origin;
      } else {
        block.set(rule.ruleId, rule);
        annotations[index].push(rule);
      }
    }
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
