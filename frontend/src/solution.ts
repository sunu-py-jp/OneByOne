import type { SolutionConfig } from "./types";

export function solutionConfigurationIssue(config?: SolutionConfig): string {
  if (!config?.path.trim()) return "処理する .sln ファイルを指定してください。";
  if (!/\.sln$/i.test(config.path.trim())) return "Visual Studio の .sln ファイルを指定してください。";
  if (!config.configuration.trim()) return "ビルド構成を指定してください。";
  if (!config.platform.trim()) return "プラットフォームを指定してください。";
  if (!Number.isSafeInteger(config.maxIterations) || config.maxIterations < 0) return "修正回数の上限は 0 以上の整数で指定してください（0 は無制限）。";
  if (!Number.isInteger(config.buildTimeoutSeconds) || config.buildTimeoutSeconds < 1 || config.buildTimeoutSeconds > 86400) return "ビルドの制限時間は 1〜86400 秒の整数で指定してください。";
  return "";
}

export function solutionStatusLabel(status: string): string {
  return ({ running: "実行中", completed: "ビルド成功", done: "ビルド成功", succeeded: "ビルド成功", failed: "エラーで終了", stopped: "停止", cancelled: "停止", needs_human: "確認が必要" } as Record<string, string>)[status] || status;
}
