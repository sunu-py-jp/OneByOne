import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from '../frontend/node_modules/typescript/lib/typescript.js';

const source = await readFile(new URL('../frontend/src/llm-connection.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } });
const { hasMatchingOAuthSession, oauthConfigurationIssue } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const saved = { provider: 'azure', authMode: 'oauth', endpoint: 'https://example.openai.azure.com/openai/v1/', deployment: 'model',
  oauthTenantId: 'abcd1234-1234-1234-1234-123456789abc', oauthClientId: '12345678-1234-1234-1234-123456789abc', oauthSignedIn: true };

test('OAuth draft must match its saved authorization context; name and deployment edits retain account availability', () => {
  assert.equal(hasMatchingOAuthSession({ ...saved, name: 'new', deployment: 'new-model' }, saved), true);
  assert.equal(hasMatchingOAuthSession({ ...saved, oauthTenantId: ` ${saved.oauthTenantId.toUpperCase()} ` }, saved), true);
  for (const change of [{ provider: 'openai' }, { authMode: 'api_key' }, { endpoint: 'https://another.openai.azure.com/' }, { oauthTenantId: 'other' }, { oauthClientId: 'other' }]) {
    assert.equal(hasMatchingOAuthSession({ ...saved, ...change }, saved), false, JSON.stringify(change));
  }
  assert.equal(hasMatchingOAuthSession(saved, { ...saved, oauthSignedIn: false }), false);
  assert.equal(hasMatchingOAuthSession(saved), false);
});

test('OAuth setup reports missing or malformed tenant/app IDs; API-key connections need neither', () => {
  assert.equal(oauthConfigurationIssue(saved), '');
  assert.match(oauthConfigurationIssue({ ...saved, oauthTenantId: undefined }), /テナントID/);
  assert.match(oauthConfigurationIssue({ ...saved, oauthTenantId: 'common' }), /テナントID/);
  assert.match(oauthConfigurationIssue({ ...saved, oauthClientId: undefined }), /クライアント/);
  assert.equal(oauthConfigurationIssue({ provider: 'azure', authMode: 'api_key' }), '');
});
