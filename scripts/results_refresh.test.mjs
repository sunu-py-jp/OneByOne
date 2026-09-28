import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const requireFrontend = createRequire(new URL('../frontend/package.json', import.meta.url));
const React = requireFrontend('react');
const bundle = await build({ absWorkingDir: frontend,
  stdin: { contents: `export { ResultsPanel, ResultCode } from './src/ResultsPanel'; export { ExecutionResultsPanel } from './src/ExecutionResultsPanel';
    export { TaskFileList } from './src/TaskFileList'; export { useResultSnapshot } from './src/useResultSnapshot';
    export { emptyState, emptyUsage, normalizeState } from './src/types'; export { api } from './src/bridge';`, resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic', external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'result-refresh-stubs', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/(?:src\/)?bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /^bridge$/, namespace: 'test' }, () => ({ contents: 'export const api = {};' }));
    plugin.onResolve({ filter: /^\.\/useResultsSplitter$/ }, () => ({ path: 'splitter', namespace: 'test' }));
    plugin.onLoad({ filter: /^splitter$/, namespace: 'test' }, () => ({ contents: 'export const useResultsSplitter = () => ({ref: null, width: 220, bounds: {min: 100, max: 500}, handleProps: {}});' }));
  } }],
});

// Exercise effect dependencies, cancellation and event state with controlled I/O
// and timers. The scroll element stands in for the persistent DOM container.
function harness() {
  const cells = [], cleanup = new Map(), timers = new Map();
  let cursor = 0, effects = [], nextTimer = 0;
  const effect = (fn, deps) => {
    const i = cursor++, old = cells[i];
    if (!old || !deps || deps.some((value, n) => !Object.is(value, old[n]))) effects.push(() => { cleanup.get(i)?.(); cleanup.set(i, fn()); });
    cells[i] = deps;
  };
  const memo = (fn, deps) => {
    const i = cursor++, old = cells[i];
    if (!old || !deps || deps.some((value, n) => !Object.is(value, old.deps[n]))) cells[i] = { deps, value: fn() };
    return cells[i].value;
  };
  const hooks = { ...React,
    useState(initial) { const i = cursor++; if (!(i in cells)) cells[i] = typeof initial === 'function' ? initial() : initial; return [cells[i], value => { cells[i] = typeof value === 'function' ? value(cells[i]) : value; }]; },
    useRef(initial) { return cells[cursor++] ??= { current: initial }; },
    useMemo: memo, useCallback: (fn, deps) => memo(() => fn, deps), useEffect: effect, useLayoutEffect: effect, useId: () => 'refresh-test',
  };
  const module = { exports: {} };
  class Observer { observe() {} disconnect() {} }
  new Function('require', 'module', 'exports', 'setTimeout', 'clearTimeout', 'ResizeObserver', bundle.outputFiles[0].text)(
    name => name === 'react' ? hooks : requireFrontend(name), module, module.exports,
    fn => { timers.set(++nextTimer, fn); return nextTimer; }, id => timers.delete(id), Observer);
  return { ...module.exports, timers,
    render(component, props) { cursor = 0; effects = []; const tree = component(props); effects.forEach(fn => fn()); return tree; },
    async settle(component, props) { for (let i = 0; i < 3; i++) { this.render(component, props); await flush(); } return this.render(component, props); },
    async tick() { const pending = [...timers.values()]; timers.clear(); pending.forEach(fn => fn()); await flush(); },
    close() { for (const fn of cleanup.values()) fn?.(); },
    reset() { this.close(); cells.length = 0; cleanup.clear(); timers.clear(); },
  };
}
const flush = () => new Promise(resolve => setImmediate(resolve));
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };
const elements = node => Array.isArray(node) ? node.flatMap(elements) : !node?.props ? [] : [node, ...elements(node.props.children)];
const find = (tree, predicate) => { const found = elements(tree).find(predicate); assert.ok(found); return found; };
const task = (h, file = 'src/a.js') => ({ file, status: 'running', rules: ['1'], rulesApplied: [], attempts: 1, note: '', updatedAt: 'unchanged-time', inputHash: '', history: [{
  id: `${file}-attempt`, number: 1, outcome: 'running', note: '', startedAt: '', finishedAt: '', rulesApplied: [], checks: [], changes: [], reviews: [], usage: h.emptyUsage, diffPath: '', commit: '',
}] });
const stateOf = (h, tasks, extra = {}) => h.normalizeState({ ...h.emptyState, activeWorkspaceId: 'workspace', tasks, running: true, worktree: '/copy', ...extra });
const propsFor = (state, extra = {}) => ({ state, selectedFile: 'src/a.js', busy: false, usable: true, canSetup: true, detailTab: 'diff',
  onSelectFile() {}, onDetailTabChange() {}, onStop() {}, onExport() {}, onRetry() {}, onDiscard() {}, onOpenWorktree() {}, onSetup() {}, ...extra });
const detail = (task, after) => ({ task, before: 'original source', after, diff: `@@ -1 +1 @@\n-original source\n+${after}`, cumulative: true, changes: [] });
const run = (id, status) => ({ id, status, targetCount: 1, startedAt: '2026-09-27T00:00:00Z', finishedAt: status === 'running' ? '' : '2026-09-27T00:01:00Z', error: '' });

test('selected file refreshes in-flight changes without a timestamp change and keeps its source view and scroll container', async () => {
  const h = harness(), initial = task(h), pending = deferred(), requests = [];
  let current = detail(initial, 'first candidate');
  h.api.GetFileDetail = async (...args) => { requests.push(args); return requests.length === 2 ? pending.promise : current; };
  let p = propsFor(stateOf(h, [initial]));
  let tree = await h.settle(h.ResultsPanel, p);
  find(tree, node => node.type === 'button' && node.props.children === '変更後').props.onClick();
  tree = h.render(h.ResultsPanel, p);
  const code = find(tree, node => node.type === h.ResultCode);
  assert.equal(code.props.content, 'first candidate');
  const scrollRef = find(tree, node => node.props.className === 'results-detail-scroll').props.ref;
  const scroll = { scrollTop: 340, scrollLeft: 120, scrollTo() { throw new Error('background refresh reset scrolling'); } };
  scrollRef.current = scroll;
  const updated = { ...initial, history: [{ ...initial.history[0], changes: [{ id: 'fix', ruleId: '1', change: '保存完了を待つ', status: 'pending' }], usage: { ...h.emptyUsage, turns: 2 } }] };
  p = { ...p, state: stateOf(h, [updated]) };
  tree = h.render(h.ResultsPanel, p);
  assert.equal(find(tree, node => node.type === h.ResultCode).props.content, 'first candidate', 'a refresh must not unmount the existing code');
  assert.equal(requests.length, 2, 'changes, checks and reviews invalidate the selected detail even if updatedAt is identical');
  current = detail(updated, 'second candidate'); pending.resolve(current);
  tree = await h.settle(h.ResultsPanel, p);
  const refreshed = find(tree, node => node.type === h.ResultCode);
  assert.equal(refreshed.props.content, 'second candidate');
  assert.equal(refreshed.props.view, 'after');
  assert.equal(find(tree, node => node.props.className === 'results-detail-scroll').props.ref, scrollRef);
  assert.deepEqual([scroll.scrollTop, scroll.scrollLeft], [340, 120]);
  h.close();
});

test('a final task transition refreshes the selected result and stops live polling', async () => {
  const h = harness(), initial = task(h), finalRead = deferred();
  let calls = 0;
  h.api.GetFileDetail = async () => ++calls === 1 ? detail(initial, 'in progress') : finalRead.promise;
  let p = propsFor(stateOf(h, [initial]));
  await h.settle(h.ResultsPanel, p);
  assert.equal(h.timers.size, 1);
  const done = { ...initial, status: 'done', history: [{ ...initial.history[0], outcome: 'done', commit: 'accepted', note: '完了', finishedAt: 'finished' }] };
  p = { ...p, state: stateOf(h, [done], { running: false }) };
  let tree = h.render(h.ResultsPanel, p);
  assert.equal(calls, 2);
  assert.ok(elements(tree).some(node => node.type === h.ResultCode));
  finalRead.resolve(detail(done, 'committed result'));
  tree = await h.settle(h.ResultsPanel, p);
  assert.match(find(tree, node => node.type === h.ResultCode).props.content, /committed result/);
  assert.equal(h.timers.size, 0);
  h.close();
});

test('slow sequential reads keep displaying progress across new revisions, retain data on errors and isolate selection keys', async () => {
  const h = harness(), old = deferred(), newest = deferred();
  let calls = 0;
  let input = { key: 'a', revision: '1', enabled: true, poll: false, read: async () => 'first' };
  await h.settle(h.useResultSnapshot, input);
  input = { ...input, revision: '2', read: async () => { calls++; return old.promise; } };
  h.render(h.useResultSnapshot, input);
  input = { ...input, revision: '3', read: async () => { calls++; return newest.promise; } };
  h.render(h.useResultSnapshot, input);
  assert.equal(calls, 1, 'updates must not start overlapping reads for the same file');
  old.resolve('intermediate progress'); await flush();
  let result = h.render(h.useResultSnapshot, input);
  assert.equal(calls, 2);
  assert.equal(result.data, 'intermediate progress', 'a completed same-file read must stay visible while a newer revision is fetched');
  newest.reject(new Error('temporary read failure')); await flush();
  result = h.render(h.useResultSnapshot, input);
  assert.equal(result.data, 'intermediate progress'); assert.equal(result.error, 'temporary read failure');
  const late = deferred();
  input = { ...input, revision: '4', read: () => late.promise };
  h.render(h.useResultSnapshot, input);
  input = { ...input, key: 'another-file', read: async () => 'other file' };
  assert.equal(h.render(h.useResultSnapshot, input).data, undefined);
  await h.settle(h.useResultSnapshot, input);
  late.resolve('wrong file'); await flush();
  assert.equal(h.render(h.useResultSnapshot, input).data, 'other file');
  h.close();
});

test('continuous revision changes during slow reads cannot freeze the displayed result', async () => {
  const h = harness(), pending = [];
  let calls = 0;
  const read = () => { calls++; const request = deferred(); pending.push(request); return request.promise; };
  let input = { key: 'live-file', revision: '0', enabled: true, poll: true, read };
  h.render(h.useResultSnapshot, input);
  for (let revision = 1; revision <= 3; revision++) {
    input = { ...input, revision: String(revision) };
    h.render(h.useResultSnapshot, input);
    assert.equal(calls, revision, 'a revision must not create a parallel read');
    pending[revision - 1].resolve(`progress ${revision}`);
    await flush();
    assert.equal(h.render(h.useResultSnapshot, input).data, `progress ${revision}`);
    assert.equal(calls, revision + 1, 'the most recent revision is fetched after the completed response');
  }
  h.close();
});

test('running detail refreshes with identical state while frozen historical data does not poll', async () => {
  const h = harness();
  let calls = 0;
  let input = { key: 'run/file', revision: 'same', enabled: true, poll: true, read: async () => ++calls };
  await h.settle(h.useResultSnapshot, input);
  await h.tick();
  assert.equal(h.render(h.useResultSnapshot, input).data, 2);
  input = { ...input, key: 'past-run/file', poll: false };
  await h.settle(h.useResultSnapshot, input);
  const frozenCalls = calls;
  await h.tick(); h.render(h.useResultSnapshot, input);
  assert.equal(calls, frozenCalls); assert.equal(h.timers.size, 0);
  h.close();
});

test('execution refresh errors keep the existing results mounted, and completion retrieves the final frozen snapshot', async () => {
  const h = harness(), selected = task(h), running = run('run', 'running');
  let calls = 0;
  const snapshot = status => ({ run: run('run', status), state: stateOf(h, [selected], { running: status === 'running' }), targetFiles: [selected.file] });
  h.api.GetExecutionRun = async () => { if (++calls === 2) throw new Error('temporary snapshot failure'); return snapshot(calls >= 3 ? 'completed' : 'running'); };
  let p = propsFor(stateOf(h, [selected], { executionRuns: [running] }));
  let tree = await h.settle(h.ExecutionResultsPanel, p);
  const panel = find(tree, node => node.type === h.ResultsPanel);
  await h.tick();
  tree = h.render(h.ExecutionResultsPanel, p);
  assert.equal(find(tree, node => node.type === h.ResultsPanel).key, panel.key);
  assert.ok(elements(tree).some(node => node.props.role === 'alert'));
  p = { ...p, state: stateOf(h, [selected], { running: false, executionRuns: [run('run', 'completed')] }) };
  tree = await h.settle(h.ExecutionResultsPanel, p);
  assert.equal(find(tree, node => node.type === h.ResultsPanel).props.state.running, false);
  assert.equal(h.timers.size, 0);
  h.close();
});

test('background task sorting keeps list scroll while explicit selection still reveals the chosen file', () => {
  const h = harness();
  const tasks = Array.from({ length: 50 }, (_, i) => task(h, `src/${i}.js`));
  const parent = h.render(h.TaskFileList, { tasks, selectedFile: tasks[0].file, onSelectFile() {} });
  const rowComponent = find(parent, node => typeof node.type === 'function' && node.props.label === 'ファイル一覧');
  h.reset();
  let props = rowComponent.props;
  let tree = h.render(rowComponent.type, props);
  const scroll = { scrollTop: 0, scrollHeight: 2000, clientHeight: 300 };
  find(tree, node => typeof node.props.ref === 'function').props.ref(scroll);
  h.render(rowComponent.type, props);
  scroll.scrollTop = 420;
  props = { ...props, filePhases: { [tasks[0].file]: 'reviewing' } };
  tree = h.render(rowComponent.type, props);
  assert.equal(scroll.scrollTop, 420, 'a phase-only update must preserve list scroll');
  props = { ...props, tasks: [...tasks].reverse() };
  h.render(rowComponent.type, props);
  assert.equal(scroll.scrollTop, 420, 'a status-sort change must not jump to the selected row');
  props = { ...props, selectedFile: tasks[1].file };
  h.render(rowComponent.type, props);
  assert.ok(scroll.scrollTop > 420, 'an explicit new selection still scrolls into view');
  h.close();
});

test('phase-only polling updates list and selected header without reloading source or changing selection', async () => {
  const h = harness(), current = task(h);
  let calls = 0;
  h.api.GetFileDetail = async () => { calls++; return detail(current, 'candidate'); };
  let p = propsFor(stateOf(h, [current], { filePhases: { [current.file]: 'running' } }));
  let tree = await h.settle(h.ResultsPanel, p);
  const reads = calls;
  const before = find(tree, node => node.type === h.TaskFileList);
  p = { ...p, state: { ...p.state, filePhases: { [current.file]: 'reviewing' } } };
  tree = h.render(h.ResultsPanel, p);
  const after = find(tree, node => node.type === h.TaskFileList);
  assert.equal(after.props.filePhases[current.file], 'reviewing');
  assert.equal(after.props.selectedFile, before.props.selectedFile);
  const heading = find(tree, node => node.props.className === 'results-detail-heading');
  assert.ok(elements(heading).some(node => node.props.label === '独立レビュー中'));
  assert.equal(calls, reads, 'phase-only progress should not refetch unchanged source');
  h.close();
});

test('choosing a held candidate-only location opens its original attempt and after source without affecting unrelated refreshes', async () => {
  const h = harness(), initial = task(h), requests = [];
  const held = { id: 'hold', ruleId: '', status: 'needs_human', change: '', reason: '入力の契約が不足', attributionVersion: 2, lineRanges: [{ beforeStart: 0, beforeEnd: 0, afterStart: 12, afterEnd: 12 }] };
  const selected = { ...initial, status: 'needs_human', history: [{ ...initial.history[0], outcome: 'needs_human', changes: [held] }] };
  const cumulativeHold = { ...held, id: `attempt:${selected.history[0].id}:0:hold`, sourceAttemptId: selected.history[0].id, lineRanges: [] };
  const cumulative = { ...detail(selected, 'accepted source'), changes: [cumulativeHold] };
  const candidate = { ...detail(selected, 'candidate line\n'.repeat(15)), cumulative: false, changes: [held] };
  h.api.GetFileDetail = async (file, index) => { requests.push([file, index]); return index < 0 ? cumulative : candidate; };
  const p = propsFor(stateOf(h, [selected], { running: false }));
  let tree = await h.settle(h.ResultsPanel, p);
  const menu = find(tree, node => typeof node.props.onChoose === 'function' && node.props.changes?.includes(cumulativeHold));
  menu.props.onChoose(cumulativeHold);
  tree = await h.settle(h.ResultsPanel, p);
  const code = find(tree, node => node.type === h.ResultCode);
  assert.deepEqual(requests, [['src/a.js', -1], ['src/a.js', 0]]);
  assert.equal(code.props.view, 'after');
  assert.equal(code.props.focus.line, 12);
  assert.equal(code.props.content, candidate.after);
  h.close();
});
