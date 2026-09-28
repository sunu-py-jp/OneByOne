import type { ResultPublicationPreview } from "./types";

export interface PublicationDraft {
  workspaceId: string;
  baseCommit: string;
  branch: string;
  title: string;
  message: string;
  messageAsFile: boolean;
  suggestedBranch: string;
  suggestedMessage: string;
}

/** Refresh generated values only where the user has not edited them. */
export function refreshPublicationDraft(previous: PublicationDraft | undefined, preview: ResultPublicationPreview): PublicationDraft {
  const sameWorkspace = previous?.workspaceId === preview.workspaceId && previous.baseCommit === preview.baseCommit;
  return {
    workspaceId: preview.workspaceId,
    baseCommit: preview.baseCommit,
    branch: sameWorkspace && previous.branch !== previous.suggestedBranch ? previous.branch : preview.suggestedBranch,
    title: sameWorkspace ? previous.title : "",
    message: sameWorkspace && previous.message !== previous.suggestedMessage ? previous.message : preview.message,
    messageAsFile: previous?.workspaceId === preview.workspaceId && previous.messageAsFile === true,
    suggestedBranch: preview.suggestedBranch,
    suggestedMessage: preview.message,
  };
}

export const DEFAULT_MESSAGE_FILE_THRESHOLD = 10_000;

export function publicationMessageFileMode(message: string, selected: boolean, configuredThreshold?: number) {
  const threshold = configuredThreshold && Number.isSafeInteger(configuredThreshold) && configuredThreshold > 0
    ? configuredThreshold : DEFAULT_MESSAGE_FILE_THRESHOLD;
  let length = 0;
  // Count Unicode code points, matching Go's rune count rather than UTF-16 units.
  for (const _ of message) { if (++length > threshold) break; }
  const required = length > threshold;
  return { threshold, required, selected: required || selected };
}

const preferenceKey = (workspaceId: string) => `onebyone.publication.messageAsFile:${workspaceId}`;
export function readPublicationFilePreference(workspaceId: string): boolean {
  try { return globalThis.localStorage?.getItem(preferenceKey(workspaceId)) === "true"; }
  catch { return false; }
}
export function savePublicationFilePreference(workspaceId: string, value: boolean) {
  try { globalThis.localStorage?.setItem(preferenceKey(workspaceId), String(value)); }
  catch { /* The live workspace draft still retains the choice if storage is unavailable. */ }
}

export function publicationBlockReason({ preview, draft, busy, running, readOnly, usable, loading, error }: {
  preview?: ResultPublicationPreview;
  draft?: PublicationDraft;
  busy: boolean;
  running: boolean;
  readOnly: boolean;
  usable: boolean;
  loading: boolean;
  error: string;
}): string {
  if (!usable) return "結果の反映はデスクトップアプリで利用できます。";
  if (running) return "実行が終了してから反映してください。";
  if (busy || loading) return "処理が終わるまでお待ちください。";
  if (readOnly) return "このワークスペースは閲覧専用です。";
  if (error) return "エラーを解消し、反映内容を再読み込みしてください。";
  if (!preview || !draft || draft.workspaceId !== preview.workspaceId || draft.baseCommit !== preview.baseCommit) return "反映内容を読み込んでください。";
  const fileMode = publicationMessageFileMode(draft.message, draft.messageAsFile === true, preview.messageFileThreshold);
  if (!preview.files.length && !(preview.reportFiles?.length && fileMode.selected)) return "反映する変更がありません。";
  if (!draft.branch.trim()) return "新規ブランチ名を入力してください。";
  if (preview.publications.some(item => item.branch === draft.branch.trim())) return "反映済みのブランチ名です。別の名前を入力してください。";
  if (!draft.title.trim()) return "コミットタイトルを入力してください。";
  return "";
}
