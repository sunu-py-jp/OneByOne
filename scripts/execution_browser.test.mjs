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
  stdin: { contents: 'export { TargetFilesPanel } from "./src/TargetFilesPanel"; export { TaskFileList, partitionTaskFiles } from "./src/TaskFileList"; export { RulePreviewPane } from "./src/RulePreviewPane";', resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'no-native-connection', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /.*/, namespace: 'test' }, () => ({ contents: 'export const api = new Proxy({}, { get() { throw new Error("Native API must not run in this test"); } });' }));
  } }],
});
const module = { exports: {} };
new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(requireFrontend, module, module.exports);
const { TargetFilesPanel, TaskFileList, partitionTaskFiles, RulePreviewPane } = module.exports;
const rule = (id, always = false) => ({ id, always, title: `Title ${id}`, summary: '', overview: 'Preserve the outcome', before: 'before()', after: 'after()', notes: '', holdConditions: '', pattern: always ? '' : 'before', candidateCount: 1, appliedCount: 0 });
const task = (file, status, rules) => ({ file, status, rules, attempts: 0, rulesApplied: [], note: '', inputHash: '', updatedAt: '', history: [] });
const files = [task('src/a.js', 'pending', ['R002', 'R001']), task('src/deep/b.js', 'done', [])];
const props = { tasks: files, rules: [rule('R001', true), rule('R002')], workspaceId: 'ws', root: '/project', selected: new Set(['src/a.js']), onSelection() {}, disabled: false };
const render = extra => renderToStaticMarkup(createElement(TargetFilesPanel, { ...props, ...extra }));

test('execution list keeps selection and status while candidate rules appear only in the content header', () => {
  const html = render();
  assert.equal((html.match(/type="checkbox"/g) || []).length, 3); // select all, selected filter, one unfinished file
  assert.match(html, /<strong>a\.js<\/strong>/);
  assert.match(html, /完了済み/);
  assert.match(html, /aria-expanded="false"/);
  assert.match(html, /src\/a\.jsの内容を表示/);
  assert.doesNotMatch(html, /src\/deep\/b\.jsを処理対象にする/);
  // R001 is both common and a candidate: deduplicate the active header and omit rule chips from list rows.
  assert.doesNotMatch(html, /class="task-row-candidates"/);
  assert.equal((html.match(/R001 Title R001の詳細/g) || []).length, 1);
  assert.equal((html.match(/R002 Title R002の詳細/g) || []).length, 1);
  assert.match(html, /task-header-candidates/);
  assert.match(html, /status-pending/);
  assert.match(html, /未修正/);
});

test('confirmation uses the same browser without selection controls', () => {
  const html = render({ selectionEnabled: false, tasks: [files[0]] });
  assert.doesNotMatch(html, /type="checkbox"/);
  assert.match(html, /確定した処理対象/);
  assert.match(html, /src\/a\.jsの内容を表示/);
  assert.match(html, /R002 Title R002の詳細/);
  assert.doesNotMatch(html, /class="task-row-candidates"/);
});

test('shared task list sorts active files by status with retries first and completed files folded separately', () => {
  const sortedTasks = [task('done.js', 'done', []), task('skip.js', 'skipped', []), task('hold.js', 'needs_human', []),
    task('failed.js', 'failed', []), task('pending.js', 'pending', []), task('running.js', 'running', []),
    { ...task('retry.js', 'pending', []), resumeRequested: true }];
  const html = renderToStaticMarkup(createElement(TaskFileList, { tasks: sortedTasks, selectedFile: 'retry.js', onSelectFile() {} }));
  const names = [...html.matchAll(/aria-label="([^"]+)の内容を表示"/g)].map(match => match[1]);
  assert.deepEqual(names, ['retry.js', 'running.js', 'pending.js', 'failed.js', 'hold.js']);
  assert.match(html, /status-retry/);
  assert.match(html, /再試行/);
  assert.match(html, /未修正/);
  assert.match(html, /aria-expanded="false"/);
  assert.doesNotMatch(html, /done\.jsの内容を表示/);
  assert.doesNotMatch(html, /skip\.jsの内容を表示/);
});

test('expanded completed rows follow their boundary in one scroll list and replace checkboxes with retry actions', () => {
  const tasks = [...files, task('src/unchanged.js', 'skipped', [])];
  const html = renderToStaticMarkup(createElement(TaskFileList, { tasks, selectedFile: files[0].file, onSelectFile() {}, onRetry() {}, completedOpen: true,
    selection: { files: new Set(tasks.map(item => item.file)), disabled: false, onChange() {} } }));
  assert.equal((html.match(/type="checkbox"/g) || []).length, 1);
  assert.doesNotMatch(html, /src\/deep\/b\.jsを処理対象にする/);
  assert.match(html, /src\/deep\/b\.jsを再試行に追加/);
  assert.ok(html.indexOf('src/deep/b.jsを再試行に追加') < html.indexOf('src/deep/b.jsの内容を表示'));
  assert.ok(html.indexOf('status-done') > html.indexOf('src/deep/b.jsの内容を表示'));
  assert.match(html, /target-file-directory">src\/deep\//);
  assert.match(html, /<strong>b\.js<\/strong>/);
  assert.doesNotMatch(html, /src\/unchanged\.jsを処理対象にする/);
  assert.match(html, /src\/unchanged\.jsを再試行に追加/);
  assert.match(html, /status-skipped/);
  assert.match(html, /変更不要/);
  assert.equal((html.match(/class="task-list-scroll"/g) || []).length, 1);
  assert.equal((html.match(/role="list"/g) || []).length, 1);
  assert.match(html, /aria-setsize="3"/);
  assert.match(html, /height:136px/);
  const boundary = html.indexOf('class="task-completed-boundary"');
  assert.ok(boundary > html.indexOf('src/a.jsの内容を表示'));
  assert.ok(boundary < html.indexOf('src/unchanged.jsの内容を表示'));
  assert.ok(boundary < html.indexOf('src/deep/b.jsの内容を表示'));
  assert.doesNotMatch(html, /task-completed-footer/);
  assert.match(html, /完了済み<\/span><b>2<\/b>/);
});

test('an all-completed queue places its boundary first without an empty unfinished section', () => {
  const tasks = [task('done.js', 'done', []), task('unchanged.js', 'skipped', [])];
  const base = { tasks, selectedFile: 'done.js', onSelectFile() {} };
  const closed = renderToStaticMarkup(createElement(TaskFileList, base));
  assert.doesNotMatch(closed, /未完了のファイルはありません|task-list-empty/);
  assert.match(closed, /完了済み<\/span><b>2<\/b>/);
  assert.match(closed, /height:34px/);
  assert.doesNotMatch(closed, /role="listitem"/);
  const open = renderToStaticMarkup(createElement(TaskFileList, { ...base, completedOpen: true }));
  assert.equal((open.match(/role="list"/g) || []).length, 1);
  assert.equal((open.match(/role="listitem"/g) || []).length, 2);
  assert.match(open, /height:102px/);
  const boundary = open.indexOf('class="task-completed-boundary"');
  assert.ok(boundary >= 0 && boundary < open.indexOf('unchanged.jsの内容を表示'));
  assert.ok(boundary < open.indexOf('done.jsの内容を表示'));
  assert.doesNotMatch(open, /task-completed-footer/);
});

test('shared task list virtualizes large queues instead of rendering every file', () => {
  const tasks = Array.from({ length: 10000 }, (_, i) => task(`src/${String(i).padStart(5, '0')}.js`, 'pending', []));
  const html = renderToStaticMarkup(createElement(TaskFileList, { tasks, selectedFile: tasks[0].file, onSelectFile() {} }));
  const rendered = (html.match(/role="listitem"/g) || []).length;
  assert.ok(rendered > 0 && rendered < 50);
  assert.match(html, /aria-setsize="10000"/);
  assert.match(html, /height:340000px/);
});

test('results retain this run\'s completed and unchanged files above previously completed files', () => {
  const tasks = [task('previous-done.js', 'done', []), task('current-unchanged.js', 'skipped', []),
    task('current-done.js', 'done', []), task('pending.js', 'pending', []), task('previous-unchanged.js', 'skipped', []),
    task('hold.js', 'needs_human', [])];
  const runTargetFiles = new Set(['current-done.js', 'current-unchanged.js', 'hold.js']);
  const base = { tasks, runTargetFiles, selectedFile: 'current-done.js', onSelectFile() {}, onRetry() {} };
  const closed = renderToStaticMarkup(createElement(TaskFileList, base));
  const names = [...closed.matchAll(/aria-label="([^"]+)の内容を表示"/g)].map(match => match[1]);
  assert.deepEqual(names, ['pending.js', 'hold.js', 'current-done.js', 'current-unchanged.js']);
  assert.match(closed, /current-done\.jsを再試行に追加/);
  assert.match(closed, /current-unchanged\.jsを再試行に追加/);
  assert.match(closed, /完了済み<\/span><b>2<\/b>/);
  assert.equal((closed.match(/class="task-list-scroll"/g) || []).length, 1);
  assert.match(closed, /height:170px/); // four current rows and one shared boundary
  const opened = renderToStaticMarkup(createElement(TaskFileList, { ...base, completedOpen: true }));
  const boundary = opened.indexOf('class="task-completed-boundary"');
  assert.ok(boundary > opened.indexOf('current-unchanged.jsの内容を表示'));
  assert.ok(boundary < opened.indexOf('previous-done.jsの内容を表示'));
  assert.ok(boundary < opened.indexOf('previous-unchanged.jsの内容を表示'));
  assert.match(opened, /height:238px/);
});

test('a fully completed current run stays visible without a completed fold', () => {
  const tasks = [task('finished.js', 'done', []), task('unchanged.js', 'skipped', [])];
  const html = renderToStaticMarkup(createElement(TaskFileList, {
    tasks, runTargetFiles: new Set(tasks.map(item => item.file)), selectedFile: 'finished.js', onSelectFile() {},
  }));
  assert.equal((html.match(/role="listitem"/g) || []).length, 2);
  assert.doesNotMatch(html, /task-completed-boundary|task-completed-toggle/);
  assert.match(html, /height:68px/);
});

test('run grouping follows selected run membership and preserves status-only execution grouping', () => {
  const tasks = [task('first-run.js', 'done', []), task('second-run.js', 'skipped', []), task('unfinished.js', 'failed', [])];
  const original = [...tasks];
  const first = partitionTaskFiles(tasks, new Set(['first-run.js']));
  assert.deepEqual(first.active.map(item => item.file), ['unfinished.js', 'first-run.js']);
  assert.deepEqual(first.completed.map(item => item.file), ['second-run.js']);
  const second = partitionTaskFiles(tasks, new Set(['second-run.js']));
  assert.deepEqual(second.active.map(item => item.file), ['unfinished.js', 'second-run.js']);
  assert.deepEqual(second.completed.map(item => item.file), ['first-run.js']);
  assert.deepEqual(partitionTaskFiles(tasks), partitionTaskFiles(tasks, new Set()));
  assert.deepEqual(partitionTaskFiles(tasks).active.map(item => item.file), ['unfinished.js']);
  assert.deepEqual(tasks, original);
});

test('run-specific grouping keeps large completed runs virtualized in the shared scroll', () => {
  const tasks = Array.from({ length: 10000 }, (_, i) => task(`src/${String(i).padStart(5, '0')}.js`, i % 2 ? 'skipped' : 'done', []));
  const runTargetFiles = new Set(tasks.slice(0, 8000).map(item => item.file));
  const groups = partitionTaskFiles(tasks, runTargetFiles);
  assert.equal(groups.active.length, 8000);
  assert.equal(groups.completed.length, 2000);
  for (const completedOpen of [false, true]) {
    const html = renderToStaticMarkup(createElement(TaskFileList, {
      tasks, runTargetFiles, selectedFile: tasks[0].file, onSelectFile() {}, completedOpen,
    }));
    const rendered = (html.match(/role="listitem"/g) || []).length;
    assert.ok(rendered > 0 && rendered < 50);
    assert.equal((html.match(/class="task-list-scroll"/g) || []).length, 1);
    assert.match(html, /完了済み<\/span><b>2,000<\/b>/);
    assert.match(html, completedOpen ? /aria-setsize="10000"/ : /aria-setsize="8000"/);
    assert.match(html, completedOpen ? /height:340034px/ : /height:272034px/);
  }
});

test('selection lock explains the reason while file and rule previews remain usable', () => {
  const html = render({ disabled: true, disabledReason: '処理中は対象を変更できません' });
  assert.match(html, /aria-label="処理中は対象を変更できません"/);
  assert.match(html, /type="checkbox"[^>]*disabled/);
  assert.doesNotMatch(html, /<button[^>]*task-file-open[^>]*disabled/);
  assert.doesNotMatch(html, /<button[^>]*task-candidate-chip[^>]*disabled/);
});

test('inline rule preview preserves structured text safely and hides empty optional sections', () => {
  const html = renderToStaticMarkup(createElement(RulePreviewPane, { rule: { ...rule('R001', true), overview: '<script>rule content</script>' }, onClose() {} }));
  assert.match(html, /変更概要/);
  assert.match(html, /変更前/);
  assert.match(html, /変更後/);
  assert.match(html, /すべてのファイルに適用/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.doesNotMatch(html, /<h3>備考<\/h3>/);
  assert.doesNotMatch(html, /<h3>修正を保留すべきケース<\/h3>/);
});
