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
  stdin: { contents: 'export { SourceDiagnostics } from "./src/SourceDiagnostics"; export { DiagnosticsSettings } from "./src/DiagnosticsSettings"; export { resultSourceDiagnostics } from "./src/diagnostics"; export { emptyState, normalizeState } from "./src/types";', resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
});
const module = { exports: {} };
new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(requireFrontend, module, module.exports);
const { SourceDiagnostics, DiagnosticsSettings, resultSourceDiagnostics, emptyState, normalizeState } = module.exports;
const diagnostic = (message, extra = {}) => ({ file: 'src/app.ts', message, ...extra });

test('normalization preserves imported context and immutable attempt context with nullable Go slices', () => {
  const input = { ...emptyState, config: { ...emptyState.config, diagnosticsPath: '/workspace/diagnostics.jsonl', diagnosticsOnly: true },
    diagnostics: { name: 'errors.jsonl', count: 1, fileCount: 1 },
    rules: [{ id: 'R001', diagnosticPattern: 'TS2304' }],
    tasks: [{ file: 'src/app.ts', sourceDiagnostics: [diagnostic('new import', { ruleIds: null })], history: [
      { id: 'attempt-1', sourceDiagnostics: [diagnostic('earlier import', { line: 5, code: 'TS2304', ruleIds: ['R001'] })] },
      { id: 'attempt-2', sourceDiagnostics: null },
    ] }],
  };
  const normalized = normalizeState(input);
  assert.deepEqual(normalized.diagnostics, input.diagnostics);
  assert.equal(normalized.config.diagnosticsPath, input.config.diagnosticsPath);
  assert.equal(normalized.config.diagnosticsOnly, true);
  assert.equal(normalized.rules[0].diagnosticPattern, 'TS2304');
  assert.deepEqual(normalized.tasks[0].sourceDiagnostics, [diagnostic('new import', { ruleIds: [] })]);
  assert.deepEqual(normalized.tasks[0].history[0].sourceDiagnostics, input.tasks[0].history[0].sourceDiagnostics);
  assert.deepEqual(normalized.tasks[0].history[1].sourceDiagnostics, []);
  assert.equal(normalizeState({ ...emptyState, config: {} }).config.diagnosticsOnly, false);
});

test('result diagnostics follow the selected attempt and never borrow a replacement import', () => {
  const task = { sourceDiagnostics: [diagnostic('new import')], history: [
    { sourceDiagnostics: [diagnostic('first attempt')] }, { sourceDiagnostics: [diagnostic('second attempt')] },
  ] };
  assert.deepEqual(resultSourceDiagnostics(task), task.history[1].sourceDiagnostics);
  assert.deepEqual(resultSourceDiagnostics(task, 0), task.history[0].sourceDiagnostics);
  assert.deepEqual(resultSourceDiagnostics(task, 1), task.history[1].sourceDiagnostics);
  assert.deepEqual(resultSourceDiagnostics(task, 2), []);
  assert.deepEqual(resultSourceDiagnostics({ ...task, history: [{}] }), []);
  assert.deepEqual(resultSourceDiagnostics({ ...task, history: [] }), task.sourceDiagnostics);
  task.sourceDiagnostics = [];
  assert.equal(resultSourceDiagnostics(task, 0)[0].message, 'first attempt');
});

test('diagnostic display retains unmatched records and escapes imported text', () => {
  const html = renderToStaticMarkup(createElement(SourceDiagnostics, { diagnostics: [
    diagnostic('<script>unsafe()</script>', { line: 12, code: 'TS2304', ruleIds: ['R001'] }), diagnostic('unmatched warning'),
  ], rules: [{ id: 'R001', title: '名前の置き換え' }], onOpenRule() {}, caption: '試行 1 開始時の記録' }));
  assert.match(html, /取り込んだ診断/);
  assert.match(html, /src\/app\.ts:12/);
  assert.match(html, /TS2304/);
  assert.match(html, /R001 名前の置き換えの詳細/);
  assert.match(html, /診断条件に一致するルールなし/);
  assert.match(html, /unmatched warning/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.doesNotMatch(html, /<details[^>]*\sopen/);
  assert.equal(renderToStaticMarkup(createElement(SourceDiagnostics, {})), '');
});

test('diagnostic settings show summary and disable file-only filtering without imported diagnostics', () => {
  const props = { only: false, disabled: false, onImport() {}, onClear() {}, onOnlyChange() {} };
  const empty = renderToStaticMarkup(createElement(DiagnosticsSettings, props));
  assert.match(empty, /未取込/);
  assert.match(empty, /取り込む/);
  assert.match(empty, /type="checkbox" disabled=""/);
  assert.doesNotMatch(empty, />解除</);
  const imported = renderToStaticMarkup(createElement(DiagnosticsSettings, { ...props, only: true, path: '/app/diagnostics.jsonl', diagnostics: { name: 'errors.jsonl', count: 4, fileCount: 2 } }));
  assert.match(imported, /4 件 · 2 ファイル/);
  assert.match(imported, /errors\.jsonl/);
  assert.match(imported, /差し替え/);
  assert.match(imported, />解除</);
  assert.match(imported, /type="checkbox" checked=""/);
  assert.doesNotMatch(imported, /type="checkbox" disabled/);
  const locked = renderToStaticMarkup(createElement(DiagnosticsSettings, { ...props, disabled: true, diagnostics: { name: 'errors.jsonl', count: 4, fileCount: 2 } }));
  assert.match(locked, /type="checkbox" disabled=""/);
  assert.equal((locked.match(/<button[^>]+disabled=""/g) || []).length, 2);
});
