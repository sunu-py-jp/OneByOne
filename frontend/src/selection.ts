import type { Config, Rule } from "./types";

// Connection and usage changes do not change the file mapping. Source, rules,
// filters and the destination queue do; a failed scan never establishes a key.
export function selectionContextKey(workspaceId: string, config: Config, rules: Rule[]): string {
  return JSON.stringify([
    workspaceId, config.root, config.queuePath, config.rulesPath, config.legacyPath,
    config.includeGlobs, config.excludeGlobs, config.maxFileBytes,
    rules.map(({ id, title, overview, before, after, notes, holdConditions, pattern }) =>
      [id, title, overview, before, after, notes, holdConditions, pattern]).sort((a, b) => a[0].localeCompare(b[0])),
  ]);
}
