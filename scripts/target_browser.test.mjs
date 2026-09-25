import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const requireFrontend = createRequire(new URL('../frontend/package.json', import.meta.url));
const { createElement } = requireFrontend('react');
const { renderToStaticMarkup } = requireFrontend('react-dom/server');
const bundle = await build({
  absWorkingDir: frontend,
  stdin: { contents: 'export { TargetFilePath, TargetFileSource } from "./src/TargetFolderBrowser"; export { previewTargetFiles, previewTargetContent, previewExecutionContent, previewState } from "./src/preview";', resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'no-native-connection', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /.*/, namespace: 'test' }, () => ({ contents: 'export const api = new Proxy({}, { get() { throw new Error("Native API must not run in this test"); } });' }));
  } }],
});
const module = { exports: {} };
new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(requireFrontend, module, module.exports);
const { TargetFilePath, TargetFileSource, previewTargetFiles, previewTargetContent, previewExecutionContent, previewState } = module.exports;
const renderSource = content => renderToStaticMarkup(createElement(TargetFileSource, { content }));

test('file paths emphasize the basename and render source markup only as text', () => {
  const path = renderToStaticMarkup(createElement(TargetFilePath, { file: 'src/nested/<script>.ts' }));
  assert.match(path, /target-file-directory">src\/nested\//);
  assert.match(path, /<strong>&lt;script&gt;\.ts<\/strong>/);
  assert.doesNotMatch(path, /<script>/);
  const source = renderSource('<script>alert("source")</script>\n');
  assert.match(source, /&lt;script&gt;/);
  assert.doesNotMatch(source, /<script>/);
});

test('source line numbers preserve blank lines and CRLF without adding a phantom final line', () => {
  const source = renderSource('first\r\n\r\nthird\r\n');
  assert.match(source, /aria-hidden="true">1\n2\n3<\/pre>/);
  assert.match(source, /<code>first\n\nthird<\/code>/);
  assert.match(renderSource(''), /空のファイルです/);
});

test('oversized line counts disclose truncation and preserve the last displayed line', () => {
  const source = renderSource('line\n'.repeat(19999) + 'last-shown\nnot-shown\n');
  assert.match(source, /先頭20,000行を表示しています/);
  assert.match(source, /last-shown/);
  assert.doesNotMatch(source, /not-shown/);
  assert.doesNotMatch(renderSource('line\n'.repeat(20000)), /先頭20,000行/);
});

test('folder preview uses original content without changing task selection or results', () => {
  const before = structuredClone(previewState);
  const id = before.activeWorkspaceId;
  const root = before.config.root;
  const listing = previewTargetFiles(id, root);
  assert.ok(listing.files.some(item => item.file === 'README.md'));
  assert.ok(listing.files.length > before.tasks.length);
  const source = previewTargetContent(id, root, before.tasks[0].file);
  assert.match(source.content, /StorageSession/);
  assert.doesNotMatch(source.content, /StorageClient/);
  assert.equal(source.size, Buffer.byteLength(source.content));
  assert.throws(() => previewTargetContent('old-workspace', root, source.file));
  assert.throws(() => previewTargetContent(id, '/another-root', source.file));
  assert.throws(() => previewTargetContent(id, root, '../outside.txt'));
  assert.deepEqual(previewState, before);
});

test('execution preview shows accepted worktree content while folder preview keeps original content', () => {
  const before = structuredClone(previewState);
  const id = before.activeWorkspaceId;
  const root = before.config.root;
  const accepted = before.tasks.find(task => task.history.some(attempt => attempt.outcome === 'done'));
  const pending = before.tasks.find(task => task.status === 'pending');
  assert.ok(accepted);
  assert.ok(pending);
  assert.match(previewExecutionContent(id, root, accepted.file).content, /StorageClient/);
  assert.match(previewTargetContent(id, root, accepted.file).content, /StorageSession/);
  assert.equal(previewExecutionContent(id, root, pending.file).content, previewTargetContent(id, root, pending.file).content);
  for (const task of before.tasks.filter(item => ['failed', 'needs_human', 'skipped'].includes(item.status))) {
    assert.equal(previewExecutionContent(id, root, task.file).content, previewTargetContent(id, root, task.file).content);
  }
  assert.throws(() => previewExecutionContent(id, root, 'README.md'));
  assert.throws(() => previewExecutionContent('stale-workspace', root, accepted.file));
  assert.deepEqual(previewState, before);
});

test('execution preview preserves an accepted revision through a skipped retry and respects discards', () => {
  const before = structuredClone(previewState);
  try {
    const id = before.activeWorkspaceId;
    const root = before.config.root;
    const task = previewState.tasks.find(item => item.status === 'done');
    const accepted = previewExecutionContent(id, root, task.file).content;
    task.history.push({ ...structuredClone(task.history[0]), id: 'retry', number: 2, outcome: 'skipped', commit: '', changes: [] });
    task.status = 'skipped';
    assert.equal(previewExecutionContent(id, root, task.file).content, accepted);
    assert.match(previewTargetContent(id, root, task.file).content, /StorageSession/);
    task.discards = [{ id: 'discard', state: 'done', throughAttempt: 2 }];
    assert.equal(previewExecutionContent(id, root, task.file).content, previewTargetContent(id, root, task.file).content);
  } finally {
    Object.assign(previewState, before);
  }
});
