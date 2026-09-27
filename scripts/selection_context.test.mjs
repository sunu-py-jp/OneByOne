import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from '../frontend/node_modules/typescript/lib/typescript.js';

const source = await readFile(new URL('../frontend/src/selection.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } });
const { selectionContextKey } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const config = { root: '/project', queuePath: '/app/queue.jsonl', rulesPath: '/rules/v1', maxFileBytes: 524288 };
const rules = [{ id: 'R001', title: 'Common', summary: 'Keep behavior', pathPattern: '', contentPattern: '', body: '# Keep behavior', candidateCount: 10, appliedCount: 0 }];
const key = (cfg = config, definitions = rules, workspace = 'workspace-1') => selectionContextKey(workspace, cfg, definitions);

test('a confirmed mapping is invalidated by source, destination, or extraction changes', () => {
  for (const change of [{ root: '/other' }, { queuePath: '/new/queue.jsonl' }, { rulesPath: '/rules/v2' }, { maxFileBytes: 1024 }]) {
    assert.notEqual(key({ ...config, ...change }), key(), JSON.stringify(change));
  }
  assert.notEqual(key(config, rules, 'workspace-2'), key());
});

test('in-place edits of any rule content require selection review again', () => {
  for (const field of ['id', 'title', 'summary', 'pathPattern', 'contentPattern', 'body']) {
    assert.notEqual(key(config, [{ ...rules[0], [field]: 'changed' }]), key(), field);
  }
  assert.notEqual(key(config, []), key());
});

test('LLM selection, execution limits, prices and statistics preserve checked files', () => {
  assert.equal(key({ ...config, llmConnectionId: 'new', endpoint: 'https://example.com', deployment: 'model', credentialSet: true, maxAttempts: 3 }), key());
  for (const change of [{ concurrency: 1 }, { concurrency: 10 }, { maxAttempts: 1 }, { maxTurns: 20 }, { maxOutputTokens: 16384 }, { timeoutSeconds: 900 }, { maxCostUSD: 2 }, { inputPricePerMillion: 3 }, { cachedInputPricePerMillion: 0.3 }, { outputPricePerMillion: 10 }]) {
    assert.equal(key({ ...config, ...change }), key(), JSON.stringify(change));
  }
  assert.equal(key(config, [{ ...rules[0], candidateCount: 50, appliedCount: 20 }]), key());
  const second = { ...rules[0], id: 'R019', contentPattern: 'StorageSession' };
  assert.equal(key(config, [...rules, second]), key(config, [second, ...rules]));
});


test('changing selected rules invalidates extraction while reordering exclusions does not', () => {
  assert.notEqual(key({ ...config, excludedRuleIds: ['R001'] }), key());
  assert.equal(key({ ...config, excludedRuleIds: [] }), key());
  assert.equal(key({ ...config, excludedRuleIds: ['R019', 'R001'] }), key({ ...config, excludedRuleIds: ['R001', 'R019', 'R001'] }));
});
