import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from '../frontend/node_modules/typescript/lib/typescript.js';

const source = await readFile(new URL('../frontend/src/workflow-navigation.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } });
const { countReadyTargets, hasReadyLLMConnection, workflowAvailability, workflowBlockReasons } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
const ready = {
  busy: false, running: false, available: true, targetReady: true, setupReady: true,
  connectionReady: true, readOnly: false, selectionCurrent: true, readyCount: 1,
};

test('every blocked navigation path has a reason and no selection disables confirmation', () => {
  const noSelection = { ...ready, selectedCount: 0, readyCount: 0 };
  assert.equal(workflowAvailability(noSelection).review, false);
  assert.match(workflowBlockReasons(noSelection).review, /1つ以上選択/);
  for (const change of [{}, { busy: true }, { available: false }, { running: true }, { targetReady: false, targetError: 'Git error' }, { setupReady: false, setupError: 'Rule error' }, { connectionReady: false }, { readOnly: true }, { selectionCurrent: false }, { readyCount: 0 }, { selectedCount: 0 }]) {
    const state = { ...ready, ...change };
    const availability = workflowAvailability(state);
    const reasons = workflowBlockReasons(state);
    for (const page of Object.keys(availability)) assert.equal(Boolean(reasons[page]), !availability[page]);
  }
  assert.equal(workflowBlockReasons({ ...ready, targetReady: false, targetError: 'Git error' }).run, 'Git error');
  assert.equal(workflowBlockReasons({ ...ready, setupReady: false, setupError: 'Rule error' }).review, 'Rule error');
});

test('completed or unselected targets block returning to confirmation while execution settings remain reachable', () => {
  const destinations = workflowAvailability({ ...ready, readyCount: 0 });
  assert.equal(destinations.review, false);
  assert.equal(destinations.run, true);
  assert.equal(destinations.results, true);
  assert.equal(workflowAvailability(ready).review, true);
});

test('confirmation requires current targets, a valid setup, a connection, and workspace write access', () => {
  for (const condition of [{ selectionCurrent: false }, { setupReady: false }, { targetReady: false }, { connectionReady: false }, { readOnly: true }]) {
    const destinations = workflowAvailability({ ...ready, ...condition });
    assert.equal(destinations.review, false, JSON.stringify(condition));
    assert.equal(destinations.results, true, 'saved results remain accessible');
  }
  const readOnly = workflowAvailability({ ...ready, readOnly: true });
  assert.equal(readOnly.rules, true);
  assert.equal(readOnly.run, true);
});

test('a missing or invalid target stops forward setup while its own page stays reachable', () => {
  const destinations = workflowAvailability({ ...ready, targetReady: false });
  assert.equal(destinations.target, true);
  assert.equal(destinations.rules, false);
  assert.equal(destinations.run, false);
  assert.equal(destinations.review, false);
});

test('execution permits only results, and pending operations block all ordinary navigation', () => {
  assert.deepEqual(workflowAvailability({ ...ready, running: true }), {
    target: false, rules: false, run: false, review: false, results: true, publish: false,
  });
  for (const condition of [{ busy: true }, { available: false }, { busy: true, running: true }]) {
    assert.ok(Object.values(workflowAvailability({ ...ready, ...condition })).every(value => !value));
  }
});

test('publication remains inspectable without an LLM, queue selection or valid current rules', () => {
  const incomplete = { ...ready, connectionReady: false, readyCount: 0, selectedCount: 0, setupReady: false, selectionCurrent: false, readOnly: true };
  assert.equal(workflowAvailability(incomplete).publish, true);
  assert.equal(workflowAvailability({ ...incomplete, running: true }).publish, false);
  assert.match(workflowBlockReasons({ ...incomplete, running: true }).publish, /実行中/);
});

test('new execution counts pending and failed files regardless of past attempts, excluding completed, held, and deselected files', () => {
  const task = (file, status, attempts = 0, excluded = false) => ({ file, status, attempts, excluded });
  const stale = [task('finished.js', 'pending')];
  const reloaded = [task('finished.js', 'done', 1), task('ignored.js', 'pending', 0, true), task('exhausted.js', 'failed', 3), task('held.js', 'needs_human', 1), task('unchanged.js', 'skipped', 1), task('active.js', 'running', 1)];
  assert.equal(countReadyTargets(stale), 1);
  assert.equal(countReadyTargets(reloaded), 1);
  assert.equal(countReadyTargets([...reloaded, task('retry.js', 'failed', 2), task('next.js', 'pending')]), 3);
  // Draft checkbox selection is respected before saving, while reload uses persisted exclusions.
  assert.equal(countReadyTargets(reloaded, new Set(['ignored.js'])), 1);
  assert.equal(countReadyTargets(stale, new Set()), 0);
  assert.equal(countReadyTargets([{ ...task('resume.js', 'pending', 3), resumeRequested: true }]), 1);
  assert.equal(countReadyTargets([{ ...task('resume.js', 'needs_human', 3), resumeRequested: true }]), 0);
});

test('reloaded connection availability rejects removed or incomplete selected connections', () => {
  const connection = { id: 'selected', endpoint: 'https://example.com', deployment: 'model', credentialSet: true };
  const state = { llmConnections: [connection], selectedLLMConnectionId: 'selected' };
  assert.equal(hasReadyLLMConnection(state), true);
  assert.equal(hasReadyLLMConnection({ ...state, llmConnections: [] }), false);
  assert.equal(hasReadyLLMConnection({ ...state, selectedLLMConnectionId: 'removed' }), false);
  for (const change of [{ endpoint: '' }, { deployment: '' }, { credentialSet: false }]) {
    assert.equal(hasReadyLLMConnection({ ...state, llmConnections: [{ ...connection, ...change }] }), false);
  }
});

test('OAuth execution requires a signed-in Azure account instead of an API-key flag', () => {
  const connection = { id: 'oauth', provider: 'azure', authMode: 'oauth', endpoint: 'https://example.openai.azure.com', deployment: 'model',
    oauthTenantId: 'a', oauthClientId: 'b', oauthSignedIn: true, credentialSet: false };
  const readyState = changes => ({ llmConnections: [{ ...connection, ...changes }], selectedLLMConnectionId: 'oauth' });
  assert.equal(hasReadyLLMConnection(readyState({})), true);
  for (const change of [{ provider: 'openai' }, { oauthSignedIn: false, credentialSet: true }, { oauthSignedIn: undefined, credentialSet: true }, { oauthTenantId: '' }, { oauthClientId: '' }]) {
    assert.equal(hasReadyLLMConnection(readyState(change)), false, JSON.stringify(change));
  }
});
