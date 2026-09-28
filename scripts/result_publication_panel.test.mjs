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
  stdin: { contents: 'export { ResultPublicationPanel, publicationLinkFile } from "./src/ResultPublicationPanel"; export { api } from "./src/bridge";', resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'publication-native-stubs', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/(?:src\/)?bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /^bridge$/, namespace: 'test' }, () => ({ contents: 'export const api = {};' }));
    plugin.onResolve({ filter: /^\.\/usePublicationDrawerResize$/ }, () => ({ path: 'drawer', namespace: 'test' }));
    plugin.onLoad({ filter: /^drawer$/, namespace: 'test' }, () => ({ contents: 'export const usePublicationDrawerResize = () => ({ref: null, ready: false, width: 600, bounds: {min: 320, max: 900}, handleProps: {}, finish() {}});' }));
    plugin.onResolve({ filter: /^\.\/useResultsSplitter$/ }, () => ({ path: 'splitter', namespace: 'test' }));
    plugin.onLoad({ filter: /^splitter$/, namespace: 'test' }, () => ({ contents: 'export const useResultsSplitter = () => ({ref: null, ready: false, width: 220, bounds: {min: 100, max: 500}, handleProps: {}});' }));
  } }],
});

// Run the actual panel's event/effect code with native I/O replaced. Layout and
// child components are outside this harness; no browser or real Git is touched.
function harness(storage = new Map()) {
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
  const listeners = new Map();
  const windowMock = { addEventListener(name, callback) { listeners.set(name, callback); }, removeEventListener(name, callback) { if (listeners.get(name) === callback) listeners.delete(name); } };
  const module = { exports: {} };
  const globals = { localStorage: { getItem(key) { return storage.get(key) ?? null; }, setItem(key, value) { storage.set(key, value); } } };
  new Function('require', 'module', 'exports', 'window', 'globalThis', bundle.outputFiles[0].text)(
    name => name === 'react' ? hooks : requireFrontend(name), module, module.exports, windowMock, globals);
  const { ResultPublicationPanel, publicationLinkFile, api } = module.exports;
  return {
    api, publicationLinkFile, key(event) { listeners.get("keydown")?.(event); },
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
const messageToggle = (tree, label) => find(tree, node => node.type === 'button' && node.props['aria-label'] === `コミット本文の${label}`);
const markdown = tree => find(tree, node => typeof node.props.onLinkClick === 'function');
function openLink(tree, href, target = { isConnected: true, focus() {} }) {
  let prevented = false;
  markdown(tree).props.onLinkClick(href, { preventDefault() { prevented = true; }, currentTarget: target });
  return prevented;
}
const drawer = tree => find(tree, node => node.props.role === 'dialog');
const deferred = () => { let resolve, reject; const promise = new Promise((a, b) => { resolve = a; reject = b; }); return { promise, resolve, reject }; };
const preview = (workspaceId = 'a') => ({ workspaceId, revision: `${workspaceId}-revision`, baseCommit: 'base', sourceCommit: 'accepted',
  suggestedBranch: `review/${workspaceId}`, message: '[src/first.js](project/src/first.js)\n- R019: 保存処理を更新', publications: [],
  files: ['src/first.js', 'src/second.js'].map(file => ({ file, linkPath: `project/${file}`, rulesApplied: ['R019'], summary: '- R019: 保存処理を更新' })) });
const props = overrides => ({ state: { activeWorkspaceId: 'a', worktree: '/copy', running: false, readOnly: false, tasks: [], rules: [], executionRuns: [] },
  busy: false, usable: true, onDraftChange() {}, async onPublish() { throw new Error('unexpected publication'); }, ...overrides });

test('publication panel requires title, sends the reviewed snapshot and blocks duplicate submission', async () => {
  const h = harness(), diffRequests = [], requests = [], pending = deferred();
  h.api.GetResultPublicationPreview = async () => preview();
  h.api.GetResultPublicationFileDiff = async (...args) => { diffRequests.push(args); return 'selected-diff'; };
  const p = props({ onPublish: async request => { requests.push(request); return pending.promise; } });
  let tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, true);
  assert.deepEqual(diffRequests, [], 'diff is only fetched after a message link is clicked');
  assert.ok(!elements(tree).some(node => node.props['aria-label'] === '反映するファイル一覧'));
  form(tree).props.onSubmit({ preventDefault() {} });
  assert.equal(requests.length, 0);
  assert.equal(messageToggle(tree, 'プレビュー').props['aria-pressed'], true);
  assert.ok(!elements(tree).some(node => node.type === 'textarea'), 'preview cannot edit the commit message');
  assert.ok(elements(tree).some(node => node.props.body === preview().message));
  messageToggle(tree, '原文').props.onClick();
  tree = await h.settle(p);
  field(tree, 'title').props.onChange({ target: { value: '保存処理を改善' } });
  field(tree, 'message').props.onChange({ target: { value: '編集したサマリー\n- R019' } });
  tree = await h.settle(p);
  messageToggle(tree, 'プレビュー').props.onClick();
  tree = await h.settle(p);
  assert.ok(elements(tree).some(node => node.props.body === '編集したサマリー\n- R019'));
  assert.ok(!elements(tree).some(node => node.type === 'textarea'));
  messageToggle(tree, '原文').props.onClick();
  tree = await h.settle(p);
  assert.equal(field(tree, 'message').props.value, '編集したサマリー\n- R019', 'switching modes preserves the draft');
  messageToggle(tree, 'プレビュー').props.onClick();
  tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, false);
  form(tree).props.onSubmit({ preventDefault() {} });
  form(tree).props.onSubmit({ preventDefault() {} });
  assert.deepEqual(requests, [{ workspaceId: 'a', revision: 'a-revision', branch: 'review/a', title: '保存処理を改善', message: '編集したサマリー\n- R019', messageAsFile: false }]);
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
  openLink(tree, 'project/src/first.js');
  tree = await h.settle(b);
  openLink(tree, 'project/src/second.js');
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
  openLink(tree, 'project/src/first.js');
  tree = await h.settle(p);
  messageToggle(tree, '原文').props.onClick();
  tree = await h.settle(p);
  field(tree, 'title').props.onChange({ target: { value: '変更を反映' } });
  field(tree, 'message').props.onChange({ target: { value: '手入力の説明' } });
  tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, true);
  form(tree).props.onSubmit({ preventDefault() {} });
  assert.equal(field(tree, 'message').props.value, '手入力の説明');
  h.close();
});

test('message links open a diff-only drawer, preserve ordinary external links, and explain unknown local destinations', async () => {
  const h = harness(), requests = [];
  let restored = 0;
  h.api.GetResultPublicationPreview = async () => preview();
  h.api.GetResultPublicationFileDiff = async (...args) => { requests.push(args); return '@@ -1 +1 @@\n-before\n+after'; };
  const p = props();
  let tree = await h.settle(p);
  assert.equal(drawer(tree).props['aria-hidden'], true);
  assert.equal(openLink(tree, 'https://example.com/docs'), false);
  assert.equal(openLink(tree, '../src/first.js'), true);
  tree = await h.settle(p);
  assert.equal(requests.length, 0);
  assert.ok(elements(tree).some(node => node.props.className === 'publication-link-error'));
  assert.equal(openLink(tree, 'project/src/first.js', { isConnected: true, focus() { restored++; } }), true);
  tree = await h.settle(p);
  assert.deepEqual(requests, [['a', 'a-revision', 'src/first.js']]);
  assert.equal(drawer(tree).props['aria-hidden'], false);
  assert.equal(drawer(tree).props.inert, false);
  assert.ok(!elements(drawer(tree)).some(node => node.props.className === 'publication-file-summary'));
  h.key({ key: 'Escape', preventDefault() {} });
  tree = await h.settle(p);
  assert.equal(drawer(tree).props['aria-hidden'], true);
  assert.equal(restored, 1);
  openLink(tree, 'project/src/second.js');
  tree = await h.settle(p);
  find(tree, node => node.props['aria-label'] === '差分を閉じる').props.onClick();
  tree = await h.settle(p);
  assert.equal(drawer(tree).props.inert, true);
  h.close();
});

test('publication file links match only the exact decoded repository path', () => {
  const h = harness();
  const f = { file: 'src/日本 語#%.js', linkPath: 'app/src/日本 語#%.js' };
  assert.equal(h.publicationLinkFile('app/src/%E6%97%A5%E6%9C%AC%20%E8%AA%9E%23%25.js', [f]), f);
  assert.equal(h.publicationLinkFile('src/%E6%97%A5%E6%9C%AC%20%E8%AA%9E%23%25.js', [f]), undefined);
  assert.equal(h.publicationLinkFile('app/../app/src/%E6%97%A5%E6%9C%AC%20%E8%AA%9E%23%25.js', [f]), undefined);
  assert.equal(h.publicationLinkFile('app/src/%broken', [f]), undefined);
  assert.equal(h.publicationLinkFile('src/a.js', [{ file: 'src/a.js' }]), undefined, 'do not infer missing provenance from a filename');
  h.close();
});

test('manual file mode survives preview refresh and reload within its workspace', async () => {
  const storage = new Map(), h = harness(storage), requests = [];
  h.api.GetResultPublicationPreview = async () => preview();
  const p = props({ onPublish: async request => { requests.push(request); return { ...request, commit: 'b'.repeat(40), reportPath: 'OneByOne/20260928123456_results.md' }; } });
  let tree = await h.settle(p);
  assert.equal(field(tree, 'message-as-file').props.checked, false);
  field(tree, 'message-as-file').props.onChange({ target: { checked: true } });
  tree = await h.settle(p);
  find(tree, node => node.type === 'button' && node.props.children?.some?.(child => child === '再読み込み')).props.onClick();
  tree = await h.settle(p);
  assert.equal(field(tree, 'message-as-file').props.checked, true);
  assert.equal(field(tree, 'message-as-file').props.disabled, false);
  field(tree, 'title').props.onChange({ target: { value: 'Report as file' } });
  tree = await h.settle(p);
  form(tree).props.onSubmit({ preventDefault() {} });
  tree = await h.settle(p);
  assert.equal(requests[0].messageAsFile, true);
  assert.ok(elements(tree).some(node => node.props.title === 'OneByOne/20260928123456_results.md'));
  h.close();
  const reloaded = harness(storage);
  reloaded.api.GetResultPublicationPreview = async () => preview();
  tree = await reloaded.settle(props());
  assert.equal(field(tree, 'message-as-file').props.checked, true);
  reloaded.api.GetResultPublicationPreview = async () => preview('b');
  tree = await reloaded.settle(props({ state: { ...p.state, activeWorkspaceId: 'b' } }));
  assert.equal(field(tree, 'message-as-file').props.checked, false);
  reloaded.close();
});

test('long body forces file mode without overwriting the voluntary preference', async () => {
  const h = harness(), requests = [];
  h.api.GetResultPublicationPreview = async () => ({ ...preview(), message: '😀😀😀', messageFileThreshold: 3 });
  const p = props({ onPublish: async request => { requests.push(request); return { ...request, commit: 'b'.repeat(40) }; } });
  let tree = await h.settle(p);
  assert.equal(field(tree, 'message-as-file').props.checked, false);
  messageToggle(tree, '原文').props.onClick();
  tree = await h.settle(p);
  field(tree, 'title').props.onChange({ target: { value: 'Long report' } });
  field(tree, 'message').props.onChange({ target: { value: '😀😀😀あ' } });
  tree = await h.settle(p);
  assert.equal(field(tree, 'message-as-file').props.checked, true);
  assert.equal(field(tree, 'message-as-file').props.disabled, true);
  assert.ok(elements(tree).some(node => node.props.id === 'publication-test-message-file-note'));
  field(tree, 'message').props.onChange({ target: { value: '😀😀😀' } });
  tree = await h.settle(p);
  assert.equal(field(tree, 'message-as-file').props.checked, false);
  assert.equal(field(tree, 'message-as-file').props.disabled, false);
  field(tree, 'message').props.onChange({ target: { value: '😀😀😀あ' } });
  tree = await h.settle(p);
  form(tree).props.onSubmit({ preventDefault() {} });
  await h.settle(p);
  assert.equal(requests[0].messageAsFile, true);
  assert.equal(requests[0].message, '😀😀😀あ');
  h.close();
});

test('report-only file links open unchanged detail and do not count as committable changes', async () => {
  const h = harness(), requests = [];
  const unchanged = { file: 'src/unchanged.js', linkPath: 'project/src/unchanged.js', rulesApplied: [], summary: '変更不要' };
  h.api.GetResultPublicationPreview = async () => ({ ...preview(), files: [], reportFiles: [unchanged] });
  h.api.GetResultPublicationFileDiff = async (...args) => { requests.push(args); return ''; };
  const p = props();
  let tree = await h.settle(p);
  field(tree, 'title').props.onChange({ target: { value: 'No code changes' } });
  tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, true);
  openLink(tree, unchanged.linkPath);
  tree = await h.settle(p);
  assert.deepEqual(requests, [['a', 'a-revision', unchanged.file]]);
  assert.equal(drawer(tree).props['aria-hidden'], false);
  assert.ok(elements(drawer(tree)).some(node => node.props.children === '変更はありません'));
  assert.equal(button(tree).props.disabled, true);
  field(tree, 'message-as-file').props.onChange({ target: { checked: true } });
  tree = await h.settle(p);
  assert.equal(button(tree).props.disabled, false, 'report-only publication is allowed when the report is committed as a file');
  h.close();
});
