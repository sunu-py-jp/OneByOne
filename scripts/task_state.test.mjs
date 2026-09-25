import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from '../frontend/node_modules/typescript/lib/typescript.js';

const source = await readFile(new URL('../frontend/src/task-state.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } });
const { taskDisplayStatus, taskStatusLabel, compareTaskStatus, isCompletedTask } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

test('retry presentation requires a pending task with a persisted resume request', () => {
  assert.equal(taskStatusLabel({ status: 'pending' }), '未修正');
  assert.equal(taskStatusLabel({ status: 'pending', attempts: 4 }), '未修正');
  assert.equal(taskStatusLabel({ status: 'pending', resumeRequested: true }), '再試行');
  assert.equal(taskStatusLabel({ status: 'done' }), '完了');
  assert.equal(taskStatusLabel({ status: 'skipped' }), '変更不要');
  for (const status of ['running', 'failed', 'needs_human', 'skipped', 'done']) {
    assert.equal(taskDisplayStatus({ status, resumeRequested: true }), status);
  }
});

test('queue presentation prioritizes retries, groups statuses, and orders paths without changing queue state', () => {
  const tasks = [
    { file: 'done.js', status: 'done' },
    { file: 'b.js', status: 'pending' },
    { file: 'skipped.js', status: 'skipped' },
    { file: 'a.js', status: 'pending' },
    { file: 'human.js', status: 'needs_human' },
    { file: 'failed.js', status: 'failed' },
    { file: 'running.js', status: 'running' },
    { file: 'retry.js', status: 'pending', resumeRequested: true },
  ];
  const snapshot = structuredClone(tasks);
  assert.deepEqual([...tasks].sort(compareTaskStatus).map(task => task.file),
    ['retry.js', 'running.js', 'a.js', 'b.js', 'failed.js', 'human.js', 'done.js', 'skipped.js']);
  assert.deepEqual(tasks, snapshot);
  assert.deepEqual(tasks.filter(isCompletedTask).map(task => task.file), ['done.js', 'skipped.js']);
});

test('unchanged retry keeps effective accepted changes complete without promoting failures or discarded work', () => {
  assert.equal(taskDisplayStatus({ status: 'skipped', canDiscardChanges: true }), 'done');
  assert.equal(taskStatusLabel({ status: 'skipped', canDiscardChanges: true }), '完了');
  assert.equal(taskStatusLabel({ status: 'skipped', canDiscardChanges: false }), '変更不要');
  for (const status of ['pending', 'running', 'failed', 'needs_human']) {
    assert.equal(taskDisplayStatus({ status, canDiscardChanges: true }), status);
  }
  assert.equal(taskDisplayStatus({ status: 'pending', resumeRequested: true, canDiscardChanges: true }), 'retry');
});
