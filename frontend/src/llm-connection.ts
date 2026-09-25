import type { LLMConnection } from "./types";

export function isOAuthConnection(connection: LLMConnection): boolean {
  return connection.provider === "azure" && connection.authMode === "oauth";
}

// A signed-in account belongs to one saved authorization context. Changing the
// resource, tenant, app, or authentication method must not reuse its UI state.
export function hasMatchingOAuthSession(draft: LLMConnection, saved?: LLMConnection): boolean {
  return Boolean(saved && isOAuthConnection(draft) && isOAuthConnection(saved) && saved.oauthSignedIn
    && draft.endpoint === saved.endpoint
    && (draft.oauthTenantId || "").trim().toLowerCase() === (saved.oauthTenantId || "").trim().toLowerCase()
    && (draft.oauthClientId || "").trim().toLowerCase() === (saved.oauthClientId || "").trim().toLowerCase());
}

export function oauthConfigurationIssue(connection: LLMConnection): string {
  if (!isOAuthConnection(connection)) return "";
  const guid = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;
  if (!guid.test((connection.oauthTenantId || "").trim())) return "テナントIDをGUID形式で入力してください。";
  if (!guid.test((connection.oauthClientId || "").trim())) return "アプリケーション（クライアント）IDをGUID形式で入力してください。";
  return "";
}
