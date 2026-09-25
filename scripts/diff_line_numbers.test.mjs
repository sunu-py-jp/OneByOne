import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from '../frontend/node_modules/typescript/lib/typescript.js';

const source = await readFile(new URL('../frontend/src/diff.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, {
  compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 },
});
const { parseUnifiedDiff } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

function rows(content) {
  return parseUnifiedDiff(content).map(({ type, oldLine, newLine }) => [type, oldLine, newLine]);
}

test('numbers both sides from the hunk, advancing only the side present', () => {
  assert.deepEqual(rows([
    'diff --git a/file b/file',
    'index 123..456 100644',
    '--- a/file',
    '+++ b/file',
    '@@ -40,4 +40,5 @@ function example()',
    ' before',
    '-old',
    '+new first',
    '+new second',
    ' after',
    ' end',
    '',
  ].join('\n')), [
    ['meta', null, null],
    ['meta', null, null],
    ['meta', null, null],
    ['meta', null, null],
    ['hunk', null, null],
    ['context', 40, 40],
    ['removed', 41, null],
    ['added', null, 41],
    ['added', null, 42],
    ['context', 42, 43],
    ['context', 43, 44],
  ]);
});

test('resets source positions across hunks and files, including omitted counts', () => {
  assert.deepEqual(rows([
    '--- a/first',
    '+++ b/first',
    '@@ -2 +2 @@',
    '-old',
    '+new',
    '@@ -120,2 +124,2 @@',
    ' same',
    '-old again',
    '+new again',
    'diff --git a/second b/second',
    '--- a/second',
    '+++ b/second',
    '@@ -1 +1 @@',
    ' unchanged',
  ].join('\n')), [
    ['meta', null, null],
    ['meta', null, null],
    ['hunk', null, null],
    ['removed', 2, null],
    ['added', null, 2],
    ['hunk', null, null],
    ['context', 120, 124],
    ['removed', 121, null],
    ['added', null, 125],
    ['meta', null, null],
    ['meta', null, null],
    ['meta', null, null],
    ['hunk', null, null],
    ['context', 1, 1],
  ]);
});

test('handles new files, deleted files, and insertion after an existing line', () => {
  assert.deepEqual(rows([
    '--- /dev/null',
    '+++ b/new',
    '@@ -0,0 +1,2 @@',
    '+first',
    '+second',
    '--- a/deleted',
    '+++ /dev/null',
    '@@ -1,2 +0,0 @@',
    '-first',
    '-second',
    '@@ -10,0 +11 @@',
    '+inserted',
  ].join('\n')), [
    ['meta', null, null],
    ['meta', null, null],
    ['hunk', null, null],
    ['added', null, 1],
    ['added', null, 2],
    ['meta', null, null],
    ['meta', null, null],
    ['hunk', null, null],
    ['removed', 1, null],
    ['removed', 2, null],
    ['hunk', null, null],
    ['added', null, 11],
  ]);
});

test('does not mistake source text with --- or +++ prefixes for file headers', () => {
  const content = '@@ -8,2 +8,2 @@\n--- old source\n+++ new source\n tail\n--- a/next\n+++ b/next\n';
  const parsed = parseUnifiedDiff(content);
  assert.deepEqual(rows(content), [
    ['hunk', null, null],
    ['removed', 8, null],
    ['added', null, 8],
    ['context', 9, 9],
    ['meta', null, null],
    ['meta', null, null],
  ]);
  assert.equal(parsed[1].text, '--- old source');
  assert.equal(parsed[2].text, '+++ new source');
});

test('no-final-newline markers do not consume either source counter', () => {
  assert.deepEqual(rows([
    '@@ -7,2 +7,2 @@',
    ' same',
    '-before',
    '\\ No newline at end of file',
    '+after',
    '\\ No newline at end of file',
  ].join('\n')), [
    ['hunk', null, null],
    ['context', 7, 7],
    ['removed', 8, null],
    ['meta', null, null],
    ['added', null, 8],
    ['meta', null, null],
  ]);
});

test('normalizes CRLF and preserves blank source lines and whitespace', () => {
  const parsed = parseUnifiedDiff('@@ -3,2 +3,2 @@\r\n \r\n-old  \r\n+new  \r\n');
  assert.deepEqual(parsed, [
    { text: '@@ -3,2 +3,2 @@', type: 'hunk', oldLine: null, newLine: null },
    { text: ' ', type: 'context', oldLine: 3, newLine: 3 },
    { text: '-old  ', type: 'removed', oldLine: 4, newLine: null },
    { text: '+new  ', type: 'added', oldLine: null, newLine: 4 },
  ]);
});

test('removes only the final newline sentinel and leaves metadata unnumbered', () => {
  assert.deepEqual(parseUnifiedDiff(''), []);
  assert.deepEqual(parseUnifiedDiff('\n'), [
    { text: '', type: 'meta', oldLine: null, newLine: null },
  ]);
  assert.deepEqual(parseUnifiedDiff('message\n\n').map(line => line.text), ['message', '']);
  assert.deepEqual(rows('--- a/file\n+++ b/file\n+outside hunk\n-outside hunk\n context outside hunk'), [
    ['meta', null, null],
    ['meta', null, null],
    ['meta', null, null],
    ['meta', null, null],
    ['meta', null, null],
  ]);
});
