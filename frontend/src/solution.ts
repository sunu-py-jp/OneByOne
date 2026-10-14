import type { SolutionConfig, SolutionRun, Task } from "./types";

export function selectedSolutionPaths(config?: SolutionConfig): string[] {
  const paths = Array.isArray(config?.paths) ? config.paths : config?.path?.trim() ? [config.path] : [];
  return [...new Set(paths.map(path => path.trim().replaceAll("\\", "/")))];
}

export function solutionAttemptIndex(tasks: Task[], file: string, run: SolutionRun): number | null {
  const history = tasks.find(task => task.file === file)?.history || [];
  const index = history.findIndex(attempt => attempt.solutionRunId === run.id);
  if (index >= 0) return index;
  // Old single-solution executions did not record solutionRunId. A matching
  // commit is immutable evidence; never fall back to cumulative shared content.
  const legacyIndex = run.commit ? history.findIndex(attempt => !attempt.solutionRunId && attempt.commit === run.commit) : -1;
  return legacyIndex >= 0 ? legacyIndex : null;
}

export function solutionConfigurationIssue(config?: SolutionConfig): string {
  const paths = selectedSolutionPaths(config);
  if (!config || paths.length === 0) return "処理するソリューションを1件以上選択してください。";
  if (paths.some(path => !/\.sln$/i.test(path))) return "Visual Studio の .sln ファイルを選択してください。";
  if (!config.configuration.trim()) return "ビルド構成を指定してください。";
  if (!config.platform.trim()) return "プラットフォームを指定してください。";
  if (!Number.isSafeInteger(config.maxIterations) || config.maxIterations < 0) return "修正回数の上限は 0 以上の整数で指定してください（0 は無制限）。";
  if (!Number.isInteger(config.buildTimeoutSeconds) || config.buildTimeoutSeconds < 1 || config.buildTimeoutSeconds > 86400) return "ビルドの制限時間は 1〜86400 秒の整数で指定してください。";
  return "";
}

export function solutionStatusLabel(status: string): string {
  return ({ pending: "待機中", not_run: "未実行", running: "実行中", completed: "完了", done: "ビルド成功", succeeded: "ビルド成功", failed: "エラーで終了", stopped: "停止", cancelled: "停止", needs_human: "確認が必要" } as Record<string, string>)[status] || status;
}
