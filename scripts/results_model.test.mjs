import test from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const bundle = await build({ entryPoints: [fileURLToPath(new URL('../frontend/src/results-model.ts', import.meta.url))], bundle: true, write: false, platform: 'node', format: 'esm' });
const { summarizeResults, filterResults } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
const task = (file, status, extra = {}) => ({ file, status, excluded: false, rules: [], rulesApplied: [], history: [], attempts: status === 'pending' ? 0 : 1, ...extra });
const summarize = (tasks, running = false, phase = 'idle', error = '') => summarizeResults(tasks, running, phase, error);

test('terminal attention and unchanged files count as processed without calling them successful', () => {
  const result = summarize([task('a', 'done'), task('b', 'skipped'), task('c', 'needs_human')]);
  assert.equal(result.progress, 100);
  assert.equal(result.counts.done, 1);
  assert.equal(result.counts.attention, 1);
  assert.equal(result.runLabel, '処理終了・要確認あり');
});

test('automatic retries are not counted as finished while running, and a stopped retry is not completion', () => {
  const tasks = [task('ok', 'done'), task('retry', 'failed')];
  assert.equal(summarize(tasks, true, 'running').progress, 50);
  assert.equal(summarize(tasks, true, 'running').runLabel, '実行中');
  assert.equal(summarize(tasks).progress, 100);
  assert.equal(summarize(tasks).runLabel, '停止中');
});

test('stopped midway, active recovery, empty selection, preparation, and fatal preflight errors stay distinct', () => {
  assert.equal(summarize([task('a', 'done'), task('b', 'pending')]).runLabel, '停止中');
  assert.equal(summarize([task('a', 'running')]).runLabel, '停止中');
  assert.equal(summarize([task('a', 'pending')]).runLabel, '実行前');
  assert.equal(summarize([]).runLabel, '対象ファイル未選択');
  assert.equal(summarize([]).progress, 0);
  assert.equal(summarize([task('a', 'pending')], true, 'preparing').runLabel, '準備中');
  assert.equal(summarize([task('a', 'pending')], false, 'idle', 'Baseline failed').runLabel, 'エラーで停止');
});

test('excluded history never affects selected-file progress and remains accessible separately', () => {
  const tasks = [task('selected', 'done'), task('excluded-failure', 'failed', { excluded: true })];
  const result = summarize(tasks);
  assert.equal(result.counts.all, 1);
  assert.equal(result.counts.attention, 0);
  assert.equal(result.counts.excluded, 1);
  assert.equal(result.progress, 100);
  assert.equal(result.runLabel, '処理完了');
  assert.deepEqual(filterResults(tasks, new Map(), 'all', '').map(t => t.file), ['selected']);
  assert.deepEqual(filterResults(tasks, new Map(), 'excluded', '').map(t => t.file), ['excluded-failure']);
});

test('search matches actually applied rule IDs and titles, not candidate or rolled-back rules', () => {
  const rules = new Map([['R001', { title: '必須の後処理' }], ['R019', { title: '構造変更' }]]);
  const tasks = [task('src/a.ts', 'done', { rules: ['R001', 'R019'], rulesApplied: ['R001'] }), task('src/b.ts', 'failed', { rules: ['R019'], history: [{ rulesApplied: ['R019'] }] })];
  assert.deepEqual(filterResults(tasks, rules, 'all', 'R001').map(t => t.file), ['src/a.ts']);
  assert.deepEqual(filterResults(tasks, rules, 'all', '後処理').map(t => t.file), ['src/a.ts']);
  assert.deepEqual(filterResults(tasks, rules, 'all', 'R019'), []);
  assert.deepEqual(filterResults(tasks, rules, 'attention', 'SRC/B').map(t => t.file), ['src/b.ts']);
});

test('pre-scan attention with no LLM attempt is shown as an unresolved target, not execution completion', () => {
  const unsupported = task('unsupported.txt', 'needs_human', { attempts: 0 });
  assert.equal(summarize([unsupported]).runLabel, '対象に要確認あり');
  assert.equal(summarize([unsupported]).progress, 100);
  assert.equal(summarize([unsupported, task('next.ts', 'pending')]).runLabel, '対象に要確認あり');
  assert.equal(summarize([unsupported, task('next.ts', 'pending')]).progress, 50);
});

test('explicit retry is separate from unmodified files and removes completed progress without losing history', () => {
  const retried = task('retry.js', 'pending', { resumeRequested: true, attempts: 1, history: [{ outcome: 'done' }] });
  const tasks = [retried, task('fresh.js', 'pending'), task('done.js', 'done')];
  const result = summarize(tasks);
  assert.equal(result.counts.all, 3);
  assert.equal(result.counts.retry, 1);
  assert.equal(result.counts.pending, 1);
  assert.equal(result.counts.done, 1);
  assert.equal(result.counts.settled, 1);
  assert.equal(result.progress, 33);
  assert.equal(result.runLabel, '停止中');
  assert.deepEqual(filterResults(tasks, new Map(), 'retry', '').map(item => item.file), ['retry.js']);
  assert.deepEqual(filterResults(tasks, new Map(), 'pending', '').map(item => item.file), ['fresh.js']);
  assert.equal(retried.history[0].outcome, 'done');
});

test('result ordering follows the shared execution status order without mutating the queue', () => {
  const tasks = [task('last.js', 'done'), task('z/fresh.js', 'pending'), task('held.js', 'needs_human'), task('failed.js', 'failed'),
    task('unchanged.js', 'skipped'), task('active.js', 'running', { resumeRequested: true }),
    task('a/fresh.js', 'pending'), task('retry.js', 'pending', { resumeRequested: true })];
  const originalOrder = tasks.map(item => item.file);
  const ordered = filterResults(tasks, new Map(), 'all', '');
  assert.deepEqual(ordered.map(item => item.file), ['retry.js', 'active.js', 'a/fresh.js', 'z/fresh.js', 'failed.js', 'held.js', 'last.js', 'unchanged.js']);
  assert.deepEqual(tasks.map(item => item.file), originalOrder);
  assert.deepEqual(filterResults(tasks, new Map(), 'running', '').map(item => item.file), ['active.js']);
  assert.deepEqual(filterResults(tasks, new Map(), 'attention', '').map(item => item.file), ['failed.js', 'held.js']);
});

test('unchanged retries with retained changes count and filter as complete while no-change attempts remain intact', () => {
  const retried = task('retried.js', 'skipped', { canDiscardChanges: true, attempts: 2,
    history: [{ outcome: 'done', commit: 'accepted' }, { outcome: 'skipped', commit: '' }] });
  const tasks = [retried, task('initially-clean.js', 'skipped'), task('discarded.js', 'skipped', { canDiscardChanges: false })];
  const result = summarize(tasks);
  assert.equal(result.counts.done, 1);
  assert.equal(result.counts.skipped, 2);
  assert.equal(result.progress, 100);
  assert.deepEqual(filterResults(tasks, new Map(), 'done', '').map(item => item.file), ['retried.js']);
  assert.deepEqual(filterResults(tasks, new Map(), 'skipped', '').map(item => item.file), ['discarded.js', 'initially-clean.js']);
  assert.equal(retried.history[1].outcome, 'skipped');
  assert.equal(retried.history[1].commit, '');
});
