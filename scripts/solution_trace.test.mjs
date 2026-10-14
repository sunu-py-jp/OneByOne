import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const requireFrontend = createRequire(new URL('../frontend/package.json', import.meta.url));
const React = requireFrontend('react');
const bundle = await build({ absWorkingDir: frontend, stdin: { contents: `
  export * from './src/SolutionTracePanel';
  export { api as testApi } from './src/bridge';
`, resolveDir: frontend }, bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'native-stub', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/(?:src\/)?bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /.*/, namespace: 'test' }, () => ({ contents: 'export const api = {};' }));
  } }],
});
const scope = { workspaceId: 'workspace', executionId: 'execution', solutionRunId: 'solution', running: false };
const row = (sequence, extra = {}) => ({ sequence, iteration: 0, session: 1, turn: 1, phase: 'repair', kind: 'tool', tool: 'read_file', callId: `call-${sequence}`, status: 'completed', summary: 'Program.vb を読む', startedAt: '2026-10-14T01:02:03Z', finishedAt: '', argumentsBytes: 24, outputBytes: 40, usage: {}, ...extra });
const entries = (first, last) => Array.from({ length: last - first + 1 }, (_, index) => row(first + index));
const page = (rows, nextOffset = rows.length, total = nextOffset) => ({ entries: rows, nextOffset, total, complete: nextOffset >= total });
const content = (value, extra = {}) => ({ content: value, offset: 0, nextOffset: Buffer.byteLength(value), totalBytes: Buffer.byteLength(value), complete: true, ...extra });
const settle = async () => { for (let i = 0; i < 12; i++) await Promise.resolve(); };

function harness(initial = scope) {
  let props = initial, active, dirty = false, nextTimer = 0, tree;
  const instances = new Map(), timers = new Map(), effects = [];
  const react = { ...React,
    useState(initialValue) {
      const instance = active, index = instance.cursor++;
      if (!(index in instance.cells)) instance.cells[index] = typeof initialValue === 'function' ? initialValue() : initialValue;
      return [instance.cells[index], value => {
        const next = typeof value === 'function' ? value(instance.cells[index]) : value;
        if (!Object.is(instance.cells[index], next)) { instance.cells[index] = next; dirty = true; }
      }];
    },
    useRef(initialValue) { const index = active.cursor++; return active.cells[index] ??= { current: initialValue }; },
    useEffect(effect, deps) {
      const instance = active, index = instance.cursor++;
      const previous = instance.cells[index];
      if (!previous || deps.some((value, i) => !Object.is(value, previous[i]))) {
        instance.cells[index] = deps;
        effects.push(() => { instance.cleanups[index]?.(); instance.cleanups[index] = effect(); });
      }
    },
  };
  const module = { exports: {} };
  new Function('require', 'module', 'exports', 'setTimeout', 'clearTimeout', bundle.outputFiles[0].text)(
    id => id === 'react' ? react : requireFrontend(id), module, module.exports,
    (callback, delay) => { const id = ++nextTimer; timers.set(id, { callback, delay }); return id; },
    id => timers.delete(id),
  );
  const ui = module.exports;
  function walk(node, path, seen) {
    if (!node || typeof node !== 'object') return node;
    if (Array.isArray(node)) return node.map((item, index) => walk(item, `${path}/${item?.key ?? index}`, seen));
    if (typeof node.type === 'function') {
      const identity = `${path}:${node.type.name}:${node.key ?? ''}`;
      seen.add(identity);
      let instance = instances.get(identity);
      if (!instance) { instance = { cells: [], cleanups: [], cursor: 0 }; instances.set(identity, instance); }
      instance.cursor = 0;
      const parent = active; active = instance;
      const result = node.type(node.props);
      active = parent;
      return walk(result, `${identity}/render`, seen);
    }
    return { ...node, props: { ...node.props, children: walk(node.props?.children, `${path}/children`, seen) } };
  }
  function render(nextProps = props) {
    props = nextProps;
    for (let count = 0; count < 20; count++) {
      dirty = false;
      const seen = new Set();
      tree = walk(React.createElement(ui.SolutionTracePanel, props), 'root', seen);
      for (const [identity, instance] of instances) if (!seen.has(identity)) { for (const cleanup of instance.cleanups) cleanup?.(); instances.delete(identity); }
      for (const effect of effects.splice(0)) effect();
      if (!dirty) return tree;
    }
    throw new Error('render did not settle');
  }
  return { ui, timers, render, get tree() { return tree; },
    toggle(node, open = true) { node.props.onToggle({ currentTarget: { open } }); render(); },
    click(node) { node.props.onClick(); render(); },
    timer() { const next = timers.entries().next().value; assert.ok(next, 'a poll timer must exist'); timers.delete(next[0]); assert.equal(next[1].delay, 3000); next[1].callback(); },
    dispose() { for (const instance of instances.values()) for (const cleanup of instance.cleanups) cleanup?.(); instances.clear(); },
  };
}
function nodes(tree, predicate) {
  if (!tree || typeof tree !== 'object') return [];
  if (Array.isArray(tree)) return tree.flatMap(item => nodes(item, predicate));
  return [...(predicate(tree) ? [tree] : []), ...nodes(tree.props?.children, predicate)];
}
const text = tree => Array.isArray(tree) ? tree.map(text).join('') : tree && typeof tree === 'object' ? text(tree.props?.children) : String(tree ?? '');
const details = (h, className) => nodes(h.tree, node => node.type === 'details' && node.props.className === className);
const button = (h, label) => nodes(h.tree, node => node.type === 'button' && text(node).includes(label))[0];
async function openList(h) { h.render(); h.toggle(details(h, 'solution-trace-panel')[0]); await settle(); h.render(); }

test('trace starts closed, fetches only summaries on opening, and loads each payload only when expanded', async () => {
  const h = harness(), calls = [];
  h.ui.testApi.ListSolutionTrace = async (...args) => { calls.push(['list', ...args]); return page([row(1)]); };
  h.ui.testApi.ReadSolutionTrace = async (...args) => {
    calls.push(['read', ...args]);
    if (args[4] === 'output') return content('{"file":"Program.vb","content":"line 1\\nline 2"}');
    if (args[5] === 0) return content('{"message":"日本', { nextOffset: 16384, totalBytes: 16392, complete: false });
    return content('語"}', { offset: 16384, nextOffset: 16392, totalBytes: 16392 });
  };
  h.render(); assert.deepEqual(calls, []);
  h.toggle(details(h, 'solution-trace-panel')[0]); await settle(); h.render();
  assert.deepEqual(calls, [['list', 'workspace', 'execution', 'solution', 0]]);
  h.toggle(details(h, 'solution-trace-row')[0]);
  assert.equal(calls.length, 1, 'opening a row only shows metadata');
  h.toggle(details(h, 'solution-trace-payload')[0]); await settle(); h.render();
  assert.deepEqual(calls[1], ['read', 'workspace', 'execution', 'solution', 1, 'arguments', 0]);
  assert.ok(text(h.tree).includes('{"message":"日本'), 'partial JSON stays raw');
  h.click(button(h, '続きを表示')); await settle(); h.render();
  assert.deepEqual(calls[2], ['read', 'workspace', 'execution', 'solution', 1, 'arguments', 16384], 'use UTF-8 byte offset from the service');
  assert.ok(text(h.tree).includes('日本語'));
  h.toggle(details(h, 'solution-trace-payload')[1]); await settle(); h.render();
  assert.deepEqual(calls[3], ['read', 'workspace', 'execution', 'solution', 1, 'output', 0]);
  assert.ok(nodes(h.tree, node => node.type === 'pre').some(node => text(node) === 'line 1\nline 2'), 'read_file content has readable actual newlines');
  assert.equal(h.timers.size, 0, 'historical results do not poll');
  h.dispose();
});

test('summary paging is explicit and live refresh updates unfinished pages plus a bounded new tail', async () => {
  const h = harness({ ...scope, running: true }), calls = [];
  let revision = 0;
  h.ui.testApi.ListSolutionTrace = async (_workspace, _execution, _run, offset) => {
    calls.push(offset);
    if (offset === 0) return page([row(1, { status: revision ? 'completed' : 'running' }), ...entries(2, 50)], 50, revision ? 101 : 100);
    if (offset === 50) return page(entries(51, 100), 100, revision ? 101 : 100);
    return page([row(101)], 101, 101);
  };
  await openList(h);
  assert.deepEqual(calls, [0]);
  assert.equal(details(h, 'solution-trace-row').length, 50);
  h.click(button(h, 'さらに表示')); await settle(); h.render();
  assert.deepEqual(calls, [0, 50]);
  revision = 1;
  h.timer(); await settle(); h.render();
  assert.deepEqual(calls, [0, 50, 0, 50, 100]);
  assert.equal(details(h, 'solution-trace-row').length, 101);
  assert.equal(text(details(h, 'solution-trace-row')[0]).includes('実行中'), false);
  h.toggle(details(h, 'solution-trace-panel')[0], false);
  assert.equal(h.timers.size, 0, 'closing the panel cancels scheduled polling');
  assert.equal(details(h, 'solution-trace-row').length, 0);
  h.dispose();
});

test('changing workspace, execution or solution closes the panel and discards late list and payload responses', async () => {
  for (const changed of [{ workspaceId: 'other-workspace' }, { executionId: 'other-execution' }, { solutionRunId: 'other-solution' }]) {
    const h = harness(), reads = [];
    let resolveList, resolveBody;
    h.ui.testApi.ListSolutionTrace = async (...args) => {
      reads.push(args);
      if (reads.length === 1) return page([row(1)]);
      return new Promise(resolve => { resolveList = resolve; });
    };
    h.ui.testApi.ReadSolutionTrace = () => new Promise(resolve => { resolveBody = resolve; });
    await openList(h);
    h.toggle(details(h, 'solution-trace-row')[0]);
    h.toggle(details(h, 'solution-trace-payload')[0]);
    h.click(button(h, '一覧を更新'));
    h.render({ ...scope, ...changed });
    assert.equal(details(h, 'solution-trace-panel')[0].props.open, false);
    assert.equal(reads.length, 2, 'new closed scope performs no request');
    resolveList(page([row(9, { summary: 'OLD SUMMARY' })])); resolveBody(content('OLD BODY')); await settle(); h.render();
    assert.doesNotMatch(text(h.tree), /OLD SUMMARY|OLD BODY/);
    h.ui.testApi.ListSolutionTrace = async (...args) => { reads.push(args); return page([row(1, { summary: 'NEW SUMMARY' })]); };
    h.toggle(details(h, 'solution-trace-panel')[0]); await settle(); h.render();
    assert.deepEqual(reads.at(-1), [{ ...scope, ...changed }.workspaceId, { ...scope, ...changed }.executionId, { ...scope, ...changed }.solutionRunId, 0]);
    assert.match(text(h.tree), /NEW SUMMARY/);
    assert.doesNotMatch(text(h.tree), /OLD SUMMARY|OLD BODY/);
    h.dispose();
  }
});

test('closing a payload invalidates its pending response and reopening reads it afresh', async () => {
  const h = harness(), responses = [];
  h.ui.testApi.ListSolutionTrace = async () => page([row(1)]);
  h.ui.testApi.ReadSolutionTrace = () => new Promise(resolve => responses.push(resolve));
  await openList(h); h.toggle(details(h, 'solution-trace-row')[0]); h.toggle(details(h, 'solution-trace-payload')[0]);
  h.toggle(details(h, 'solution-trace-payload')[0], false);
  h.toggle(details(h, 'solution-trace-payload')[0]);
  responses[0](content('stale')); await settle(); h.render();
  assert.doesNotMatch(text(h.tree), /stale/);
  responses[1](content('<script>visible text only</script>')); await settle(); h.render();
  assert.match(text(h.tree), /visible text only/);
  assert.equal(nodes(h.tree, node => node.type === 'script').length, 0);
  h.dispose();
});

test('finished executions label unfinished rows accurately and old empty results remain understandable', async () => {
  const h = harness({ ...scope, running: true });
  h.ui.testApi.ListSolutionTrace = async () => page([row(1, { status: 'running' })]);
  await openList(h); assert.match(text(h.tree), /実行中/);
  h.render(scope); await settle(); h.render();
  assert.match(text(h.tree), /未完了/);
  assert.equal(h.timers.size, 0);
  h.toggle(details(h, 'solution-trace-panel')[0], false);
  h.ui.testApi.ListSolutionTrace = async () => page([]);
  h.toggle(details(h, 'solution-trace-panel')[0]); await settle(); h.render();
  assert.match(text(h.tree), /機能の追加前/);
  h.dispose();
});

test('failed page and payload reads preserve earlier data and retry the same offsets', async () => {
  const h = harness(), bodyOffsets = [];
  let calls = 0;
  h.ui.testApi.ListSolutionTrace = async () => { if (++calls === 2) throw new Error('summary unavailable'); return page([row(1)]); };
  h.ui.testApi.ReadSolutionTrace = async (...args) => {
    bodyOffsets.push(args.at(-1));
    if (bodyOffsets.length === 2) throw new Error('payload unavailable');
    return bodyOffsets.length === 1 ? content('first', { nextOffset: 16384, totalBytes: 16385, complete: false }) : content('last', { offset: 16384, nextOffset: 16385, totalBytes: 16385 });
  };
  await openList(h); h.click(button(h, '一覧を更新')); await settle(); h.render();
  assert.match(text(h.tree), /summary unavailable/); assert.equal(details(h, 'solution-trace-row').length, 1);
  h.click(button(h, '再読み込み')); await settle(); h.render();
  h.toggle(details(h, 'solution-trace-row')[0]); h.toggle(details(h, 'solution-trace-payload')[0]); await settle(); h.render();
  h.click(button(h, '続きを表示')); await settle(); h.render(); assert.match(text(h.tree), /first/);
  h.click(button(h, '再読み込み')); await settle(); h.render();
  assert.deepEqual(bodyOffsets, [0, 16384, 16384]); assert.match(text(h.tree), /firstlast/);
  h.dispose();
});
