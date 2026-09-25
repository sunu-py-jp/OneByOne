import type { Backend } from "./types";
import { normalizeState, normalizeExecutionRunResult } from "./types";
import { createPreviewWorkspace, deletePreviewWorkspace, deletePreviewRule, previewDetail, previewState, selectPreviewTasks, selectPreviewWorkspace, savePreviewLLMConnection, deletePreviewLLMConnection, selectPreviewLLMConnection, clearPreviewLLMCredential, previewTargetFiles, previewTargetContent, previewExecutionContent, previewExecutionRun, previewExecutionFileDetail, previewResultPublication } from "./preview";

declare global {
  interface Window {
    go?: { main?: { App?: Backend } };
  }
}
export const isPreview =
  new URLSearchParams(window.location.search).get("demo") === "1";
export const isNative = Boolean(window.go?.main?.App);
function native(): Backend {
  if (isPreview)
    throw new Error(
      "画面プレビューではファイル操作・設定保存・AI実行はできません。専用アプリでお試しください。",
    );
  if (!window.go?.main?.App)
    throw new Error(
      "デスクトップアプリとの接続がありません。OneByOne アプリから起動するか、画面プレビューを開いてください。",
    );
  return window.go.main.App;
}
export const api: Backend = {
  CheckGitInstallation: () => native().CheckGitInstallation(),
  OpenGitInstallGuide: () => native().OpenGitInstallGuide(),
  DuplicateWorkspace: async (name) =>
    normalizeState(await native().DuplicateWorkspace(name)),
  CreateWorkspace: async (name, root) =>
    normalizeState(isPreview ? createPreviewWorkspace(name, root) : await native().CreateWorkspace(name, root)),
  CreateDemoWorkspace: async (name, root) => normalizeState(await native().CreateDemoWorkspace(name, root)),
  ValidateTargetFolder: (root) => isPreview ? Promise.resolve(root) : native().ValidateTargetFolder(root),
  CreateDemoProject: (parent) => native().CreateDemoProject(parent),
  SetTaskSelection: async (files) => normalizeState(isPreview ? selectPreviewTasks(files) : await native().SetTaskSelection(files)),
  ChangeTargetFolder: async (root) => normalizeState(await native().ChangeTargetFolder(root)),
  SelectWorkspace: async (id) =>
    normalizeState(
      isPreview
        ? selectPreviewWorkspace(id)
        : await native().SelectWorkspace(id),
    ),
  RenameWorkspace: async (name) =>
    normalizeState(await native().RenameWorkspace(name)),
  DeleteWorkspace: async (id) => normalizeState(isPreview ? deletePreviewWorkspace(id) : await native().DeleteWorkspace(id)),
  DeleteRule: async (id) => normalizeState(isPreview ? deletePreviewRule(id) : await native().DeleteRule(id)),
  GetState: async () =>
    normalizeState(
      isPreview ? structuredClone(previewState) : await native().GetState(),
    ),
  SaveConfig: async (config) =>
    normalizeState(await native().SaveConfig(config)),
  ChooseDirectory: (kind) =>
    isPreview && kind === "root"
      ? Promise.resolve("/workspace/sample-project")
      : native().ChooseDirectory(kind),
  ChooseLegacy: () => native().ChooseLegacy(),
  Scan: async () => normalizeState(await native().Scan()),
  Start: (limit) => native().Start(limit),
  Stop: () => native().Stop(),
  RetryTasks: async (files) => normalizeState(await native().RetryTasks(files)),
  DiscardFileChanges: async (file) => normalizeState(await native().DiscardFileChanges(file)),
  GetFileDetail: (file, attempt) =>
    isPreview
      ? Promise.resolve(previewDetail(file, attempt))
      : native().GetFileDetail(file, attempt),
  GetExecutionRun: async (id) => normalizeExecutionRunResult(isPreview ? previewExecutionRun(id) : await native().GetExecutionRun(id)),
  GetExecutionFileDetail: (id, file, attempt) => isPreview
    ? Promise.resolve(previewExecutionFileDetail(id, file, attempt))
    : native().GetExecutionFileDetail(id, file, attempt),
  ListTargetFiles: async (workspaceId, root) => isPreview
    ? previewTargetFiles(workspaceId, root)
    : native().ListTargetFiles(workspaceId, root),
  ReadTargetFile: async (workspaceId, root, file) => isPreview
    ? previewTargetContent(workspaceId, root, file)
    : native().ReadTargetFile(workspaceId, root, file),
  ReadExecutionFile: async (workspaceId, root, file) => isPreview
    ? previewExecutionContent(workspaceId, root, file)
    : native().ReadExecutionFile(workspaceId, root, file),
  ReadRule: (id) =>
    isPreview
      ? Promise.resolve(previewState.rules.find((rule) => rule.id === id)!)
      : native().ReadRule(id),
  OpenRule: async (id) => {
    if (!isPreview) return native().OpenRule(id);
    const rule = previewState.rules.find((candidate) => candidate.id === id);
    if (!rule) throw new Error(previewState.workspaces.find((workspace) => workspace.id === previewState.activeWorkspaceId)?.issues?.find((issue) => issue.ruleId === id)?.message || "ルールが見つかりません。");
    return { rule: structuredClone(rule), revision: "preview", readOnly: true };
  },
  CloseRule: async () => { if (!isPreview) await native().CloseRule(); },
  SaveRule: async (edit) => normalizeState(await native().SaveRule(edit)),
  SaveRules: async (edits) => normalizeState(await native().SaveRules(edits)),
  CreateRule: async (edit) => normalizeState(await native().CreateRule(edit)),
  ExportReport: () => native().ExportReport(),
  ExportExecutionReport: (id) => native().ExportExecutionReport(id),
  OpenWorktree: () => native().OpenWorktree(),
  GetResultPublicationPreview: () => isPreview ? Promise.resolve(previewResultPublication()) : native().GetResultPublicationPreview(),
  GetResultPublicationFileDiff: (workspaceId, revision, file) => {
    if (!isPreview) return native().GetResultPublicationFileDiff(workspaceId, revision, file);
    if (workspaceId !== previewState.activeWorkspaceId || revision !== "preview-publication") return Promise.reject(new Error("表示対象が変更されています。再読み込みしてください。"));
    return Promise.resolve(previewDetail(file).diff);
  },
  PublishResults: (request) => native().PublishResults(request),
  TestConnection: () => native().TestConnection(),
  SaveLLMConnection: async (connection) => normalizeState(isPreview
    ? savePreviewLLMConnection(connection) : await native().SaveLLMConnection(connection)),
  DeleteLLMConnection: async (id) => normalizeState(isPreview
    ? deletePreviewLLMConnection(id) : await native().DeleteLLMConnection(id)),
  SelectLLMConnection: async (id) => normalizeState(isPreview
    ? selectPreviewLLMConnection(id) : await native().SelectLLMConnection(id)),
  ClearLLMConnectionCredential: async (id) => normalizeState(isPreview
    ? clearPreviewLLMCredential(id) : await native().ClearLLMConnectionCredential(id)),
  TestLLMConnection: (id) => native().TestLLMConnection(id),
  SignInLLMConnection: async (id) => normalizeState(await native().SignInLLMConnection(id)),
  CancelLLMSignIn: () => native().CancelLLMSignIn(),
  SignOutLLMConnection: async (id) => normalizeState(await native().SignOutLLMConnection(id)),
  ChooseRulePackage: () => native().ChooseRulePackage(),
  ImportRulePackage: async (path, mode) => normalizeState(await native().ImportRulePackage(path, mode)),
  ExportRulePackage: () => native().ExportRulePackage(),
};
