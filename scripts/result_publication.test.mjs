import test from 'node:test';
import assert from 'node:assert/strict';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const bundle = await build({
  absWorkingDir: frontend,
  stdin: { contents: 'export * from "./src/result-publication"; export { previewResultPublication, previewDetail, previewState } from "./src/preview";', resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'esm',
});
const { refreshPublicationDraft, publicationBlockReason, publicationMessageFileMode, previewResultPublication, previewDetail, previewState } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
const preview = (changes = {}) => ({ workspaceId: 'workspace-a', baseCommit: 'base-a', revision: 'revision-a', suggestedBranch: 'result/a', message: 'generated summary',
  files: [{ file: 'a.js', rulesApplied: ['R101'] }], publications: [], ...changes });

test('refresh updates generated values but preserves a typed title, branch and message', () => {
  const first = refreshPublicationDraft(undefined, preview());
  assert.equal(first.title, '', 'the commit title is deliberately required rather than silently generated');
  const changed = preview({ revision: 'revision-b', suggestedBranch: 'result/b', message: 'updated summary' });
  const untouched = refreshPublicationDraft({ ...first, title: 'My review title' }, changed);
  assert.equal(untouched.branch, 'result/b');
  assert.equal(untouched.message, 'updated summary');
  assert.equal(untouched.title, 'My review title');
  const typed = refreshPublicationDraft({ ...first, title: 'Title', branch: 'review/manual', message: 'Custom explanation' }, changed);
  assert.equal(typed.branch, 'review/manual');
  assert.equal(typed.message, 'Custom explanation');
  assert.equal(typed.title, 'Title');
  assert.equal(typed.suggestedMessage, 'updated summary');
  // Editing to an empty field is still deliberate and must not be overwritten.
  assert.equal(refreshPublicationDraft({ ...first, message: '' }, changed).message, '');
});

test('a different workspace never receives another workspace draft', () => {
  const first = { ...refreshPublicationDraft(undefined, preview()), title: 'Private project title', message: 'Private content', messageAsFile: true };
  const next = refreshPublicationDraft(first, preview({ workspaceId: 'workspace-b', suggestedBranch: 'result/other', message: 'Other summary' }));
  assert.equal(next.title, '');
  assert.equal(next.message, 'Other summary');
  assert.equal(next.branch, 'result/other');
  assert.equal(next.messageAsFile, false);
  assert.equal(refreshPublicationDraft(first, preview({ message: 'Refreshed report' })).messageAsFile, true);
  const anotherTarget = refreshPublicationDraft(first, preview({ baseCommit: 'base-another-target', message: 'New project summary' }));
  assert.equal(anotherTarget.title, '');
  assert.equal(anotherTarget.message, 'New project summary');
});

test('file mode honors explicit selection and enforces only a Unicode code-point threshold', () => {
  assert.deepEqual(publicationMessageFileMode('😀'.repeat(10_000), false), { threshold: 10_000, required: false, selected: false });
  assert.deepEqual(publicationMessageFileMode('😀'.repeat(10_000) + 'あ', false), { threshold: 10_000, required: true, selected: true });
  assert.equal(publicationMessageFileMode('abc', true).selected, true);
  assert.equal(publicationMessageFileMode('abcd', false, 3).required, true);
  assert.equal(publicationMessageFileMode('abc', false, 3).required, false);
  for (const value of [0, -1, 2.5, NaN, Infinity]) assert.equal(publicationMessageFileMode('', false, value).threshold, 10_000);
});

test('a results file can be committed without code changes but requires reportable files', () => {
  const reportFiles = [{ file: 'unchanged.js', rulesApplied: [] }];
  const data = preview({ files: [], reportFiles });
  const ready = { preview: data, draft: { ...refreshPublicationDraft(undefined, data), title: 'Reviewed unchanged files', messageAsFile: true },
    busy: false, running: false, readOnly: false, usable: true, loading: false, error: '' };
  assert.equal(publicationBlockReason(ready), '');
  assert.ok(publicationBlockReason({ ...ready, draft: { ...ready.draft, messageAsFile: false } }));
  assert.ok(publicationBlockReason({ ...ready, preview: { ...data, reportFiles: [] } }));
  assert.equal(publicationBlockReason({ ...ready, draft: { ...ready.draft, messageAsFile: false, message: 'あ'.repeat(10_001) } }), '');
});

test('publication requires a title, changes, write access, a current preview and an idle app', () => {
  const data = preview();
  const ready = { preview: data, draft: { ...refreshPublicationDraft(undefined, data), title: 'Apply changes' },
    busy: false, running: false, readOnly: false, usable: true, loading: false, error: '' };
  assert.equal(publicationBlockReason(ready), '');
  for (const change of [
    { usable: false }, { running: true }, { busy: true }, { readOnly: true }, { loading: true }, { error: 'stale' },
    { preview: undefined }, { draft: undefined }, { preview: preview({ files: [] }) },
    { draft: { ...ready.draft, workspaceId: 'different' } }, { draft: { ...ready.draft, baseCommit: 'different' } }, { draft: { ...ready.draft, title: '  ' } },
    { draft: { ...ready.draft, branch: ' ' } }, { preview: preview({ publications: [{ branch: 'result/a' }] }) },
  ]) assert.ok(publicationBlockReason({ ...ready, ...change }), JSON.stringify(change));
  assert.match(publicationBlockReason({ ...ready, draft: { ...ready.draft, title: '' } }), /タイトル/);
});

test('browser publication preview includes only adopted cumulative diffs and actual fixed rule IDs', () => {
  const result = previewResultPublication();
  const expected = previewState.tasks.filter(task => previewDetail(task.file).diff).map(task => task.file).sort();
  assert.deepEqual(result.files.map(file => file.file), expected);
  assert.ok(result.files.length > 0);
  assert.deepEqual(result.reportFiles.map(file => file.file), previewState.tasks.filter(task => task.status !== 'pending' || task.history.length).map(task => task.file).sort());
  assert.equal(result.messageFileThreshold, 10_000);
  assert.match(result.message, /\| ルールID \| ステータス \| 修正内容 \| レビュー結果 \|/);
  for (const status of ['✅完了', '☑️修正不要', '⚠️要確認']) assert.ok(result.message.includes(`## ファイル　${status}`));
  for (const file of result.files) {
    const fixed = (previewDetail(file.file).changes || []).filter(item => item.status === 'fixed');
    assert.deepEqual(file.rulesApplied, [...new Set(fixed.map(item => item.ruleId))]);
    assert.equal(file.diff, '', 'diff is lazy rather than included for every file');
    assert.ok(result.message.includes(file.file));
  }
  assert.ok(!result.files.some(file => previewState.tasks.find(task => task.file === file.file)?.status === 'failed'));
  const saved = structuredClone(previewState.tasks);
  try {
    const adopted = previewState.tasks.find(task => result.files.some(file => file.file === task.file));
    adopted.discards = [{ id: 'test-discard', state: 'done', throughAttempt: adopted.attempts }];
    assert.ok(!previewResultPublication().files.some(file => file.file === adopted.file));
  } finally { previewState.tasks = saved; }
});

test('file summary and details share status/path ordering and retained fixed changes survive a no-change retry', () => {
  const saved = structuredClone(previewState.tasks);
  try {
    const base = structuredClone(saved.find(task => task.status === 'done'));
    const fixedItem = base.history[0].changes[0];
    const make = (file, outcome, items, partial = false) => ({ ...structuredClone(base), file, status: outcome,
      rules: ['R_UNCHANGED', 'R_HELD', 'R_FIXED', 'R_UNKNOWN'],
      history: [{ ...structuredClone(base.history[0]), id: file, outcome, changes: items, partial, commit: outcome === 'done' || partial ? 'accepted' : '' }] });
    const fixed = { ...fixedItem, id: 'fixed', ruleId: 'R_FIXED' };
    const held = { ...fixedItem, id: 'held', ruleId: 'R_HELD', status: 'needs_human', change: 'Confirm the response contract' };
    const unchanged = { ...fixedItem, id: 'rule:unchanged', ruleId: 'R_UNCHANGED', status: 'unchanged' };
    const retry = make('src/b-done.ts', 'done', [fixed, unchanged]);
    retry.history.push({ ...structuredClone(retry.history[0]), id: 'retry', number: 2, outcome: 'skipped', changes: [], commit: '', note: '追加修正はありません' });
    previewState.tasks = [make('src/a-unchanged.ts', 'skipped', [unchanged]), make('src/z-held.ts', 'needs_human', [held]), retry,
      make('src/a-held.ts', 'needs_human', [fixed, held, unchanged], true), make('src/a-done.ts', 'done', [fixed, unchanged])];
    const result = previewResultPublication();
    const summary = result.message.split('# 修正サマリー\n')[1].split('# 修正一覧')[0];
    const details = result.message.split('# 修正一覧\n')[1];
    const paths = body => [...body.matchAll(/\[([^\]]+)\]\(<[^>]+>\)/g)].map(match => match[1]);
    const order = ['src/a-done.ts', 'src/b-done.ts', 'src/a-held.ts', 'src/z-held.ts', 'src/a-unchanged.ts'];
    assert.deepEqual(paths(summary), order);
    assert.deepEqual(paths(details), order);
    assert.match(summary, /b-done\.ts.*✅完了/);
    assert.match(summary, /a-held\.ts.*⚠️一部修正済み要確認/);
    assert.match(summary, /a-unchanged\.ts.*☑️修正不要/);
    assert.match(result.message, /📄 対象ファイル数：5  \n✅ 修正完了：2  \n⚠️ 要確認：2（うち一部修正済み：1）  \n☑️ 修正不要：1/);
    const partialSection = details.split('[src/a-held.ts]')[1].split('\n---')[0];
    assert.deepEqual([...partialSection.matchAll(/^\| (R_\w+) \|/gm)].map(match => match[1]), ['R_FIXED', 'R_HELD', 'R_UNKNOWN', 'R_UNCHANGED']);
    const retrySection = details.split('[src/b-done.ts]')[1].split('\n---')[0];
    assert.match(retrySection, /\| R_FIXED \| ✅完了 \|/);
  } finally { previewState.tasks = saved; }
});
