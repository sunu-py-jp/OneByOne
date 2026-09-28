import type { Attempt, State, Task } from "./types";

type TaskState = Pick<Task, "status" | "resumeRequested" | "canDiscardChanges">;

export function hasRuleCandidates(task: Pick<Task, "rules"> | undefined): boolean {
  return Boolean(task?.rules?.length);
}

export function ruleScopeBlockReason(task: Pick<Task, "rules"> | undefined): string {
  return hasRuleCandidates(task) ? "" : "現在のルールの対象外です。ルールの条件を変更して再抽出してください。";
}

/** Retry remains pending in the queue; the persisted flag distinguishes its presentation. */
export function taskDisplayStatus(task: TaskState): string {
  if (task.status === "pending" && task.resumeRequested) return "retry";
  // Saved queues may describe only the last attempt. Keep an accepted change
  // visible as complete when a later inspection found nothing more to edit.
  if (task.status === "skipped" && task.canDiscardChanges) return "done";
  return task.status;
}

const labels: Record<string, string> = {
  retry: "再試行", running: "処理中", pending: "未修正", failed: "失敗",
  needs_human: "要確認", skipped: "変更不要", done: "完了",
};

export function taskStatusLabel(task: TaskState, phase?: string): string {
  const status = taskDisplayStatus(task);
  if (status === "running") {
    switch (phase) {
      case "reviewing": return "独立レビュー中";
      case "checking": return "検証中";
      case "preparing": return "準備中";
      case "applying": return "反映待ち・保存中";
      case "finalizing": return "結果を保存中";
    }
  }
  if (status === "needs_human" && task.canDiscardChanges) return "修正済み・要確認あり";
  return labels[status] ?? status;
}

/** Per-file phases are authoritative; a global phase must never label every worker. */
export function activeFilePhases(state: Pick<State, "running" | "filePhases" | "currentFile" | "currentFiles" | "phase">): Readonly<Record<string, string>> | undefined {
  if (!state.running) return undefined;
  if (state.currentFile && !state.filePhases?.[state.currentFile] && (state.currentFiles?.length || 0) <= 1) {
    return { ...state.filePhases, [state.currentFile]: state.phase };
  }
  return state.filePhases;
}

export function isPartialAdoption(attempt?: Pick<Attempt, "outcome" | "partial" | "commit">): boolean {
  return Boolean(attempt?.outcome === "needs_human" && attempt.partial && attempt.commit);
}

export function attemptStatusLabel(attempt: Pick<Attempt, "outcome" | "partial" | "commit">): string {
  const historyLabels: Record<string, string> = { validated: "検証済み", interrupted: "中断" };
  return isPartialAdoption(attempt) ? "修正済み・要確認あり" : labels[attempt.outcome] || historyLabels[attempt.outcome] || attempt.outcome || "処理中";
}

export function isCompletedTask(task: Pick<Task, "status">): boolean {
  return task.status === "done" || task.status === "skipped";
}

const statusOrder: Record<string, number> = {
  retry: 0, running: 1, pending: 2, failed: 3, needs_human: 4, done: 5, skipped: 6,
};

export function compareTaskStatus(a: TaskState & Pick<Task, "file">, b: TaskState & Pick<Task, "file">): number {
  return (statusOrder[taskDisplayStatus(a)] ?? 7) - (statusOrder[taskDisplayStatus(b)] ?? 7)
    || a.file.localeCompare(b.file);
}
