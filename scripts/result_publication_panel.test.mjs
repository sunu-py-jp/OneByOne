import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const requireFrontend = createRequire(new URL('../frontend/package.json', import.meta.url));
const React = requireFrontend('react');
const bundle = await build({
  absWorkingDir: frontend,
  stdin: { contents: 'export { ResultPublicationPanel } from "./src/ResultPublicationPanel"; export { api } from "./src/bridge";', resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'publication-native-stubs', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/(?:src\/)?bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /^bridge$/, namespace: 'test' }, () => ({ contents: 'export const api = {};' }));
    plugin.onResolve({ filter: /^\.\/useResultsSplitter$/ }, () => ({ path: 'splitter', namespace: 'test' }));
    plugin.onLoad({ filter: /^splitter$/, namespace: 'test' }, () => ({ contents: 'export const useResultsSplitter = () => ({ref: null, ready: false, width: 220, bounds: {min: 100, max: 500}, handleProps: {}});' }));
  } }],
});

// Run the actual panel's event/effect code with native I/O replaced. Layout and
// child components are outside this harness; no browser or real Git is touched.
function harness() {
  const cells = [], cleanups = new Map();
  let cursor = 0, effects = [];
  const hooks = { ...React,
    useState(initial) {
      const i = cursor++;
      if (!(i in cells)) cells[i] = typeof initial === 'function' ? initial() : initial;
      return [cells[i], value => { cells[i] = typeof value === 'function' ? value(cells[i]) : value; }];
    },
    useRef(initial) { const i = cursor++; return cells[i] ??= { current: initial }; },
    useMemo: fn => fn(), useId: () => 'publication-test',
    useEffect(fn, deps) {
      const i = cursor++, previous = cells[i];
      if (!previous || !deps || deps.some((value, n) => !Object.is(value, previous[n]))) {
        effects.push(() => { cleanups.get(i)?.(); cleanups.set(i, fn()); });
      }
      cells[i] = deps;
    },
  };
  const module = { exports: {} };
  new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(
    name => name === 'react' ? hooks : requireFrontend(name), module, module.exports);
  const { ResultPublicationPanel, api } = module.exports;
  return {
    api,
    render(props) {
      cursor = 0; effects = [];
      const tree = ResultPublicationPanel(props);
      effects.forEach(fn => fn());
      return tree;
    },
    async settle(props) {
      for (let i = 0; i < 4; i++) { this.render(props); await new Promise(resolve => setImmediate(resolve)); }
      return this.render(props);
    },
    close() { for (const cleanup of cleanups.values()) cleanup?.(); },
  };
}
function elements(node) {
  if (Array.isArray(node)) return node.flatMap(elements);
  if (!node || typeof node !== 'object' || !node.props) return [];
  return [node, ...elements(node.props.children)];
}
const find = (tree, predicate) => { const element = elements(tree).find(predicate); assert.ok(element); return element; };
const button = tree => find(tree, node => node.type === 'button' && node.props.type === 'submit');
const form = tree => find(tree, node => node.type === 'form');
const field = (tree, suffix) => find(tree, node => node.props.id === `publication-test-${suffix}`);
const deferred = () => { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };
const preview = (workspaceId = 'a') => ({ workspaceId, revision: `${workspaceId}-revision`, baseCommit: 'base', sourceCommit: 'accepted',
  suggestedBranch: `review/${workspaceId}`, message: 'src/first.js\n- R019: 保存処理を更新', publications: [],
  files: ['src/first.js', 'src/second.js'].map(file => ({ file, rulesApplied: ['R019'], summary: '- R019: 保存処理を更新' })) });
const props = overrides => ({ state: { activeWorkspaceId: 'a', worktree: '/copy', running: false, readOnly: false, tasks: [], rules: [], executionRuns: [] },
  busy: false, usable: true, onDraftChange() {}, async onPublish() { throw new Error('unexpected publication'); }, ...overrides });

test('publication panel requires title, sends the reviewed snapshot and blocks duplicate submission', async () => {
  const h = harness(), diffRequests = [], requests = [], pending = deferred();
  h.api.GetResultPublicationPreview = async () => preview();
  h.api.GetResultPublicationFileDiff = async (...args) => { diffRequests.push(args); return 'selected-diff'; };
  const p = props({ onPublish: async request => { requests.push(request); return pending.promise; } });
  let tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, true);
  assert.deepEqual(diffRequests, [['a', 'a-revision', 'src/first.js']], 'only selected diff is requested');
  form(tree).props.onSubmit({ preventDefault() {} });
  assert.equal(requests.length, 0);
  field(tree, 'title').props.onChange({ target: { value: '保存処理を改善' } });
  field(tree, 'message').props.onChange({ target: { value: '編集したサマリー\n- R019' } });
  tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, false);
  form(tree).props.onSubmit({ preventDefault() {} });
  form(tree).props.onSubmit({ preventDefault() {} });
  assert.deepEqual(requests, [{ workspaceId: 'a', revision: 'a-revision', branch: 'review/a', title: '保存処理を改善', message: '編集したサマリー\n- R019' }]);
  pending.resolve({ ...requests[0], commit: 'a'.repeat(40), createdAt: '2026-09-23T00:00:00Z', fileCount: 2 });
  tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, true, 'same branch cannot be published again');
  assert.ok(elements(tree).some(node => node.props.className === 'publication-reflected'));
  h.close();
});

test('late preview and diff responses cannot replace another workspace or selected file', async () => {
  const h = harness(), oldPreview = deferred(), oldDiff = deferred();
  let calls = 0;
  h.api.GetResultPublicationPreview = () => ++calls === 1 ? oldPreview.promise : Promise.resolve(preview('b'));
  h.api.GetResultPublicationFileDiff = async (_workspace, _revision, file) => file === 'src/first.js' ? oldDiff.promise : 'second-file-diff';
  const a = props(), b = props({ state: { ...a.state, activeWorkspaceId: 'b' } });
  h.render(a);
  let tree = await h.settle(b);
  oldPreview.resolve(preview('a'));
  tree = await h.settle(b);
  assert.equal(field(tree, 'branch').props.value, 'review/b');
  const fileList = find(tree, node => typeof node.type === 'function' && typeof node.props.onSelect === 'function' && Array.isArray(node.props.files));
  fileList.props.onSelect('src/second.js');
  tree = await h.settle(b);
  oldDiff.resolve('stale-first-file-diff');
  tree = await h.settle(b);
  assert.ok(elements(tree).some(node => node.props.content === 'second-file-diff'));
  assert.ok(!elements(tree).some(node => node.props.content === 'stale-first-file-diff'));
  h.close();
});

test('a failed diff read prevents publication and leaves the edited commit body intact', async () => {
  const h = harness();
  h.api.GetResultPublicationPreview = async () => preview();
  h.api.GetResultPublicationFileDiff = async () => { throw new Error('反映内容が変更されています'); };
  const p = props();
  let tree = await h.settle(p);
  field(tree, 'title').props.onChange({ target: { value: '変更を反映' } });
  field(tree, 'message').props.onChange({ target: { value: '手入力の説明' } });
  tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, true);
  form(tree).props.onSubmit({ preventDefault() {} });
  assert.equal(field(tree, 'message').props.value, '手入力の説明');
  h.close();
});
