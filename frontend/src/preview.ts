import {
  defaultConfig,
  emptyState,
  emptyUsage,
  type FileDetail,
  type LLMConnection,
  type Rule,
  type State,
  type Task,
  type TargetFileList,
  type TargetFileContent,
  type ChangeReportItem,
  type ExecutionRun,
  type ExecutionRunResult,
  type ResultPublicationPreview,
} from "./types";

const rules: Rule[] = [
  {
    id: "R001",
    title: "既存の振る舞いとコードスタイルを保持する",
    summary: "公開インターフェース、例外処理、コメントの意図を維持する",
    pattern: "",
    always: true,
    candidateCount: 0,
    appliedCount: 3,
    overview: "既存の公開インターフェースと、呼び出し元から見た振る舞いを保持してください。",
    before: "",
    after: "",
    notes: "無関係なリファクタリングをしない\n既存の例外処理とログを維持する\n編集対象は指定された1ファイルに限定する",
    holdConditions: "複数ファイルの同時変更が必要な場合は needs_human とする。",
  },
  {
    id: "R019",
    title: "保存処理をストレージクライアントAPIへ移す",
    summary: "StorageSession.save を StorageClient.write に変更する",
    pattern: "StorageSession|\\.save\\s*\\(",
    always: false,
    candidateCount: 6,
    appliedCount: 3,
    overview: "StorageSession.save を StorageClient.write に移行します。",
    before: "const store = new StorageSession(config);\nstore.save(key, value);",
    after: "const store = new StorageClient(config);\nstore.write({ key, value });",
    notes: "引数のキー名を省略せず、既存のエラー処理を維持する。",
    holdConditions: "saveの返り値に依存している場合は、互換性を確認するまで修正しない。",
  },
  {
    id: "R025",
    title: "設定オブジェクトの引数構成を変更",
    summary: "StorageOptions の timeout を request.timeoutMs へ移動する",
    pattern: "StorageOptions",
    always: false,
    candidateCount: 3,
    appliedCount: 1,
    overview: "StorageOptions の timeout を、新しい request.timeoutMs に移します。",
    before: "const options = new StorageOptions({ timeout: 3000 });",
    after: "const options = { request: { timeoutMs: 3000 } };",
    notes: "",
    holdConditions: "オプションが動的に組み立てられ、型を特定できない場合。",
  },
  {
    id: "R032",
    title: "同期リクエストを非同期処理へ変更",
    summary: "HttpRequest.send の同期処理を httpClient.send の Promise に変更する",
    pattern: "HttpRequest",
    always: false,
    candidateCount: 1,
    appliedCount: 0,
    overview: "HttpRequest.send の同期処理を httpClient.send の Promise に変更します。",
    before: "const response = HttpRequest.send(url);",
    after: "const response = await httpClient.send(url);",
    notes: "",
    holdConditions: "呼び出し元の関数シグネチャまで変更する必要がある場合。",
  },
];
const sampleDiff =
  'diff --git a/src/services/storage.ts b/src/services/storage.ts\n--- a/src/services/storage.ts\n+++ b/src/services/storage.ts\n@@ -1,10 +1,10 @@\n-import { StorageSession } from "vault-storage";\n+import { StorageClient } from "vault-client";\n \n export function saveDocument(key: string, value: string) {\n-  const store = new StorageSession(config);\n+  const store = new StorageClient(config);\n   try {\n-    store.save(key, value);\n+    store.write({ key, value });\n     logger.info("Document saved", { key });\n   } catch (error) {\n     logger.error("Save failed", error);\n     throw error;\n';
const taskSpecs = [
  [
    "src/services/storage.ts",
    "done",
    ["R019"],
    "save の引数をオブジェクト形式に変更。既存の例外処理は保持しました。",
  ],
  [
    "src/config/client-options.ts",
    "done",
    ["R019", "R025"],
    "タイムアウト設定を新しい構成へ移動しました。",
  ],
  [
    "src/repositories/document.ts",
    "done",
    ["R019"],
    "ストレージクライアントを移行しました。",
  ],
  [
    "src/services/sync-client.ts",
    "needs_human",
    ["R032"],
    "非同期化に伴い呼び出し元の変更が必要です。編集範囲を超えるため保留しました。",
  ],
  [
    "src/adapters/cache.ts",
    "failed",
    ["R019", "R025"],
    "旧シンボル残存チェックに失敗しました。次の試行に検証結果を引き継ぎます。",
  ],
  ["src/commands/export.ts", "pending", ["R019"], ""],
  ["src/config/defaults.ts", "pending", ["R025"], ""],
  [
    "src/tests/storage.test.ts",
    "skipped",
    ["R019"],
    "文字列に一致しましたが、対象APIの呼び出しはありませんでした。",
  ],
  ["src/utils/format.ts", "pending", [], "共通ルールの確認対象です。"],
] as const;
const tasks: Task[] = taskSpecs.map(([file, status, candidates, note], i) => ({
  file,
  status,
  rules: [...candidates],
  note,
  attempts: status === "pending" ? 0 : 1,
  inputHash: "",
  updatedAt: "2026-09-12T13:42:16Z",
  rulesApplied: status === "done" ? ["R001", ...candidates] : [],
  history:
    status === "pending"
      ? []
      : [
          {
            id: `preview-${i}`,
            number: 1,
            startedAt: "2026-09-12T13:41:30Z",
            finishedAt: "2026-09-12T13:42:16Z",
            outcome: status,
            note,
            rulesApplied: status === "done" ? ["R001", ...candidates] : [],
            checks:
              status === "done"
                ? [
                    {
                      name: "旧シンボル残存チェック",
                      status: "passed",
                      detail: "旧ライブラリ由来の識別子は残っていません。",
                      durationMs: 28,
                    },
                    {
                      name: "編集範囲チェック",
                      status: "passed",
                      detail: "編集は対象ファイル1つに限定されています。",
                      durationMs: 14,
                    },
                    {
                      name: "TypeScript build",
                      status: "passed",
                      detail: "> tsc --noEmit\nProcess exited with code 0.",
                      durationMs: 2413,
                    },
                  ]
                : status === "failed"
                  ? [
                      {
                        name: "旧シンボル残存チェック",
                        status: "failed",
                        detail:
                          "src/adapters/cache.ts:42: StorageOptions が残っています。",
                        durationMs: 22,
                      },
                    ]
                  : [],
            usage: {
              inputTokens: 4260,
              cachedTokens: 1920,
              outputTokens: 824,
              costUsd: 0.0162,
              turns: 3,
            },
            diffPath: "",
            commit: status === "done" ? "e29b7a18f4607b0" : "",
            changes: (status === "done" || status === "failed") && candidates.some(id => id === "R019") ? [{
              id: "P01", ruleId: "R019", ruleTitle: "保存処理をストレージクライアントAPIへ移す",
              location: `${file}:1,4,6`,
              risk: "保存APIの引数形式を変える際に、保存先のキー・値や既存の例外処理が失われる可能性があります。",
              change: "インポートと生成するクライアントを変更し、save(key, value) を write({ key, value }) に置き換えました。",
              expected: "同じキーと値を保存し、成功時のログと例外の再送出を維持します。",
              status: status === "done" ? "fixed" : "not_applied",
              reason: status === "done" ? "検証を通過し、変更を保存しました。" : "修正案は検証を通過していないため、ファイルへの変更は採用していません。",
              lineRanges: [1, 4, 6].map(line => ({ beforeStart: line, beforeEnd: line, afterStart: line, afterEnd: line })),
            }] : [],
          },
        ],
}));

for (const rule of rules) {
  rule.candidateCount = rule.always ? tasks.length : tasks.filter((task) => task.rules.includes(rule.id)).length;
}

export const previewState: State = {
  executionRuns: [],
  readOnly: false,
  llmSettingsPath: "/Users/you/Library/Application Support/OneByOne/private/llm-settings/",
  llmConnections: [
    { id: "preview-azure", name: "Azure 開発用", provider: "azure", endpoint: "https://example.openai.azure.com/openai/v1/", deployment: "code-model", authMode: "api_key", credential: "", credentialSet: true },
    { id: "preview-openai", name: "OpenAI 検証用", provider: "openai", endpoint: "https://api.openai.com/v1/", deployment: "code-model", authMode: "api_key", credential: "", credentialSet: true },
  ],
  selectedLLMConnectionId: "preview-azure",
  workspaces: [
    {
      id: "storage-api",
      name: "ストレージAPI移行",
      root: "/workspace/sample-project",
    },
    {
      id: "storage-api-canary",
      name: "ストレージAPI移行・比較用",
      root: "/workspace/sample-project",
    },
    {
      id: "framework-upgrade",
      name: "フレームワーク更新",
      root: "/workspace/web-project",
      issues: [
        { id: "catalog:R001", page: "rules", ruleId: "R001", message: "rule R001: 旧形式のname.txtは使用できません。rule.jsonに項目を保存してください" },
        { id: "catalog:R019", page: "rules", ruleId: "R019", message: "rule R019: rule.jsonの変更概要を文字列で指定してください" },
      ],
    },
  ],
  activeWorkspaceId: "storage-api",
  config: {
    ...defaultConfig,
    root: "/workspace/sample-project",
    rulesPath: "/app/OneByOne/workspaces/storage-api/rule-packages/sample/rules",
    rulePackageName: "storage-api.oborules",
    legacyPath: "/app/OneByOne/workspaces/storage-api/rule-packages/sample/patterns/legacy-symbols.txt",
    queuePath:
      "/app/OneByOne/workspaces/storage-api/runs/queue.jsonl",
    endpoint: "https://example.openai.azure.com/openai/v1/",
    deployment: "code-model",
    credentialSet: true,
    includeGlobs: ["**/*.ts"],
    checkCommands: [
      {
        name: "TypeScript build",
        executable: "npm",
        args: ["run", "typecheck"],
      },
    ],
  },
  tasks,
  rules,
  logs: [
    {
      time: "2026-09-12T13:40:00Z",
      level: "info",
      message:
        "画面プレビュー用のサンプルデータです。AIやファイル操作は実行していません。",
    },
    {
      time: "2026-09-12T13:40:01Z",
      level: "info",
      message: `表示例: 共通ルールを含むため、対象範囲の ${tasks.length} ファイルすべてをキューに追加。`,
    },
    {
      time: "2026-09-12T13:42:16Z",
      level: "info",
      message: "表示例: src/services/storage.ts の検証に合格。",
    },
    {
      time: "2026-09-12T13:43:10Z",
      level: "warn",
      message:
        "表示例: src/services/sync-client.ts は呼び出し元の変更が必要なため保留。",
    },
  ],
  running: false,
  phase: "idle",
  currentFile: "",
  worktree: "/workspace/run/worktree",
  branch: "onebyone/20260912-134000",
  scannedCount: tasks.length,
  excludedCount: 0,
  usage: {
    ...emptyUsage,
    inputTokens: 21640,
    cachedTokens: 9600,
    outputTokens: 4120,
    costUsd: 0.081,
    turns: 15,
  },
  lastError: "",
};
const previewExecutionSnapshots = new Map<string, Map<string, ExecutionRunResult>>();
const previewSampleBase = structuredClone(previewState);
const previewWorkspaceStates = new Map(previewState.workspaces.map((workspace) => {
  const state = structuredClone(previewSampleBase);
  state.activeWorkspaceId = workspace.id;
  state.config.root = workspace.root;
  state.config.queuePath = `/app/OneByOne/workspaces/${workspace.id}/runs/queue.jsonl`;
  if (workspace.id !== "framework-upgrade") preparePreviewExecutionRuns(state);
  return [workspace.id, state];
}));
Object.assign(previewState, structuredClone(previewWorkspaceStates.get(previewState.activeWorkspaceId)!));
const invalidPreviewWorkspace = previewWorkspaceStates.get("framework-upgrade")!;
invalidPreviewWorkspace.rules = [];
invalidPreviewWorkspace.lastError = previewState.workspaces.find((workspace) => workspace.id === "framework-upgrade")!.issues![0].message;
const previewWorkspaceLocks: Record<string, State["workspaceLock"]> = {
  "storage-api-canary": { owner: "佐藤", host: "DESIGN-PC", openedAt: "2026-09-12T13:40:00Z" },
};

function preparePreviewExecutionRuns(state: State) {
  const first: ExecutionRun = { id: `preview-${state.activeWorkspaceId}-run-1`, startedAt: "2026-09-12T13:40:00Z", finishedAt: "2026-09-12T13:43:10Z", status: "completed", targetCount: state.tasks.filter(task => task.attempts > 0).length, error: "" };
  const second: ExecutionRun = { id: `preview-${state.activeWorkspaceId}-run-2`, startedAt: "2026-09-12T14:10:00Z", finishedAt: "2026-09-12T14:12:20Z", status: "completed", targetCount: 3, error: "" };
  for (const task of state.tasks) {
    for (const attempt of task.history) {
      attempt.executionId = first.id;
      attempt.changes = (attempt.changes || []).map(item => ({ ...item, sourceAttemptId: attempt.id }));
    }
    task.canDiscardChanges = task.history.some(attempt => attempt.outcome === "done" && Boolean(attempt.commit));
  }
  state.executionRuns = [first];
  refreshPreviewExecutionSummary(state);
  const firstResult: ExecutionRunResult = { run: structuredClone(first), state: structuredClone(state), targetFiles: state.tasks.filter(task => task.attempts > 0).map(task => task.file) };
  const retried = state.tasks.find(task => task.file === "src/repositories/document.ts")!;
  const retryAttempt = {
    ...structuredClone(retried.history[0]), id: `${second.id}-document`, executionId: second.id, number: 2,
    startedAt: second.startedAt, finishedAt: "2026-09-12T14:10:48Z", outcome: "skipped", commit: "",
    note: "前回の修正は要件を満たしており、追加の変更はありません。", changes: [],
    usage: { inputTokens: 3140, cachedTokens: 1920, outputTokens: 380, costUsd: 0.009, turns: 2 },
  };
  retried.history.push(retryAttempt);
  Object.assign(retried, { attempts: 2, status: "done", note: retryAttempt.note, updatedAt: retryAttempt.finishedAt });
  const added = state.tasks.find(task => task.file === "src/commands/export.ts")!;
  const example = state.tasks.find(task => task.file === "src/services/storage.ts")!;
  const addedAttempt = {
    ...structuredClone(example.history[0]), id: `${second.id}-export`, executionId: second.id,
    startedAt: "2026-09-12T14:10:48Z", finishedAt: "2026-09-12T14:12:02Z",
    note: "エクスポート先の保存処理を移行し、例外処理を維持しました。", commit: "e29b7a18f4607b2",
  };
  addedAttempt.changes = (addedAttempt.changes || []).map(item => ({ ...item, sourceAttemptId: addedAttempt.id, location: item.location.replaceAll(example.file, added.file) }));
  Object.assign(added, { status: "done", attempts: 1, note: addedAttempt.note, updatedAt: addedAttempt.finishedAt, history: [addedAttempt], rulesApplied: [...addedAttempt.rulesApplied], canDiscardChanges: true });
  const unchanged = state.tasks.find(task => task.file === "src/utils/format.ts")!;
  const unchangedAttempt = {
    ...structuredClone(retryAttempt), id: `${second.id}-format`, number: 1,
    startedAt: "2026-09-12T14:12:02Z", finishedAt: second.finishedAt,
    rulesApplied: ["R001"], note: "共通ルールを確認しました。既存の実装は要件を満たしています。",
  };
  Object.assign(unchanged, { status: "skipped", attempts: 1, note: unchangedAttempt.note, updatedAt: second.finishedAt, history: [unchangedAttempt], rulesApplied: [], canDiscardChanges: false });
  state.executionRuns = [first, second];
  refreshPreviewExecutionSummary(state);
  state.logs.push({ time: second.finishedAt, level: "info", message: "表示例: 2回目の実行は3ファイルを確認し、1ファイルの追加修正を採用しました。" });
  const secondResult: ExecutionRunResult = { run: structuredClone(second), state: structuredClone(state), targetFiles: [retried.file, added.file, unchanged.file] };
  previewExecutionSnapshots.set(state.activeWorkspaceId, new Map([[first.id, firstResult], [second.id, secondResult]]));
}

function refreshPreviewExecutionSummary(state: State) {
  state.usage = state.tasks.flatMap(task => task.history).reduce((total, attempt) => ({
    uncertain: total.uncertain || attempt.usage.uncertain,
    inputTokens: total.inputTokens + attempt.usage.inputTokens, cachedTokens: total.cachedTokens + attempt.usage.cachedTokens,
    outputTokens: total.outputTokens + attempt.usage.outputTokens, costUsd: total.costUsd + attempt.usage.costUsd, turns: total.turns + attempt.usage.turns,
  }), { ...emptyUsage });
  for (const rule of state.rules) rule.appliedCount = state.tasks.filter(task => task.rulesApplied.includes(rule.id)).length;
}

function previewFileVersions(file: string): { before: string; after: string } {
  if (file === "src/utils/format.ts") {
    const content = 'export const format = (value: string) => value.trim();\n';
    return { before: content, after: content };
  }
  return {
    before:
      'import { StorageSession } from "vault-storage";\n\nexport function saveDocument(key: string, value: string) {\n  const store = new StorageSession(config);\n  try {\n    store.save(key, value);\n    logger.info("Document saved", { key });\n  } catch (error) {\n    logger.error("Save failed", error);\n    throw error;\n  }\n}',
    after:
      'import { StorageClient } from "vault-client";\n\nexport function saveDocument(key: string, value: string) {\n  const store = new StorageClient(config);\n  try {\n    store.write({ key, value });\n    logger.info("Document saved", { key });\n  } catch (error) {\n    logger.error("Save failed", error);\n    throw error;\n  }\n}',
  };
}

function previewLastDiscard(task: Task, throughAttempt = Infinity): number {
  return Math.max(0, ...(task.discards || []).filter(item => item.state === "done" && item.throughAttempt < throughAttempt).map(item => item.throughAttempt));
}

function previewCumulativeChanges(task: Task, contentChanged: boolean): ChangeReportItem[] {
  const history = task.history.filter(attempt => attempt.number > previewLastDiscard(task));
  const fixed = new Map<string, ChangeReportItem>();
  const key = (item: ChangeReportItem) => `${item.ruleId}\0${item.location}\0${item.change}`;
  for (const attempt of history) {
    if (!contentChanged || attempt.outcome !== "done" || !attempt.commit) continue;
    for (const [index, item] of (attempt.changes || []).entries()) {
      if (item.status === "fixed") fixed.set(key(item), {
        ...item, sourceAttemptId: attempt.id, id: `attempt:${attempt.id || attempt.number}:${index}:${item.id}`,
        location: item.location.trim() ? `修正時の位置: ${item.location}` : item.location,
      });
    }
  }
  const latestAttempt = history.at(-1);
  const fixedRules = new Set([...fixed.values()].map(item => item.ruleId));
  const unresolved = (latestAttempt?.changes || [])
    .flatMap((item, index) => item.status === "fixed" || (item.status === "unchanged" && item.id.startsWith("rule:") && fixedRules.has(item.ruleId))
      ? [] : [{ ...item, sourceAttemptId: latestAttempt!.id, id: `attempt:${latestAttempt!.id || latestAttempt!.number}:${index}:${item.id}`, lineRanges: [] }]);
  return structuredClone([...fixed.values(), ...unresolved]);
}

export function previewDetail(file: string, attemptIndex = -1): FileDetail {
  return previewDetailForState(previewState, file, attemptIndex);
}

export function previewResultPublication(): ResultPublicationPreview {
  const files = previewState.tasks.flatMap(task => {
    const detail = previewDetail(task.file);
    if (!detail.diff) return [];
    const fixed = (detail.changes || []).filter(change => change.status === "fixed");
    return [{ file: task.file, rulesApplied: [...new Set(fixed.map(change => change.ruleId))],
      summary: fixed.map(change => change.change).filter(Boolean).join("\n") || task.note, diff: "" }];
  });
  return {
    workspaceId: previewState.activeWorkspaceId, revision: "preview-publication", baseCommit: "4e87db28ca00000000000000000000000000000000",
    sourceCommit: "f17c92c87000000000000000000000000000000000", suggestedBranch: "onebyone-result/storage-api",
    message: `${files.length}ファイルの修正を反映\n\n` + files.map(file => `${file.file}\n${file.rulesApplied.join(", ")}\n${file.summary}`).join("\n\n"),
    files, publications: [],
  };
}

function previewDetailForState(state: State, file: string, attemptIndex = -1): FileDetail {
  const task = state.tasks.find(item => item.file === file);
  if (!task) throw new Error("対象一覧にないファイルは表示できません。");
  if (!Number.isInteger(attemptIndex) || attemptIndex < -1 || attemptIndex >= task.history.length)
    throw new Error("指定された試行が見つかりません。");
  const versions = previewFileVersions(file);
  let before = versions.before;
  let after = before;
  let changes: ChangeReportItem[];
  if (attemptIndex < 0) {
    const accepted = task.history.some(attempt => attempt.outcome === "done" && Boolean(attempt.commit) && attempt.number > previewLastDiscard(task));
    if (accepted) after = versions.after;
    changes = previewCumulativeChanges(task, before !== after);
  } else {
    const attempt = task.history[attemptIndex];
    const lastDiscard = previewLastDiscard(task, attempt.number);
    const acceptedBefore = task.history.slice(0, attemptIndex).some(item => item.outcome === "done" && Boolean(item.commit) && item.number > lastDiscard);
    if (acceptedBefore) before = versions.after;
    after = attempt.outcome === "done" || attempt.outcome === "failed" ? versions.after : before;
    changes = (attempt.changes || []).map(item => ({ ...structuredClone(item), sourceAttemptId: attempt.id }));
  }
  return {
    task: structuredClone(task), before, after,
    diff: before === after ? "" : sampleDiff.replaceAll("src/services/storage.ts", file),
    cumulative: attemptIndex < 0, changes,
  };
}

export function previewExecutionRun(id: string): ExecutionRunResult {
  const result = previewExecutionSnapshots.get(previewState.activeWorkspaceId)?.get(id);
  if (!result || !previewState.executionRuns.some(run => run.id === id)) throw new Error("このワークスペースの実行履歴が見つかりません。");
  return structuredClone(result);
}

export function previewExecutionFileDetail(id: string, file: string, attemptIndex = -1): FileDetail {
  return previewDetailForState(previewExecutionRun(id).state, file, attemptIndex);
}

function previewSourceFiles(): Record<string, string> {
  return {
    "README.md": "# Sample project\n\nドキュメントを保存するサンプルプロジェクトです。\n",
    "package.json": '{\n  "name": "sample-project",\n  "private": true,\n  "type": "module"\n}\n',
    ...Object.fromEntries(tasks.map(task => [task.file, previewFileVersions(task.file).before])),
  };
}

function checkPreviewTarget(workspaceId: string, root: string) {
  if (workspaceId !== previewState.activeWorkspaceId || root !== previewState.config.root)
    throw new Error("対象フォルダが変更されています。再読み込みしてください。");
}

export function previewTargetFiles(workspaceId: string, root: string): TargetFileList {
  checkPreviewTarget(workspaceId, root);
  return { workspaceId, root, truncated: false, limit: 50000,
    files: Object.entries(previewSourceFiles()).sort(([a], [b]) => a.localeCompare(b)).map(([file, content]) => ({ file, size: new TextEncoder().encode(content).length })) };
}

export function previewTargetContent(workspaceId: string, root: string, file: string): TargetFileContent {
  checkPreviewTarget(workspaceId, root);
  const sources = previewSourceFiles();
  if (!Object.hasOwn(sources, file)) throw new Error("ファイルが見つかりません。");
  const content = sources[file];
  return { workspaceId, root, file, size: new TextEncoder().encode(content).length, content, unavailableReason: "" };
}

export function previewExecutionContent(workspaceId: string, root: string, file: string): TargetFileContent {
  const original = previewTargetContent(workspaceId, root, file);
  const task = previewState.tasks.find(item => item.file === file);
  if (!task) throw new Error("対象一覧にないファイルは表示できません。");
  const content = previewDetail(file).after;
  return { ...original, content, size: new TextEncoder().encode(content).length };
}

export function selectPreviewTasks(files: string[]): State {
  if (previewState.readOnly || previewState.running) throw new Error("現在は対象ファイルを変更できません。");
  const known = new Set(previewState.tasks.map((task) => task.file));
  if (files.some((file) => !known.has(file))) throw new Error("対象一覧にないファイルは選択できません。");
  const selected = new Set(files);
  previewState.tasks = previewState.tasks.map((task) => ({ ...task, excluded: !selected.has(task.file) }));
  return structuredClone(previewState);
}

export function selectPreviewWorkspace(id: string): State {
  const workspace = previewState.workspaces.find((item) => item.id === id);
  if (!workspace) throw new Error("ワークスペースが見つかりません");
  previewWorkspaceStates.set(previewState.activeWorkspaceId, structuredClone(previewState));
  const currentWorkspaces = previewState.workspaces;
  const currentConnections = previewState.llmConnections;
  const saved = previewWorkspaceStates.get(id);
  if (saved) Object.assign(previewState, structuredClone(saved));
  previewState.workspaces = currentWorkspaces;
  previewState.llmConnections = currentConnections;
  previewState.activeWorkspaceId = id;
  previewState.readOnly = Boolean(previewWorkspaceLocks[id]);
  previewState.workspaceLock = structuredClone(previewWorkspaceLocks[id]);
  previewState.config.root = workspace.root;
  previewState.config.queuePath = `/app/OneByOne/workspaces/${workspace.id}/runs/queue.jsonl`;
  applyPreviewLLMSelection();
  return structuredClone(previewState);
}

export function deletePreviewWorkspace(id: string): State {
  if (id !== previewState.activeWorkspaceId || previewState.readOnly) throw new Error("このワークスペースは削除できません。");
  previewState.workspaces = previewState.workspaces.filter((workspace) => workspace.id !== id);
  const next = previewState.workspaces[0];
  if (next) selectPreviewWorkspace(next.id);
  else {
    const connections = previewState.llmConnections;
    Object.assign(previewState, structuredClone(emptyState), { llmConnections: connections });
  }
  previewWorkspaceStates.delete(id);
  previewExecutionSnapshots.delete(id);
  return structuredClone(previewState);
}

export function deletePreviewRule(id: string): State {
  if (previewState.readOnly) throw new Error("このルールは削除できません。");
  const workspace = previewState.workspaces.find((item) => item.id === previewState.activeWorkspaceId)!;
  if (!previewState.rules.some((rule) => rule.id === id) && !workspace.issues?.some((issue) => issue.ruleId === id)) throw new Error("ルールが見つかりません。");
  previewState.rules = previewState.rules.filter((rule) => rule.id !== id);
  workspace.issues = workspace.issues?.filter((issue) => issue.ruleId !== id);
  previewState.lastError = workspace.issues?.[0]?.message || "";
  return structuredClone(previewState);
}

// These preview operations change only sample data in this browser tab.
export function createPreviewWorkspace(name: string, root: string): State {
  if (!root || !name) throw new Error("対象フォルダと名前を指定してください");
  const id = crypto.randomUUID().replaceAll("-", "").slice(0, 24);
  previewState.workspaces.push({ id, name, root });
  previewWorkspaceStates.set(id, {
    ...structuredClone(previewState),
    selectedLLMConnectionId: "",
    config: { ...structuredClone(defaultConfig), root },
    tasks: [], rules: [], logs: [], executionRuns: [], worktree: "", branch: "", currentFile: "",
    phase: "idle", scannedCount: 0, excludedCount: 0, usage: { ...emptyUsage }, lastError: "",
  });
  return selectPreviewWorkspace(id);
}

function applyPreviewLLMSelection() {
  const connection = previewState.llmConnections.find((item) => item.id === previewState.selectedLLMConnectionId);
  if (!connection) previewState.selectedLLMConnectionId = "";
  const selected = connection || defaultConfig;
  Object.assign(previewState.config, {
    llmConnectionId: previewState.selectedLLMConnectionId,
    provider: selected.provider, endpoint: connection?.endpoint || "",
    deployment: connection?.deployment || "", authMode: selected.authMode,
    credential: "", credentialSet: connection?.credentialSet || false,
  });
}
export function savePreviewLLMConnection(input: LLMConnection): State {
  const name = input.name.trim();
  if (!name) throw new Error("接続名を入力してください");
  if (previewState.llmConnections.some((item) => item.id !== input.id && item.name.toLocaleLowerCase() === name.toLocaleLowerCase())) throw new Error("同じ名前のLLM接続が既にあります");
  const previous = previewState.llmConnections.find((item) => item.id === input.id);
  if (input.id && !previous) throw new Error("LLM接続が見つかりません");
  const unchanged = previous?.provider === input.provider && previous.endpoint === input.endpoint && previous.authMode === input.authMode;
  const oauth = input.provider === "azure" && input.authMode === "oauth";
  const connection: LLMConnection = {
    ...input, name, id: input.id || crypto.randomUUID(), credential: "",
    credentialSet: !oauth && (Boolean(input.credential) || Boolean(unchanged && previous?.credentialSet)),
    oauthTenantId: input.oauthTenantId || "", oauthClientId: input.oauthClientId || "",
    // Preview never creates or simulates an authenticated Microsoft account.
    oauthSignedIn: false, oauthUsername: "",
  };
  if (previous) previewState.llmConnections = previewState.llmConnections.map((item) => item.id === connection.id ? connection : item);
  else previewState.llmConnections.push(connection);
  applyPreviewLLMSelection();
  return structuredClone(previewState);
}
export function selectPreviewLLMConnection(id: string): State {
  if (!previewState.activeWorkspaceId) throw new Error("ワークスペースを選択してください");
  if (previewState.readOnly) throw new Error("このワークスペースは閲覧専用です。");
  if (id && !previewState.llmConnections.some((item) => item.id === id)) throw new Error("LLM接続が見つかりません");
  previewState.selectedLLMConnectionId = id;
  applyPreviewLLMSelection();
  return structuredClone(previewState);
}
export function deletePreviewLLMConnection(id: string): State {
  if (!previewState.llmConnections.some((item) => item.id === id)) throw new Error("LLM接続が見つかりません");
  previewState.llmConnections = previewState.llmConnections.filter((item) => item.id !== id);
  for (const state of previewWorkspaceStates.values()) {
    if (state.selectedLLMConnectionId === id) state.selectedLLMConnectionId = "";
  }
  applyPreviewLLMSelection();
  return structuredClone(previewState);
}
export function clearPreviewLLMCredential(id: string): State {
  const connection = previewState.llmConnections.find((item) => item.id === id);
  if (!connection) throw new Error("LLM接続が見つかりません");
  connection.credential = "";
  connection.credentialSet = false;
  applyPreviewLLMSelection();
  return structuredClone(previewState);
}
