import type { Task } from "./types";

type TaskState = Pick<Task, "status" | "resumeRequested" | "canDiscardChanges">;

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

export function taskStatusLabel(task: TaskState): string {
  const status = taskDisplayStatus(task);
  return labels[status] ?? status;
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
