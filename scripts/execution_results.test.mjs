import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const requireFrontend = createRequire(new URL('../frontend/package.json', import.meta.url));
const React = requireFrontend('react');
const { renderToStaticMarkup } = requireFrontend('react-dom/server');
const bundle = await build({
  absWorkingDir: frontend,
  stdin: { contents: `
    export { ResultsPanel, resultChanges, ChangeReport, ResultCode } from "./src/ResultsPanel";
    export { ExecutionResultsPanel, executionLabel } from "./src/ExecutionResultsPanel";
    export { emptyState, emptyUsage, normalizeState } from "./src/types";
    export { api as testApi } from "./src/bridge";
  `, resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'no-native-connection', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/(?:src\/)?bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /.*/, namespace: 'test' }, () => ({ contents: `export const api = new Proxy({}, {
      get(target, key) { if (key in target) return target[key]; throw new Error("Native API must not run in this test: " + String(key)); }
    });` }));
  } }],
});
function load(react = React) {
  const module = { exports: {} };
  new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(
    id => id === 'react' ? react : requireFrontend(id), module, module.exports);
  return module.exports;
}
const { ResultsPanel, ExecutionResultsPanel, executionLabel, resultChanges, ChangeReport, ResultCode, emptyState, emptyUsage, normalizeState } = load();
const noOp = () => {};
const task = (file, status, extra = {}) => ({ file, status, rules: [], rulesApplied: [], history: [], attempts: 0, note: '', inputHash: '', updatedAt: '', ...extra });
const run = (id, status = 'completed', targetCount = 2) => ({ id, status, targetCount, startedAt: '2026-09-21T03:00:00Z', finishedAt: status === 'running' ? '' : '2026-09-21T03:01:00Z', error: '' });
const stateOf = (tasks, extra = {}) => normalizeState({ ...emptyState, activeWorkspaceId: 'workspace', tasks, worktree: '/worktree', ...extra });
const defaultTasks = [
  task('current/done.js', 'done', { canDiscardChanges: true }), task('current/unchanged.js', 'skipped'),
  task('current/pending.js', 'pending'), task('previous/done.js', 'done'), task('previous/unchanged.js', 'skipped'),
  task('outside/failed.js', 'failed'),
];
const targets = new Set(defaultTasks.slice(0, 3).map(item => item.file));
function propsFor(extra = {}) {
  const state = stateOf(defaultTasks);
  return {
    state, selectedFile: 'current/done.js', busy: false, usable: true, canSetup: true,
    detailTab: 'history', onSelectFile: noOp, onDetailTabChange: noOp, onStop: noOp, onExport: noOp,
    onRetry: noOp, onDiscard: noOp, onOpenWorktree: noOp, onSetup: noOp,
    execution: { id: 'selected-run', targetFiles: targets, liveState: state, historical: false }, ...extra,
  };
}
const render = props => renderToStaticMarkup(React.createElement(ResultsPanel, propsFor(props)));
const fileList = html => html.slice(html.indexOf('<section class="results-files"'), html.indexOf('<div class="results-splitter'));

// A small hook harness exercises the actual event handlers and wrapper selection
// without a DOM, native calls, timers, or an LLM. SSR above remains real React.
function harness() {
  const cells = [];
  let cursor = 0;
  let effects = [];
  const hooks = { ...React,
    useState(initial) {
      const index = cursor++;
      if (!(index in cells)) cells[index] = typeof initial === 'function' ? initial() : initial;
      return [cells[index], next => { cells[index] = typeof next === 'function' ? next(cells[index]) : next; }];
    },
    useRef(initial) {
      const index = cursor++;
      if (!(index in cells)) cells[index] = { current: initial };
      return cells[index];
    },
    useMemo: factory => factory(), useCallback: callback => callback,
    useEffect: effect => { effects.push(effect); }, useLayoutEffect: noOp, useId: () => 'test-id',
  };
  const module = load(hooks);
  return {
    module,
    render(component, props) { cursor = 0; effects = []; return component(props); },
    async effects() {
      const cleanup = effects.map(effect => effect()).filter(item => typeof item === 'function');
      await new Promise(resolve => setImmediate(resolve));
      return () => cleanup.forEach(fn => fn());
    },
  };
}
function elements(node) {
  if (Array.isArray(node)) return node.flatMap(elements);
  if (!node || typeof node !== 'object' || !node.props) return [];
  return [node, ...elements(node.props.children)];
}
function textOf(node) {
  if (Array.isArray(node)) return node.map(textOf).join('');
  if (node == null || typeof node === 'boolean') return '';
  return typeof node === 'object' ? textOf(node.props?.children) : String(node);
}
function button(tree, text) {
  const found = elements(tree).find(node => node.type === 'button' && textOf(node) === text);
  assert.ok(found, `button ${text} exists`);
  return found;
}

test('selected-run completed rows stay visible while earlier completions fold and unrelated failures stay out', () => {
  const html = render();
  const list = fileList(html);
  const names = [...list.matchAll(/aria-label="([^"]+)の内容を表示"/g)].map(match => match[1]);
  assert.deepEqual(names, ['current/pending.js', 'current/done.js', 'current/unchanged.js']);
  assert.match(list, /完了済み<\/span><b>2<\/b>/);
  assert.match(list, /task-completed-toggle" aria-expanded="false"/);
  assert.doesNotMatch(list, /previous\/(?:done|unchanged)\.jsの内容を表示|outside\/failed\.js/);
  assert.match(html, /試行履歴/);
});

test('run progress counts only selected targets even when their later queue state is excluded', () => {
  const tasks = defaultTasks.map(item => ({ ...item, excluded: true }));
  const state = stateOf(tasks);
  const html = render({ state, execution: { id: 'selected-run', targetFiles: targets, liveState: state, historical: false } });
  assert.match(html, /aria-valuemax="3" aria-valuenow="2" aria-valuetext="2 \/ 3 ファイル"/);
  assert.match(html, /results-progress-count">2 \/ 3<span> ファイル<\/span><b>66%/);
  assert.match(html, /filter-done[^>]*>[\s\S]*?完了<b>1<\/b>/);
  assert.match(html, /filter-skipped[^>]*>[\s\S]*?変更不要<b>1<\/b>/);
  assert.match(html, /filter-attention[^>]*>[\s\S]*?要確認・失敗<b>0<\/b>/);
  assert.doesNotMatch(html, /results-excluded-toggle/);
});

test('a historical result is read-only and explains why it cannot mutate the current workspace', () => {
  const state = stateOf(defaultTasks);
  const html = render({ execution: { id: 'past-run', targetFiles: targets, liveState: state, historical: true } });
  const footer = html.match(/<footer class="results-detail-footer">([\s\S]*?)<\/footer>/)[1];
  assert.match(footer, /選択した実行時点の記録/);
  assert.match(footer, /<button class="results-text-button" disabled="" title="過去の実行は閲覧専用/);
  assert.match(footer, /<button class="results-text-button is-danger" disabled=""/);
  assert.match(html, /<button type="button" class="task-list-retry"[^>]*disabled=""/);
  assert.match(html, /過去の実行時点の内容は変更内容から確認できます/);
});

test('latest completed snapshots respect the live runner lock and live retry queue', () => {
  const state = stateOf(defaultTasks);
  const liveState = stateOf(defaultTasks, { running: true });
  const locked = render({ state, execution: { id: 'selected-run', targetFiles: targets, liveState, historical: false } });
  assert.match(locked, /<button class="results-text-button" disabled="" title="実行中は再試行を追加できません"/);
  assert.match(locked, /<button class="results-text-button is-danger" disabled=""/);
  assert.match(locked, /<button class="results-button" disabled="">[^]*?出力/);
  const queued = stateOf(defaultTasks.map(item => item.file === 'current/done.js' ? { ...item, status: 'pending', resumeRequested: true } : item));
  const html = render({ state, execution: { id: 'selected-run', targetFiles: targets, liveState: queued, historical: false } });
  assert.match(html, /次の実行の再試行に追加済み/);
  assert.match(html, /disabled="" title="このファイルは次の処理対象です"/);
});

test('export sends the selected execution ID, including historical exports', () => {
  for (const historical of [false, true]) {
    const h = harness();
    const exported = [];
    const props = propsFor({ onExport: id => exported.push(id) });
    props.execution = { ...props.execution, id: 'chosen-run', historical };
    const tree = h.render(h.module.ResultsPanel, props);
    const exportButton = button(tree, '出力');
    assert.equal(exportButton.props.disabled, false);
    exportButton.props.onClick();
    assert.deepEqual(exported, ['chosen-run']);
  }
});

test('a stale or historical running snapshot cannot stop a different live execution', () => {
  const state = stateOf(defaultTasks, { running: true });
  for (const [historical, liveRunning] of [[true, true], [true, false], [false, false]]) {
    const h = harness();
    const props = propsFor({ state, execution: { id: 'selected-run', targetFiles: targets, historical, liveState: stateOf(defaultTasks, { running: liveRunning }) } });
    const tree = h.render(h.module.ResultsPanel, props);
    assert.equal(elements(tree).find(node => node.type === 'button' && textOf(node) === '停止'), undefined);
  }
  const h = harness();
  let stopped = 0;
  const props = propsFor({ state, onStop: () => { stopped++; }, execution: { id: 'selected-run', targetFiles: targets, historical: false, liveState: state } });
  const stop = button(h.render(h.module.ResultsPanel, props), '停止');
  assert.equal(stop.props.disabled, false);
  stop.props.onClick();
  assert.equal(stopped, 1);
  props.execution.liveState = { ...state, readOnly: true };
  assert.equal(button(h.render(h.module.ResultsPanel, props), '停止').props.disabled, true);
});

test('execution selector describes dates, state, and target counts and omits itself without runs', () => {
  const runs = [run('first', 'stopped', 8), run('latest', 'completed', 3)];
  const html = renderToStaticMarkup(React.createElement(ExecutionResultsPanel, propsFor({ state: stateOf([], { executionRuns: runs }) })));
  assert.match(html, /実行履歴/);
  assert.match(html, /最新の実行 · 2026\/09\/21 [0-9:]+ · 終了 · 3 ファイル/);
  assert.match(html, /<option value="first">2026\/09\/21 [0-9:]+ · 停止 · 8 ファイル/);
  assert.match(html, /実行結果を読み込み中/);
  const empty = renderToStaticMarkup(React.createElement(ExecutionResultsPanel, propsFor({ state: stateOf([]) })));
  assert.doesNotMatch(empty, /id="execution-run"/);
  assert.match(empty, /実行結果はまだありません/);
  assert.match(executionLabel(run('running', 'running', 5)), /実行中 · 5 ファイル$/);
  assert.match(executionLabel(run('failed', 'failed', 2)), /エラー · 2 ファイル$/);
});

test('latest selection follows a new run while explicitly selected past execution remains historical', async () => {
  const h = harness();
  const first = run('first');
  const second = run('second');
  const third = run('third');
  const snapshots = new Map([first, second, third].map(item => [item.id, {
    run: item, state: stateOf(defaultTasks), targetFiles: [...targets],
  }]));
  const requested = [];
  h.module.testApi.GetExecutionRun = async id => { requested.push(id); return snapshots.get(id); };
  let props = propsFor({ state: stateOf(defaultTasks, { executionRuns: [first] }) });
  h.render(h.module.ExecutionResultsPanel, props);
  let cleanup = await h.effects();
  let tree = h.render(h.module.ExecutionResultsPanel, props);
  let panel = elements(tree).find(node => node.type === h.module.ResultsPanel);
  assert.equal(panel.props.execution.id, 'first');
  assert.equal(panel.props.execution.historical, false);
  cleanup();

  props = { ...props, state: stateOf(defaultTasks, { executionRuns: [first, second] }) };
  tree = h.render(h.module.ExecutionResultsPanel, props);
  assert.equal(elements(tree).find(node => node.type === 'select').props.value, '');
  assert.equal(elements(tree).find(node => node.type === h.module.ResultsPanel), undefined, 'old snapshot is not temporarily shown as the new run');
  cleanup = await h.effects();
  tree = h.render(h.module.ExecutionResultsPanel, props);
  panel = elements(tree).find(node => node.type === h.module.ResultsPanel);
  assert.equal(panel.props.execution.id, 'second');
  assert.equal(panel.props.execution.historical, false);
  cleanup();

  elements(tree).find(node => node.type === 'select').props.onChange({ target: { value: 'first' } });
  h.render(h.module.ExecutionResultsPanel, props);
  cleanup = await h.effects();
  tree = h.render(h.module.ExecutionResultsPanel, props);
  panel = elements(tree).find(node => node.type === h.module.ResultsPanel);
  assert.equal(panel.props.execution.id, 'first');
  assert.equal(panel.props.execution.historical, true);
  assert.deepEqual([...panel.props.execution.targetFiles], [...targets]);
  assert.equal(panel.props.execution.liveState, props.state);
  cleanup();

  props = { ...props, state: stateOf(defaultTasks, { executionRuns: [first, second, third] }) };
  tree = h.render(h.module.ExecutionResultsPanel, props);
  panel = elements(tree).find(node => node.type === h.module.ResultsPanel);
  assert.equal(elements(tree).find(node => node.type === 'select').props.value, 'first');
  assert.equal(panel.props.execution.id, 'first');
  assert.equal(panel.props.execution.historical, true, 'a past file snapshot never becomes current on a later run');
  assert.deepEqual(requested, ['first', 'second', 'first']);
});

test('rule provenance compares execution IDs rather than the latest attempt for each file', () => {
  const old = { id: 'old-attempt', executionId: 'past-run', outcome: 'done' };
  const oldFix = { id: 'old-fix', ruleId: 'R001', ruleTitle: 'Old fix', change: 'Keep accepted work', status: 'fixed', sourceAttemptId: old.id };
  assert.equal(resultChanges([old], -1, { cumulative: true, changes: [oldFix] }, 'selected-run')[0].origin, 'previous');
  const current1 = { id: 'current-1', executionId: 'selected-run', outcome: 'done' };
  const current2 = { id: 'current-2', executionId: 'selected-run', outcome: 'done' };
  const currentFix = (attempt, line) => ({ ...oldFix, id: attempt.id, sourceAttemptId: attempt.id, lineRanges: [{ beforeStart: line, beforeEnd: line, afterStart: line, afterEnd: line }] });
  const detail = { cumulative: true, changes: [oldFix, currentFix(current1, 1), currentFix(current2, 3)] };
  const changes = resultChanges([old, current1, current2], -1, detail, 'selected-run');
  assert.deepEqual(changes.map(item => item.origin), ['previous', 'latest', 'latest']);
  assert.equal(resultChanges([{ ...current1, changes: [currentFix(current1, 1)] }, current2], 0, undefined, 'selected-run')[0].origin, 'latest');
  assert.equal(detail.changes[0].origin, undefined);
  const report = renderToStaticMarkup(React.createElement(ChangeReport, { changes, cumulative: true, onOpenRule: noOp }));
  assert.match(report, /data-change-origin="previous"/);
  assert.match(report, /data-change-origin="latest"/);
  const diff = '@@ -1,3 +1,3 @@\n-old\n+first\n context\n-old\n+second\n';
  const code = renderToStaticMarkup(React.createElement(ResultCode, { content: diff, isDiff: true, changes }));
  assert.equal((code.match(/data-change-origin="latest"/g) || []).length, 2);
  assert.doesNotMatch(code, /data-change-origin="previous"/);
});

test('a run opens on one of its targets and still lets the user inspect an earlier completed file', async () => {
  const h = harness();
  const selected = run('selected-run');
  const snapshot = { run: selected, state: stateOf(defaultTasks), targetFiles: [...targets] };
  h.module.testApi.GetExecutionRun = async () => snapshot;
  const chosen = [];
  let props = propsFor({ selectedFile: 'previous/done.js', onSelectFile: file => chosen.push(file), state: stateOf(defaultTasks, { executionRuns: [selected] }) });
  h.render(h.module.ExecutionResultsPanel, props);
  const cleanup = await h.effects();
  let tree = h.render(h.module.ExecutionResultsPanel, props);
  let panel = elements(tree).find(node => node.type === h.module.ResultsPanel);
  assert.equal(panel.props.selectedFile, 'current/done.js');
  panel.props.onSelectFile('previous/done.js');
  props = { ...props, selectedFile: 'previous/done.js' };
  tree = h.render(h.module.ExecutionResultsPanel, props);
  panel = elements(tree).find(node => node.type === h.module.ResultsPanel);
  assert.equal(panel.props.selectedFile, 'previous/done.js');
  assert.deepEqual(chosen, ['previous/done.js']);
  cleanup();
});
