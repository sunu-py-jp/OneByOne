import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from '../frontend/node_modules/typescript/lib/typescript.js';

async function compile(name) {
  const source = await readFile(new URL(`../frontend/src/${name}.ts`, import.meta.url), 'utf8');
  return ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } }).outputText;
}
const url = source => `data:text/javascript;base64,${Buffer.from(source).toString('base64')}`;
const typesURL = url(await compile('types'));
const previewURL = url((await compile('preview')).replace('"./types"', JSON.stringify(typesURL)));
const preview = await import(previewURL);

test('preview OAuth settings cannot claim authentication or reuse an API key', () => {
  const original = structuredClone(preview.previewState);
  try {
    const connection = preview.previewState.llmConnections.find(item => item.provider === 'azure');
    const state = preview.savePreviewLLMConnection({ ...connection, authMode: 'oauth', credential: 'should-not-be-kept', oauthSignedIn: true, oauthUsername: 'pretend@example.com' });
    const saved = state.llmConnections.find(item => item.id === connection.id);
    assert.equal(saved.credential, '');
    assert.equal(saved.credentialSet, false);
    assert.equal(saved.oauthSignedIn, false);
    assert.equal(saved.oauthUsername, '');
  } finally {
    Object.assign(preview.previewState, original);
  }
});

test('preview checks persist across pages and workspace switches without changing outcomes', () => {
  const before = structuredClone(preview.previewState);
  const file = before.tasks[0].file;
  const selected = preview.selectPreviewTasks([file]);
  assert.deepEqual(selected.tasks.filter(task => !task.excluded).map(task => task.file), [file]);
  assert.deepEqual(selected.tasks.map(({ excluded, ...task }) => task), before.tasks.map(({ excluded, ...task }) => task));
  const other = before.workspaces.find(workspace => workspace.id !== before.activeWorkspaceId);
  preview.selectPreviewWorkspace(other.id);
  const restored = preview.selectPreviewWorkspace(before.activeWorkspaceId);
  assert.deepEqual(restored.tasks.filter(task => !task.excluded).map(task => task.file), [file]);
  const snapshot = structuredClone(preview.previewState);
  assert.throws(() => preview.selectPreviewTasks(['unknown-file']));
  assert.deepEqual(preview.previewState, snapshot);
});

test('preview cumulative results keep accepted changes after a retry with no additional changes', () => {
  const before = structuredClone(preview.previewState);
  try {
    const task = preview.previewState.tasks.find(item => item.status === 'done' && item.history[0].changes.length);
    const first = structuredClone(task.history[0]);
    task.history.push({ ...structuredClone(first), id: 'retry-no-change', number: 2, outcome: 'skipped', commit: '',
      note: '追加の変更箇所はありません。', changes: [{ ...first.changes[0], id: 'rule:R019', location: '', change: '', lineRanges: [], status: 'unchanged', reason: '既に要件を満たしています。' }] });
    task.status = 'skipped';
    const cumulative = preview.previewDetail(task.file, -1);
    assert.equal(cumulative.cumulative, true);
    assert.match(cumulative.before, /StorageSession/);
    assert.match(cumulative.after, /StorageClient/);
    assert.ok(cumulative.diff);
    assert.equal(cumulative.changes.length, 1);
    assert.equal(cumulative.changes[0].status, 'fixed');
    assert.equal(cumulative.changes[0].id, `attempt:${first.id}:0:${first.changes[0].id}`);
    assert.equal(cumulative.changes[0].location, `修正時の位置: ${first.changes[0].location}`);
    const retry = preview.previewDetail(task.file, 1);
    assert.equal(retry.cumulative, false);
    assert.equal(retry.before, retry.after);
    assert.match(retry.before, /StorageClient/);
    assert.equal(retry.diff, '');
    assert.deepEqual(retry.changes, task.history[1].changes.map(item => ({ ...item, sourceAttemptId: task.history[1].id })));
    assert.equal(cumulative.changes[0].sourceAttemptId, first.id);
    assert.match(preview.previewDetail(task.file, 0).before, /StorageSession/);
    assert.equal(preview.previewDetail(task.file, 0).changes[0].location, first.changes[0].location);
    cumulative.changes[0].reason = 'edited outside preview';
    assert.deepEqual(task.history[0], first);
  } finally {
    Object.assign(preview.previewState, before);
  }
});

test('preview cumulative report preserves latest unresolved records alongside earlier accepted work', () => {
  const before = structuredClone(preview.previewState);
  try {
    const task = preview.previewState.tasks.find(item => item.status === 'done' && item.history[0].changes.length);
    const first = structuredClone(task.history[0]);
    const unresolved = { ...first.changes[0], status: 'needs_human', reason: '呼び出し元の判断が必要です。' };
    task.history.push({ ...structuredClone(first), id: 'retry-held', number: 2, outcome: 'needs_human', commit: '', changes: [unresolved] });
    task.status = 'needs_human';
    const cumulative = preview.previewDetail(task.file);
    assert.deepEqual(cumulative.changes.map(item => item.status), ['fixed', 'needs_human']);
    assert.equal(cumulative.changes[1].reason, unresolved.reason);
    assert.deepEqual(cumulative.changes[1].lineRanges, []);
    assert.equal(cumulative.changes[1].location, unresolved.location);
    assert.deepEqual(preview.previewDetail(task.file, 1).changes[0].lineRanges, unresolved.lineRanges);
    assert.notEqual(cumulative.changes[0].id, cumulative.changes[1].id);
    assert.ok(cumulative.diff);
  } finally {
    Object.assign(preview.previewState, before);
  }
});

test('preview separates rejected proposals from cumulative content and validates history indexes', () => {
  const task = preview.previewState.tasks.find(item => item.status === 'failed');
  const cumulative = preview.previewDetail(task.file);
  assert.equal(cumulative.cumulative, true);
  assert.equal(cumulative.before, cumulative.after);
  assert.equal(cumulative.diff, '');
  assert.ok(cumulative.changes.every(item => item.status === 'not_applied'));
  const rejected = preview.previewDetail(task.file, 0);
  assert.equal(rejected.cumulative, false);
  assert.notEqual(rejected.before, rejected.after);
  assert.ok(rejected.diff);
  assert.deepEqual(rejected.changes, task.history[0].changes.map(item => ({ ...item, sourceAttemptId: task.history[0].id })));
  for (const index of [-2, 0.5, 1, NaN]) assert.throws(() => preview.previewDetail(task.file, index));
  assert.throws(() => preview.previewDetail('missing-file'));
  const pending = preview.previewState.tasks.find(item => item.status === 'pending');
  assert.equal(preview.previewDetail(pending.file).diff, '');
  assert.throws(() => preview.previewDetail(pending.file, 0));
});

test('preview discards remove accepted changes from cumulative results without rewriting history', () => {
  const before = structuredClone(preview.previewState);
  try {
    const task = preview.previewState.tasks.find(item => item.status === 'done' && item.history[0].changes.length);
    task.discards = [{ id: 'discard-preview', state: 'done', throughAttempt: 1 }];
    const cumulative = preview.previewDetail(task.file);
    assert.equal(cumulative.before, cumulative.after);
    assert.equal(cumulative.diff, '');
    assert.deepEqual(cumulative.changes, []);
    assert.ok(preview.previewDetail(task.file, 0).diff);
    assert.deepEqual(task.history, before.tasks.find(item => item.file === task.file).history);
  } finally {
    Object.assign(preview.previewState, before);
  }
});

test('preview execution snapshots distinguish new targets, old completion, and a no-change retry', () => {
  const [firstRun, secondRun] = preview.previewState.executionRuns;
  assert.equal(preview.previewState.executionRuns.length, 2);
  const first = preview.previewExecutionRun(firstRun.id);
  const second = preview.previewExecutionRun(secondRun.id);
  assert.deepEqual(first.state.executionRuns.map(run => run.id), [firstRun.id]);
  assert.deepEqual(second.state.executionRuns.map(run => run.id), [firstRun.id, secondRun.id]);
  for (const run of [first, second]) assert.equal(run.run.targetCount, run.targetFiles.length);
  const oldDone = second.state.tasks.find(task => task.file === 'src/services/storage.ts');
  assert.equal(oldDone.status, 'done');
  assert.ok(!second.targetFiles.includes(oldDone.file));
  assert.equal(oldDone.history[0].executionId, firstRun.id);
  assert.ok(first.targetFiles.includes('src/tests/storage.test.ts'));
  assert.ok(!second.targetFiles.includes('src/tests/storage.test.ts'));
  const retry = second.state.tasks.find(task => task.file === 'src/repositories/document.ts');
  assert.ok(second.targetFiles.includes(retry.file));
  assert.equal(retry.status, 'done');
  assert.equal(retry.history.length, 2);
  assert.equal(retry.history[1].outcome, 'skipped');
  assert.equal(retry.history[1].executionId, secondRun.id);
  const oldRetry = first.state.tasks.find(task => task.file === retry.file);
  assert.equal(oldRetry.history.length, 1);
  for (const [file, outcome] of [['src/commands/export.ts', 'done'], ['src/utils/format.ts', 'skipped']]) {
    assert.equal(first.state.tasks.find(task => task.file === file).status, 'pending');
    assert.ok(!first.targetFiles.includes(file));
    assert.ok(second.targetFiles.includes(file));
    const task = second.state.tasks.find(task => task.file === file);
    assert.equal(task.status, outcome);
    assert.equal(task.history[0].executionId, secondRun.id);
  }
  const previousDetail = preview.previewExecutionFileDetail(firstRun.id, 'src/commands/export.ts');
  const currentDetail = preview.previewExecutionFileDetail(secondRun.id, 'src/commands/export.ts');
  assert.equal(previousDetail.diff, '');
  assert.equal(previousDetail.before, previousDetail.after);
  assert.ok(currentDetail.diff);
  assert.notEqual(currentDetail.before, currentDetail.after);
  assert.equal(currentDetail.changes[0].sourceAttemptId, currentDetail.task.history[0].id);
  assert.equal(currentDetail.task.history.find(attempt => attempt.id === currentDetail.changes[0].sourceAttemptId).executionId, secondRun.id);
  const retryDetail = preview.previewExecutionFileDetail(secondRun.id, retry.file);
  assert.ok(retryDetail.diff);
  assert.equal(retryDetail.changes[0].sourceAttemptId, retry.history[0].id);
  assert.equal(preview.previewExecutionFileDetail(secondRun.id, retry.file, 1).diff, '');
  assert.throws(() => preview.previewExecutionFileDetail(firstRun.id, retry.file, 1));
});

test('preview historical results are independent of current state and returned object mutations', () => {
  const before = structuredClone(preview.previewState);
  const runId = before.executionRuns[0].id;
  const frozen = preview.previewExecutionRun(runId);
  try {
    preview.previewState.tasks[0].status = 'failed';
    preview.previewState.tasks[0].history[0].note = 'later edited note';
    preview.previewState.rules[0].overview = 'later edited rule';
    preview.previewState.config.deployment = 'later-model';
    assert.deepEqual(preview.previewExecutionRun(runId), frozen);
    const returned = preview.previewExecutionRun(runId);
    returned.targetFiles.length = 0;
    returned.state.tasks[0].history.length = 0;
    assert.deepEqual(preview.previewExecutionRun(runId), frozen);
    const detail = preview.previewExecutionFileDetail(runId, frozen.state.tasks[0].file);
    assert.equal(detail.task.status, frozen.state.tasks[0].status);
    assert.equal(detail.task.history[0].note, frozen.state.tasks[0].history[0].note);
  } finally {
    Object.assign(preview.previewState, before);
  }
});

test('preview run history is scoped to the active workspace and new workspaces start empty', () => {
  const initialId = preview.previewState.activeWorkspaceId;
  const firstRun = preview.previewState.executionRuns[0].id;
  const other = preview.previewState.workspaces.find(workspace => workspace.id !== initialId && workspace.id !== 'framework-upgrade');
  try {
    const switched = preview.selectPreviewWorkspace(other.id);
    assert.equal(switched.executionRuns.length, 2);
    assert.ok(switched.executionRuns.every(run => run.id !== firstRun));
    assert.throws(() => preview.previewExecutionRun(firstRun));
    const otherResult = preview.previewExecutionRun(switched.executionRuns[0].id);
    assert.equal(otherResult.state.activeWorkspaceId, other.id);
    preview.selectPreviewWorkspace(initialId);
    const created = preview.createPreviewWorkspace('Empty execution example', '/workspace/new-project');
    assert.deepEqual(created.executionRuns, []);
    assert.throws(() => preview.previewExecutionRun(firstRun));
    preview.deletePreviewWorkspace(created.activeWorkspaceId);
    assert.equal(preview.selectPreviewWorkspace(initialId).executionRuns[0].id, firstRun);
    assert.equal(preview.previewExecutionRun(firstRun).state.activeWorkspaceId, initialId);
  } finally {
    preview.selectPreviewWorkspace(initialId);
  }
});

test('preview bridge serves frozen runs while rejecting native execution and exports', async () => {
  const originalWindow = globalThis.window;
  globalThis.window = { location: { search: '?demo=1' } };
  try {
    const source = (await compile('bridge')).replace('"./types"', JSON.stringify(typesURL)).replace('"./preview"', JSON.stringify(previewURL));
    const { api } = await import(url(source));
    const runId = preview.previewState.executionRuns[0].id;
    const result = await api.GetExecutionRun(runId);
    assert.equal(result.run.id, runId);
    assert.ok(result.targetFiles.length);
    const detail = await api.GetExecutionFileDetail(runId, result.targetFiles[0], -1);
    assert.equal(detail.cumulative, true);
    assert.throws(() => api.Start(0), /画面プレビュー/);
    assert.throws(() => api.ExportExecutionReport(runId), /画面プレビュー/);
    await assert.rejects(api.SignInLLMConnection('preview-azure'), /画面プレビュー/);
    await assert.rejects(api.SignOutLLMConnection('preview-azure'), /画面プレビュー/);
    assert.throws(() => api.CancelLLMSignIn(), /画面プレビュー/);
  } finally {
    globalThis.window = originalWindow;
  }
});
