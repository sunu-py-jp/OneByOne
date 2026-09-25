import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

// Render the actual result panel without a browser or a native/LLM connection.
const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const requireFrontend = createRequire(new URL('../frontend/package.json', import.meta.url));
const { createElement } = requireFrontend('react');
const { renderToStaticMarkup } = requireFrontend('react-dom/server');
const bundle = await build({
  absWorkingDir: frontend,
  stdin: { contents: 'export { ResultsPanel } from "./src/ResultsPanel"; export { emptyState, emptyUsage, normalizeState } from "./src/types";', resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'no-native-connection', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /.*/, namespace: 'test' }, () => ({ contents: 'export const api = new Proxy({}, { get() { throw new Error("Native API must not run in this test"); } });' }));
  } }],
});
const module = { exports: {} };
new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(requireFrontend, module, module.exports);
const { ResultsPanel, emptyState, emptyUsage, normalizeState } = module.exports;
const noOp = () => {};
const review = (id, verdict, extra = {}) => ({
  id, candidateId: `candidate-${id}`, baseHash: 'before-hash', candidateHash: `after-${id}`, planRevision: 1,
  verdict, summary: verdict === 'passed' ? '全ルールを確認しました。' : '通知の実行順序を確認してください。',
  assessments: [{ ruleId: 'R101', status: verdict === 'passed' ? 'satisfied' : 'violated', reason: '保存完了後に通知します。' }],
  issues: [], usage: emptyUsage, startedAt: '2026-09-15T10:00:00Z', finishedAt: '2026-09-15T10:00:10Z', ...extra,
});
function render(reviews, { tab = 'checks', running = false, busy = false, readOnly = false, usable = true, taskChanges = {}, extraTasks = [] } = {}) {
  const task = { file: 'src/dispatch.js', rules: ['R101'], status: running ? 'running' : 'done', attempts: 1, rulesApplied: ['R101'], note: '', inputHash: '', updatedAt: '', history: [{ id: 'attempt-1', number: 1, startedAt: '', finishedAt: '', outcome: running ? 'running' : 'done', note: '', rulesApplied: ['R101'], checks: [], usage: emptyUsage, diffPath: '', commit: '', reviews }] };
  Object.assign(task, taskChanges);
  const state = normalizeState({ ...emptyState, tasks: [task, ...extraTasks], running, readOnly, phase: running ? 'reviewing' : 'idle', currentFile: running ? task.file : '' });
  return renderToStaticMarkup(createElement(ResultsPanel, { state, busy, usable, canSetup: true, selectedFile: task.file, detailTab: tab, onSelectFile: noOp, onDetailTabChange: noOp, onStop: noOp, onExport: noOp, onRetry: noOp, onDiscard: noOp, onOpenWorktree: noOp, onSetup: noOp }));
}

test('a corrected candidate retains both independent reviews without a stale tab warning', () => {
  const reviews = [review('first', 'needs_changes'), review('second', 'passed')];
  for (const tab of ['checks', 'history']) {
    const html = render(reviews, { tab });
    assert.equal((html.match(/class="results-check results-review /g) || []).length, 2);
    assert.match(html, /独立レビュー 1/);
    assert.match(html, /独立レビュー 2/);
    assert.match(html, /要修正/);
    assert.match(html, /合格/);
    assert.doesNotMatch(html, /results-tab-alert/);
    assert.doesNotMatch(html, /<details[^>]*results-review[^>]*\sopen/);
  }
});

test('current review issues show the rule and code position as text, not executable markup', () => {
  const html = render([review('first', 'needs_changes', { issues: [{ ruleId: 'R101', location: 'notifyCustomer()', lineBasis: 'after', excerpt: '<script>alert("review")</script>', reason: '保存前に通知しています。', requestedChange: '保存完了を待ってから通知してください。' }] })]);
  assert.match(html, /results-tab-alert/);
  assert.match(html, /変更後 · notifyCustomer\(\)/);
  assert.match(html, /保存前に通知しています。/);
  assert.match(html, /保存完了を待ってから通知してください。/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
});

test('an in-flight review renders as checking with empty Go slices and incomplete usage', () => {
  const html = render([review('first', 'running', { assessments: null, issues: null, usage: null, finishedAt: '' })], { running: true });
  assert.match(html, /独立レビュー中/);
  assert.match(html, /確認中/);
  assert.doesNotMatch(html, /results-tab-alert/);
  assert.doesNotMatch(html, /NaN/);
});

test('a current hold explains this execution above the previous attempt', () => {
  const html = render([], { taskChanges: { status: 'needs_human', note: '今回 20 / 上限 20 ターン。再実行は0から開始します。' } });
  assert.match(html, /results-explanation attention/);
  assert.match(html, /今回 20 \/ 上限 20 ターン。再実行は0から開始します。/);
});

test('discard appears after retry and remains available after an unchanged rerun', () => {
  for (const status of ['done', 'skipped', 'pending']) {
    const html = render([], { taskChanges: { status, canDiscardChanges: true } });
    const footer = html.match(/<footer class="results-detail-footer">([\s\S]*?)<\/footer>/)[1];
    assert.ok(footer.indexOf('再試行に追加') < footer.indexOf('変更破棄'));
    assert.match(footer, /<button class="results-text-button is-danger">/);
  }
});

test('an unchanged retry with retained changes shows complete while preserving the attempt outcome', () => {
  const history = [
    { id: 'first', number: 1, outcome: 'done', note: '修正を保存しました', commit: 'accepted', checks: [], rulesApplied: ['R101'], usage: emptyUsage },
    { id: 'retry', number: 2, outcome: 'skipped', note: '既に要件を満たしています', commit: '', checks: [], rulesApplied: ['R101'], usage: emptyUsage },
  ];
  for (const status of ['done', 'skipped']) {
    const html = render([], { tab: 'history', taskChanges: { status, canDiscardChanges: true, attempts: 2, history } });
    const header = html.match(/<header class="results-detail-heading">([\s\S]*?)<\/header>/)[1];
    const footer = html.match(/<footer class="results-detail-footer">([\s\S]*?)<\/footer>/)[1];
    assert.match(header, /status-done/);
    assert.doesNotMatch(header, /status-skipped/);
    assert.match(footer, /追加修正なし・以前の修正を保持して完了/);
    assert.match(html, /2 回目<\/strong><span class="results-task-status status-skipped"/);
    assert.match(html, /既に要件を満たしています/);
  }
  const clean = render([], { taskChanges: { status: 'skipped', canDiscardChanges: false, history: [history[1]] } });
  assert.match(clean, /変更不要として終了/);
  assert.doesNotMatch(clean, /以前の修正を保持して完了/);
});

test('discard is disabled without effective changes, while running, or without write access', () => {
  for (const options of [{ taskChanges: { canDiscardChanges: false } }, { running: true }, { busy: true }, { readOnly: true }, { usable: false }]) {
    const html = render([], { taskChanges: { canDiscardChanges: true }, ...options });
    assert.match(html, /<button class="results-text-button is-danger" disabled="">/);
  }
});

test('discard records appear separately from attempts and current waiting note replaces old success', () => {
  const html = render([], { tab: 'history', taskChanges: {
    status: 'pending', canDiscardChanges: false, note: '変更を破棄し、待機中に戻しました。',
    discards: [{ id: 'discard-1', state: 'done', throughAttempt: 1, startedAt: '', finishedAt: '', commit: 'abcdef1234567890' }],
  } });
  assert.match(html, /変更を破棄し、待機中に戻しました。/);
  assert.doesNotMatch(html, /表示中の試行は変更破棄前の記録です/);
  assert.match(html, /破棄済み/);
  assert.match(html, /以前の試行は記録として残っています/);
  assert.match(html, /1 回目/);
  assert.doesNotMatch(html, /2 回目/);
  assert.match(html, /abcdef1234/);
});

test('result navigation uses the compact shared list without selection checkboxes or rule columns', () => {
  const html = render([], { taskChanges: { status: 'pending', resumeRequested: true } });
  const list = html.slice(html.indexOf('<section class="results-files"'), html.indexOf('<div class="results-splitter'));
  assert.match(list, /class="task-file-list/);
  assert.match(list, /class="task-list-row is-selected/);
  assert.match(list, /class="target-file-directory">src\//);
  assert.match(list, /<strong>dispatch\.js<\/strong>/);
  assert.match(list, /status-retry/);
  assert.match(list, /再試行/);
  assert.doesNotMatch(list, /type="checkbox"/);
  assert.doesNotMatch(list, /results-rule-chips|results-file-table|適用ルール<\/th>/);
  assert.match(html, /class="results-task-status status-retry"/);
});

test('completed results initially fold below active files while their selected detail remains accessible', () => {
  const html = render([], { extraTasks: [{ file: 'src/next.js', status: 'pending', history: [], rules: [], rulesApplied: [], attempts: 0 }] });
  const list = html.slice(html.indexOf('<section class="results-files"'), html.indexOf('<div class="results-splitter'));
  assert.match(list, /<strong>next\.js<\/strong>/);
  assert.match(list, /task-completed-toggle" aria-expanded="false"/);
  assert.match(list, /class="task-completed-boundary"/);
  assert.match(list, /完了済み/);
  assert.doesNotMatch(list, /<strong>dispatch\.js<\/strong>/);
  assert.ok(list.indexOf('next.jsの内容を表示') < list.indexOf('task-completed-toggle'));
  assert.doesNotMatch(list, /task-completed-footer/);
  assert.match(html, /id="result-file-detail"[^>]*aria-label="src\/dispatch\.jsの詳細"/);
  assert.match(html, /status-done/);
});
