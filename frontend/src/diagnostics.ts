import type { SourceDiagnostic, Task } from "./types";

/** A result belongs to its attempt, even after the imported file was replaced or cleared. */
export function resultSourceDiagnostics(task: Task | undefined, attemptIndex = -1): SourceDiagnostic[] {
  if (!task) return [];
  if (attemptIndex >= 0) return task.history[attemptIndex]?.sourceDiagnostics || [];
  return task.history.length ? task.history.at(-1)?.sourceDiagnostics || [] : task.sourceDiagnostics || [];
}
