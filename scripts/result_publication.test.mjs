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
const { refreshPublicationDraft, publicationBlockReason, previewResultPublication, previewDetail, previewState } = await import(`data:text/javascript;base64,${Buffer.from(bundle.outputFiles[0].text).toString('base64')}`);
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
  const first = { ...refreshPublicationDraft(undefined, preview()), title: 'Private project title', message: 'Private content' };
  const next = refreshPublicationDraft(first, preview({ workspaceId: 'workspace-b', suggestedBranch: 'result/other', message: 'Other summary' }));
  assert.equal(next.title, '');
  assert.equal(next.message, 'Other summary');
  assert.equal(next.branch, 'result/other');
  const anotherTarget = refreshPublicationDraft(first, preview({ baseCommit: 'base-another-target', message: 'New project summary' }));
  assert.equal(anotherTarget.title, '');
  assert.equal(anotherTarget.message, 'New project summary');
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
  const expected = previewState.tasks.filter(task => previewDetail(task.file).diff).map(task => task.file);
  assert.deepEqual(result.files.map(file => file.file), expected);
  assert.ok(result.files.length > 0);
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
