import type { Rule, Task } from "./types";
import { compareTaskStatus, taskDisplayStatus } from "./task-state";

export type ResultFilter = "all" | "done" | "attention" | "retry" | "pending" | "running" | "skipped" | "excluded";
export const isResultAttention = (task: Task) => task.status === "failed" || task.status === "needs_human";

export function summarizeResults(tasks: Task[], running: boolean, phase: string, lastError: string) {
  const counts = { all: 0, done: 0, attention: 0, retry: 0, pending: 0, running: 0, skipped: 0, excluded: 0, settled: 0, retryable: 0, attempted: 0 };
  for (const task of tasks) {
    if (task.excluded) { counts.excluded++; continue; }
    counts.all++;
    if (task.history.length || task.attempts > 0 || ["done", "skipped", "running"].includes(task.status)) counts.attempted++;
    if (isResultAttention(task)) counts.attention++;
    if (task.status === "failed") counts.retryable++;
    const status = taskDisplayStatus(task);
    if (status === "done" || status === "pending" || status === "retry" || status === "running" || status === "skipped") counts[status]++;
    // A failed attempt is still queued for automatic retry while the runner is active.
    if (["done", "skipped", "needs_human"].includes(task.status) || (!running && task.status === "failed")) counts.settled++;
  }
  const progress = counts.all ? Math.floor(counts.settled / counts.all * 100) : 0;
  const runLabel = running
    ? phase === "preparing" ? "準備中" : "実行中"
    : lastError ? "エラーで停止"
      : !counts.all ? "対象ファイル未選択"
        : !counts.attempted ? counts.attention ? "対象に要確認あり" : "実行前"
          : counts.settled < counts.all || counts.retryable ? "停止中"
            : counts.attention ? "処理終了・要確認あり" : "処理完了";
  return { counts, progress, runLabel };
}

export function filterResults(tasks: Task[], rules: Map<string, Pick<Rule, "title">>, filter: ResultFilter, search: string) {
  const query = search.trim().toLocaleLowerCase();
  return tasks.filter(task => {
    if (filter === "excluded" ? !task.excluded : task.excluded) return false;
    if (filter !== "all" && filter !== "excluded" && (filter === "attention" ? !isResultAttention(task) : taskDisplayStatus(task) !== filter)) return false;
    return !query || `${task.file} ${task.rulesApplied.map(id => `${id} ${rules.get(id)?.title || ""}`).join(" ")}`.toLocaleLowerCase().includes(query);
  }).sort(compareTaskStatus);
}
