import type { State, Task } from "./types";

export type WorkflowPage = "target" | "rules" | "run" | "review" | "results" | "publish";

export function hasReadyLLMConnection(state: Pick<State, "llmConnections" | "selectedLLMConnectionId">): boolean {
  const connection = state.llmConnections.find(item => item.id === state.selectedLLMConnectionId);
  if (!connection?.endpoint || !connection.deployment) return false;
  if (connection.authMode === "oauth") {
    return connection.provider === "azure" && Boolean(connection.oauthSignedIn && connection.oauthTenantId && connection.oauthClientId);
  }
  return Boolean(connection.credentialSet);
}

export function countReadyTargets(tasks: Task[], selectedFiles?: ReadonlySet<string>): number {
  return tasks.filter(item => (selectedFiles ? selectedFiles.has(item.file) : !item.excluded)
    && (item.status === "pending" || item.status === "failed")).length;
}

export interface WorkflowNavigationState {
  busy: boolean;
  running: boolean;
  available: boolean;
  targetReady: boolean;
  setupReady: boolean;
  connectionReady: boolean;
  readOnly: boolean;
  selectionCurrent: boolean;
  readyCount: number;
  selectedCount?: number;
  targetError?: string;
  setupError?: string;
}

// Tabs, footer buttons, and ordinary navigation handlers use the same destination
// permissions. Diagnostic links remain separate so errors can always be inspected.
export function workflowAvailability(state: WorkflowNavigationState): Record<WorkflowPage, boolean> {
  const reasons = workflowBlockReasons(state);
  return Object.fromEntries(Object.entries(reasons).map(([page, reason]) => [page, !reason])) as Record<WorkflowPage, boolean>;
}

export function workflowBlockReasons(state: WorkflowNavigationState): Record<WorkflowPage, string> {
  const base = !state.available ? "アプリとの接続を確認してください。" : state.busy ? "処理が終わるまでお待ちください。" : "";
  const idle = base || (state.running ? "実行中です。停止または完了後に変更できます。" : "");
  const target = idle || (!state.targetReady ? state.targetError || "対象フォルダを設定し、エラーを解消してください。" : "");
  const setup = target || (!state.setupReady ? state.setupError || "ルールを設定し、エラーを解消してください。" : "");
  const review = setup || (state.readOnly ? "閲覧専用です。編集権限を取得してから実行してください。" : "")
    || (!state.connectionReady ? "使用するLLM接続を選び、モデルと認証情報を設定してください。" : "")
    || (!state.selectionCurrent ? "設定が変更されています。対象ファイルを更新してください。" : "")
    || (state.selectedCount === 0 ? "処理するファイルを1つ以上選択してください。" : "")
    || (state.readyCount <= 0 ? "選択中に処理可能なファイルがありません。処理するファイルを再試行に追加してください。" : "");
  return { target: idle, rules: target, run: setup, review, results: base, publish: idle };
}
