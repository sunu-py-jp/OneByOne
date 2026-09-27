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
  stdin: { contents: 'export { ResultsPanel, ChangeReport, ResultCode, resolveResultDetailTab, withChangeReport, resultChanges, HeldChangeMenu, heldChangeDestination } from "./src/ResultsPanel"; export { recordedChanges, rulesForLine, changeLineLabel, ruleOrigins, heldSourceTarget, changeNote } from "./src/result-line-rules"; export { emptyState, emptyUsage, normalizeState } from "./src/types";', resolveDir: frontend },
  bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'no-native-connection', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /.*/, namespace: 'test' }, () => ({ contents: 'export const api = new Proxy({}, { get() { throw new Error("Native API must not run in this test"); } });' }));
  } }],
});
const module = { exports: {} };
new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(requireFrontend, module, module.exports);
const { ResultsPanel, ChangeReport, ResultCode, resolveResultDetailTab, withChangeReport, resultChanges, recordedChanges, rulesForLine, changeLineLabel, ruleOrigins, HeldChangeMenu, heldChangeDestination, heldSourceTarget, changeNote, emptyState, emptyUsage, normalizeState } = module.exports;
const noOp = () => {};
const item = (id, status, extra = {}) => ({
  id, attributionVersion: 2, ruleId: 'R101', ruleTitle: '保存後に通知する', location: 'notifyCustomer()',
  risk: '保存失敗時に通知だけ届く', change: '保存成功後に通知を移動する', expected: '通知前に保存が完了する',
  status, reason: '検証結果に基づく判定', ...extra,
});
const attempt = (changes, extra = {}) => ({
  id: 'attempt-1', number: 1, startedAt: '', finishedAt: '', outcome: 'needs_human', note: '仕様判断が必要です。',
  rulesApplied: ['R101'], checks: [], usage: emptyUsage, diffPath: '', commit: '', changes, ...extra,
});
function render(history, tab = 'diff', rules = []) {
  const task = { file: 'src/dispatch.js', rules: ['R101'], status: history.at(-1)?.outcome || 'pending', attempts: history.length, rulesApplied: [], note: '', inputHash: '', updatedAt: '', history };
  const state = normalizeState({ ...emptyState, tasks: [task], rules });
  return renderToStaticMarkup(createElement(ResultsPanel, { state, busy: false, usable: true, canSetup: true, selectedFile: task.file, detailTab: tab, onSelectFile: noOp, onDetailTabChange: noOp, onStop: noOp, onExport: noOp, onRetry: noOp, onOpenWorktree: noOp, onSetup: noOp }));
}

function renderReport(record) {
  return renderToStaticMarkup(createElement(ChangeReport, { attempt: record, onOpenRule: noOp }));
}

test('a held file reports unadopted fixes separately from human decisions without claiming a fix', () => {
  const html = renderReport(attempt([item('notification', 'not_applied'), item('contract', 'needs_human', { ruleId: 'R102', risk: '公開契約が変わる', change: '公開メソッドの非同期化を判断する' })]));
  assert.match(html, /未反映/);
  assert.match(html, /要確認/);
  assert.match(html, /この試行の修正は未採用です。/);
  assert.doesNotMatch(html, /修正済み/);
  assert.match(html, /保存失敗時に通知だけ届く/);
  assert.match(html, /公開契約が変わる/);
  assert.match(html, /通知前に保存が完了する/);
  assert.match(html, /判断理由/);
  assert.equal((html.match(/class="results-change-item"/g) || []).length, 2);
  assert.doesNotMatch(html, /<details class="results-change-item" open/);
});

test('committed per-change results keep the supplied statuses and the recorded rule identity', () => {
  const html = renderReport(attempt([item('notification', 'fixed'), item('contract', 'needs_human')], { commit: '1234567890' }));
  assert.match(html, /修正済み/);
  assert.match(html, /要確認/);
  assert.doesNotMatch(html, /この試行の修正は未採用です。/);
  assert.match(html, /R101 · 保存後に通知する/);
  assert.doesNotMatch(html, /前 \d|後 \d/);
});

test('reports stay in changes view without switching the current tab', () => {
  assert.equal(resolveResultDetailTab('diff', false, false), 'diff');
  assert.equal(resolveResultDetailTab('diff', true, false), 'diff');
  assert.equal(resolveResultDetailTab('diff', true, true), 'diff');
  assert.equal(resolveResultDetailTab('checks', true, true), 'checks');
  assert.equal(resolveResultDetailTab('history', true, false), 'history');
  assert.equal(resolveResultDetailTab('changes', false, false), 'diff');
  assert.equal(resolveResultDetailTab('changes', true, true), 'diff');
});

test('default scope waits for the cumulative report without showing latest-only ranges', () => {
  const old = attempt([item('old', 'fixed', { risk: '以前の試行のリスク', lineRanges: [{ beforeStart: 1, beforeEnd: 1, afterStart: 1, afterEnd: 1 }] })], { id: 'old', outcome: 'done', commit: 'old-commit' });
  const latest = attempt([item('latest', 'needs_human', { risk: '最新の試行のリスク' })]);
  const history = [old, latest];
  const currentHTML = render(history);
  assert.match(currentHTML, /全体（開始前 → 現在）/);
  assert.match(currentHTML, /id="results-tab-diff"[^>]*aria-selected="true"/);
  assert.doesNotMatch(currentHTML, /id="results-tab-changes"/);
  assert.match(currentHTML, /読み込み中/);
  assert.doesNotMatch(currentHTML, /最新の試行のリスク|以前の試行のリスク/);
  assert.deepEqual(resultChanges(history, -1, null), []);
  const cumulative = [{ ...old.changes[0], id: 'old:old', lineRanges: [{ beforeStart: 1, beforeEnd: 1, afterStart: 4, afterEnd: 4 }] }, latest.changes[0]];
  const detail = { cumulative: true, changes: cumulative };
  assert.deepEqual(resultChanges(history, -1, detail), cumulative);
  const cumulativeHTML = renderToStaticMarkup(createElement(ChangeReport, { changes: resultChanges(history, -1, detail), cumulative: true, onOpenRule: noOp }));
  assert.match(cumulativeHTML, /以前の試行のリスク/);
  assert.match(cumulativeHTML, /最新の試行のリスク/);
  assert.doesNotMatch(cumulativeHTML, /前 \d|後 \d/);
  assert.doesNotMatch(cumulativeHTML, /この試行の修正は未採用/);
  assert.deepEqual(resultChanges(history, 0, detail), old.changes.map(item => ({ ...item, origin: "previous" })));
  assert.deepEqual(resultChanges(history, 1, detail), latest.changes);
  const historicalHTML = renderReport(old);
  assert.match(historicalHTML, /以前の試行のリスク/);
  assert.doesNotMatch(historicalHTML, /最新の試行のリスク/);
  assert.doesNotMatch(historicalHTML, /前 \d|後 \d/);
  assert.deepEqual(resultChanges(history, -1, { cumulative: true, changes: [] }), []);
  assert.deepEqual(resultChanges(history, -1, { cumulative: false, changes: latest.changes }), []);
  const legacyHTML = render([attempt(null)]);
  assert.doesNotMatch(legacyHTML, /id="results-tab-changes"/);
  assert.match(legacyHTML, /id="results-tab-diff"[^>]*aria-selected="true"/);
});

test('missing risk remains unrecorded and report text is escaped', () => {
  const html = renderReport(attempt([item('unknown', 'pending', { risk: '', change: '<script>alert("risk")</script>' }), item('no-change', 'unchanged')], { outcome: 'running' }));
  assert.match(html, /<dt>リスク<\/dt><dd>未記録<\/dd>/);
  assert.match(html, /確認中/);
  assert.match(html, /変更不要/);
  assert.match(html, /&lt;script&gt;/);
  assert.doesNotMatch(html, /<script>/);
  assert.doesNotMatch(html, /この試行の修正は未採用です。/);
});

test('a missing historical rule title stays unrecorded and the link identifies the current definition', () => {
  const html = renderReport(attempt([item('past', 'needs_human', { ruleTitle: '' })]));
  assert.match(html, /R101 · 名称未記録/);
  assert.doesNotMatch(html, /今のルールの新しい名称/);
  assert.match(html, /title="現在のルールを表示" aria-label="R101 の現在のルールを表示"/);
});

test('lazy reports supplement only a matching historical attempt and cannot overwrite live results', () => {
  const snapshot = attempt([], { id: 'first', note: '最新の説明' });
  const older = attempt([item('old', 'not_applied')], { id: 'older' });
  const loaded = attempt([item('held', 'needs_human')], { id: 'first', note: '古い説明' });
  const merged = withChangeReport(snapshot, { history: [older, loaded] });
  assert.deepEqual(merged.changes, loaded.changes);
  assert.equal(merged.note, '最新の説明');
  assert.equal(withChangeReport(snapshot, { history: [older] }), snapshot);
  const live = { ...snapshot, changes: [item('new', 'pending')] };
  assert.equal(withChangeReport(live, { history: [loaded] }), live);
  assert.equal(withChangeReport(snapshot, { history: [{ ...loaded, outcome: 'done', commit: 'new-commit' }] }), snapshot);
});

test('empty unchanged records stay hidden while held reasons survive without a planned change', () => {
  const absent = [item('empty', 'unchanged', { change: '', reason: 'already correct' }), item('space', 'needs_human', { change: ' \n ' })];
  const html = render([attempt(absent)]);
  assert.doesNotMatch(html, /id="results-tab-changes"|対応内容未記録|results-change-item/);
  assert.match(html, /id="results-tab-diff"[^>]*aria-selected="true"/);
  const withHold = renderReport(attempt([...absent, item('hold', 'needs_human', { change: '通知をいつ送るべきか確認する' })]));
  assert.equal((withHold.match(/class="results-change-item"/g) || []).length, 2);
  assert.match(withHold, /要確認<b>2<\/b>/);
  assert.doesNotMatch(withHold, /class="results-change-status change-unchanged"|対応内容未記録/);
  assert.deepEqual(recordedChanges(absent), [absent[1]]);
});

const span = (beforeStart, beforeEnd, afterStart, afterEnd) => ({ beforeStart, beforeEnd, afterStart, afterEnd });

test('only persisted exact spans identify rules and old prose locations cannot create markers', () => {
  const changes = [
    item('first', 'fixed', { lineRanges: [span(5, 5, 7, 8)] }),
    item('same-rule', 'fixed', { lineRanges: [span(5, 5, 7, 7)] }),
    item('second', 'fixed', { ruleId: 'R102', ruleTitle: '通知順序', lineRanges: [span(5, 5, 7, 7)] }),
    item('old', 'fixed', { ruleId: 'R103', location: '5行目', lineRanges: undefined }),
    item('invalid', 'fixed', { ruleId: 'R104', lineRanges: [span(5, 4, -1, 20)] }),
  ];
  assert.deepEqual(rulesForLine(changes, 'before', 5).map(rule => rule.ruleId), ['R101', 'R102']);
  assert.deepEqual(rulesForLine(changes, 'after', 7).map(rule => rule.ruleId), ['R101', 'R102']);
  assert.deepEqual(rulesForLine(changes, 'after', 8).map(rule => rule.ruleId), ['R101']);
  assert.deepEqual(rulesForLine(changes, 'before', 7), []);
  assert.deepEqual(rulesForLine(changes, 'after', 0), []);
  assert.equal(changeLineLabel([span(5, 5, 7, 8)]), '前 5 → 後 7–8');
});

test('diff rule chips appear once per replacement, never on context rows, and share exact source coordinates', () => {
  const diff = '@@ -4,4 +4,5 @@\n same\n-old\n+new\n+added\n tail\n end\n';
  const changes = [
    item('first', 'fixed', { lineRanges: [span(5, 5, 5, 6)] }),
    item('second', 'fixed', { ruleId: 'R102', ruleTitle: '<unsafe title>', lineRanges: [span(5, 5, 5, 6)] }),
    item('context', 'fixed', { ruleId: 'R999', lineRanges: [span(4, 4, 4, 4)] }),
  ];
  const html = renderToStaticMarkup(createElement(ResultCode, { content: diff, isDiff: true, changes, onOpenRule: noOp }));
  assert.doesNotMatch(html, /<thead>|<th\b/);
  assert.match(html, /class="removed"><td class="results-line-number">5<\/td>/);
  assert.equal((html.match(/class="results-code-rules-row"/g) || []).length, 1, 'one row for the removed/added replacement together');
  assert.equal((html.match(/<code>R101<\/code>/g) || []).length, 1);
  assert.equal((html.match(/<code>R102<\/code>/g) || []).length, 1);
  assert.match(html, /class="results-code-unplaced-row"[\s\S]*?R999/);
  assert.doesNotMatch(html.slice(html.indexOf('<tr class="hunk"')), /R999/, "unlocated explanations are not assigned to a specific change");
  assert.doesNotMatch(html, /results-change-report|<details/);
  assert.match(html, /title="R101 · 保存後に通知する"/);
  assert.match(html, /&lt;unsafe title&gt;/);
  assert.match(html, /<\/div><\/td><\/tr><tr class="removed">/);
  assert.doesNotMatch(html, /<\/div><\/td><\/tr><tr class="added">/);
  const plain = renderToStaticMarkup(createElement(ResultCode, { content: 'one\ntwo\nthree\nfour\nnew\nadded', isDiff: false, view: 'after', changes: changes.slice(0, 2), onOpenRule: noOp }));
  assert.equal((plain.match(/class="results-code-rules-row"/g) || []).length, 1);
  const noAnchors = renderToStaticMarkup(createElement(ResultCode, { content: diff, isDiff: true, changes: [item('legacy', 'fixed', { location: '5行目' })] }));
  assert.doesNotMatch(noAnchors, /results-code-rules-row/);
});

test('a rule used in separate changes remains visible at every change block', () => {
  const diff = '@@ -5,3 +5,3 @@\n-first\n+firstUpdated\n context\n-second\n+secondUpdated\n@@ -20 +20 @@\n-third\n+thirdUpdated\n';
  const changes = [item('repeated', 'fixed', { lineRanges: [span(5, 5, 5, 5), span(7, 7, 7, 7), span(20, 20, 20, 20)] })];
  const html = renderToStaticMarkup(createElement(ResultCode, { content: diff, isDiff: true, changes }));
  assert.equal((html.match(/<code>R101<\/code>/g) || []).length, 3, 'context and hunk boundaries preserve independent rule markers');
  assert.equal((html.match(/<\/div><\/td><\/tr><tr class="removed">/g) || []).length, 3);
});

test('new rules on added rows retain their exact location without repeating removed-side rules', () => {
  const diff = '@@ -5,2 +5,2 @@\n-first\n-second\n+firstUpdated\n+secondUpdated\n';
  const changes = [
    item('first', 'fixed', { lineRanges: [span(5, 5, 5, 5)] }),
    item('second', 'fixed', { ruleId: 'R102', lineRanges: [span(6, 6, 6, 6)] }),
    item('addition', 'fixed', { ruleId: 'R103', lineRanges: [span(0, 0, 6, 6)] }),
  ];
  const html = renderToStaticMarkup(createElement(ResultCode, { content: diff, isDiff: true, changes }));
  for (const id of ['R101', 'R102', 'R103']) {
    assert.equal((html.match(new RegExp(`<code>${id}<\\/code>`, 'g')) || []).length, 1);
  }
  assert.match(html, /<code>R103<\/code>[\s\S]*?<\/div><\/td><\/tr><tr class="removed"><td class="results-line-number">6<\/td>/);
});

test('missing final newline markers do not duplicate a replacement rule', () => {
  const diff = '@@ -1 +1 @@\n-old\n\\ No newline at end of file\n+new\n\\ No newline at end of file\n';
  const changes = [item('newline', 'fixed', { lineRanges: [span(1, 1, 1, 1)] })];
  const html = renderToStaticMarkup(createElement(ResultCode, { content: diff, isDiff: true, changes }));
  assert.equal((html.match(/<code>R101<\/code>/g) || []).length, 1);
  assert.equal((html.match(/No newline at end of file/g) || []).length, 2);
});

test('standalone before and after views keep their own rule markers', () => {
  const changes = [item('replacement', 'fixed', { lineRanges: [span(2, 3, 3, 4)] })];
  const before = renderToStaticMarkup(createElement(ResultCode, { content: 'context\nold\noldMore\nend', isDiff: false, view: 'before', changes }));
  const after = renderToStaticMarkup(createElement(ResultCode, { content: 'context\nextra\nnew\nnewMore\nend', isDiff: false, view: 'after', changes }));
  assert.equal((before.match(/<code>R101<\/code>/g) || []).length, 1);
  assert.equal((after.match(/<code>R101<\/code>/g) || []).length, 1);
  assert.match(before, /<\/div><\/td><\/tr><tr class="source"[^>]*><td class="results-line-number">2<\/td>/);
  assert.match(after, /<\/div><\/td><\/tr><tr class="source"[^>]*><td class="results-line-number">3<\/td>/);
});

test('pure insertion/deletion leaves the absent side unmarked without prose line labels', () => {
  const deletion = item('delete', 'fixed', { lineRanges: [span(9, 10, 0, 0)] });
  const insertion = item('insert', 'fixed', { ruleId: 'R102', lineRanges: [span(0, 0, 12, 14)] });
  assert.deepEqual(rulesForLine([deletion, insertion], 'before', 12), []);
  assert.deepEqual(rulesForLine([deletion, insertion], 'after', 9), []);
  const html = renderToStaticMarkup(createElement(ChangeReport, { attempt: attempt([deletion, insertion]), onOpenRule: noOp }));
  assert.doesNotMatch(html, /前 \d|後 \d/);
  assert.match(html, /R101/);
  assert.match(html, /R102/);
});


test('rule badges distinguish latest accepted fixes from earlier fixes without changing history', () => {
  const old = attempt([item('old', 'fixed')], { id: 'first', outcome: 'done', commit: 'first-commit' });
  const latest = attempt([item('new', 'fixed')], { id: 'second', outcome: 'done', commit: 'second-commit' });
  const snapshot = structuredClone([old, latest]);
  const detail = { cumulative: true, changes: [
    { ...old.changes[0], sourceAttemptId: old.id, lineRanges: [span(1, 1, 1, 1)] },
    { ...latest.changes[0], sourceAttemptId: latest.id, lineRanges: [span(3, 3, 3, 3)] },
  ] };
  const changes = resultChanges([old, latest], -1, detail);
  assert.deepEqual(changes.map(item => item.origin), ['previous', 'latest']);
  const report = renderToStaticMarkup(createElement(ChangeReport, { changes, cumulative: true, onOpenRule: noOp }));
  assert.match(report, /<code data-change-origin="previous"[^>]*過去の実行で修正済み/);
  assert.match(report, /<code data-change-origin="latest"[^>]*選択した実行で修正/);
  assert.match(report, /ルールIDの色の意味/);
  assert.match(report, /この実行の修正/);
  assert.match(report, /以前の修正/);
  const diff = '@@ -1,3 +1,3 @@\n-old\n+changed\n context\n-other\n+latest\n';
  const code = renderToStaticMarkup(createElement(ResultCode, { content: diff, isDiff: true, changes }));
  assert.equal((code.match(/<code>R101<\/code>/g) || []).length, 2);
  assert.equal((code.match(/data-change-origin="previous"/g) || []).length, 1);
  assert.equal((code.match(/data-change-origin="latest"/g) || []).length, 1);
  assert.equal(ruleOrigins(changes).get('R101'), 'mixed');
  assert.deepEqual([old, latest], snapshot);
  assert.equal(detail.changes[0].origin, undefined);
});

test('no-op and failed retries leave all earlier accepted IDs in the previous color', () => {
  const old = attempt([item('old', 'fixed')], { id: 'first', outcome: 'done', commit: 'accepted' });
  for (const outcome of ['skipped', 'failed', 'needs_human', 'running']) {
    const retry = attempt([item('retry', outcome === 'skipped' ? 'unchanged' : 'not_applied')], { id: 'retry', outcome });
    const changes = resultChanges([old, retry], -1, { cumulative: true, changes: [
      { ...old.changes[0], sourceAttemptId: old.id }, { ...retry.changes[0], sourceAttemptId: retry.id },
    ] });
    assert.equal(changes[0].origin, 'previous');
    assert.equal(changes[1].origin, undefined, 'unadopted or unchanged work is never colored as a latest fix');
    assert.equal(resultChanges([old, retry], 0)[0].origin, 'previous');
  }
  assert.equal(resultChanges([old], 0)[0].origin, 'latest');
  assert.equal(resultChanges([old], -1, { cumulative: true, changes: old.changes })[0].origin, undefined, 'missing provenance stays neutral');
});

test('changes at the same location merge old and latest IDs without duplicating badges', () => {
  const changes = [
    item('old', 'fixed', { origin: 'previous', lineRanges: [span(1, 1, 1, 1)] }),
    item('new', 'fixed', { origin: 'latest', lineRanges: [span(1, 1, 1, 1)] }),
  ];
  const html = renderToStaticMarkup(createElement(ResultCode, { content: '@@ -1 +1 @@\n-old\n+new\n', isDiff: true, changes }));
  assert.equal((html.match(/<code>R101<\/code>/g) || []).length, 1);
  assert.match(html, /data-change-origin="mixed"/);
  assert.match(html, /選択した実行と以前の実行で修正/);
  assert.match(html, /<\/div><\/td><\/tr><tr class="removed">/);
});

test('source view shows timing changes on adjacent lines even when the rule ID is the same', () => {
  const changes = [
    item('old', 'fixed', { origin: 'previous', lineRanges: [span(1, 1, 1, 1)] }),
    item('new', 'fixed', { origin: 'latest', lineRanges: [span(2, 2, 2, 2)] }),
  ];
  const html = renderToStaticMarkup(createElement(ResultCode, { content: 'first\nsecond', isDiff: false, view: 'after', changes }));
  assert.equal((html.match(/<code>R101<\/code>/g) || []).length, 2);
  assert.match(html, /data-change-origin="previous"/);
  assert.match(html, /data-change-origin="latest"/);
});

test('different changes of one rule stay beside their own added lines within one diff block', () => {
  const changes = [
    item('api', 'fixed', { change: 'APIの呼び出しを置き換えた', lineRanges: [span(1, 1, 1, 1)] }),
    item('flush', 'fixed', { change: '通知の前にflushを追加した', lineRanges: [span(0, 0, 2, 2)] }),
  ];
  const html = renderToStaticMarkup(createElement(ResultCode, { content: '@@ -1 +1,2 @@\n-writer.add(data);\n+writer.write(data);\n+await writer.flush();\n', isDiff: true, changes }));
  assert.equal((html.match(/<code>R101<\/code>/g) || []).length, 2);
  assert.equal((html.match(/APIの呼び出しを置き換えた/g) || []).length, 1);
  assert.equal((html.match(/通知の前にflushを追加した/g) || []).length, 1);
  assert.ok(html.indexOf('APIの呼び出しを置き換えた') < html.indexOf('writer.write(data);'));
  assert.ok(html.indexOf('writer.write(data);') < html.indexOf('通知の前にflushを追加した'));
  assert.ok(html.indexOf('通知の前にflushを追加した') < html.indexOf('await writer.flush();'));
});

test('old broad line ranges stay inside the diff as unlocated notes without implying exact attribution', () => {
  const change = item('old', 'fixed', { attributionVersion: undefined, change: '以前の保存処理の修正', lineRanges: [span(1, 50, 1, 50)] });
  assert.deepEqual(rulesForLine([change], 'after', 1), []);
  const html = renderToStaticMarkup(createElement(ResultCode, { content: '@@ -1 +1 @@\n-old\n+new\n', isDiff: true, changes: [change] }));
  assert.match(html, /以前の保存処理の修正/);
  const code = html.slice(html.indexOf('<div class="results-code is-diff"'));
  assert.match(code, /results-code-unplaced-row/);
  assert.match(code, /位置未特定の対応記録/);
  assert.doesNotMatch(code, /class="results-code-rules-row"|<details|results-change-report/);
});


test('merged change rows show a rule id and concrete edits without descriptions, adoption boilerplate or decision details', () => {
  const changes = [
    item('import', 'fixed', { change: 'ReportWriterのimportを削除した', reason: '機械検証と独立レビューを通過した修正をコミットしました。', lineRanges: [span(1, 1, 1, 1)] }),
    item('new-import', 'fixed', { change: 'parcel-client.jsからreportsをimportした', reason: '検証結果に基づく判定', lineRanges: [span(0, 0, 1, 1)] }),
    item('hold', 'needs_human', { ruleId: 'R102', change: '契約の変更を確認する', reason: '呼び出し元の契約が確認できない' }),
  ];
  const rules = new Map([['R101', { id: 'R101', title: 'importを整理する', summary: '旧API移行後に不要なimportを除去し、新APIのimportを重複なく配置する' }]]);
  const html = renderToStaticMarkup(createElement(ResultCode, { content: '@@ -1 +1 @@\n-old\n+updated\n', isDiff: true, changes, rules }));
  assert.equal((html.match(/<code>R101<\/code>/g) || []).length, 1);
  assert.equal((html.match(/class="results-line-rule"/g) || []).length, 2, 'one located rule group and one unlocated human decision');
  for (const text of ['ReportWriterのimportを削除した', 'parcel-client.jsからreportsをimportした', '呼び出し元の契約が確認できない', '要確認', '修正済み']) assert.ok(html.includes(text), text);
  assert.match(html, /<code>R101<\/code>[\s\S]*?：[\s\S]*?ReportWriterのimportを削除した/);
  assert.doesNotMatch(html, /旧API移行後に不要なimportを除去し、新APIのimportを重複なく配置する/);
  const code = html.slice(html.indexOf('<div class="results-code is-diff"'));
  assert.doesNotMatch(code, /機械検証と独立レビューを通過した修正をコミットしました。|検証結果に基づく判定|判断の詳細|保存失敗時に通知だけ届く|通知前に保存が完了する/);
  assert.doesNotMatch(html, /前 \d|後 \d/);
});

test('cumulative split coordinates show the rule id and concrete fix above the replacement only once', () => {
  const changes = [item('repair', 'fixed', { change: '保存後に完了通知を移動した', lineRanges: [span(3, 3, 0, 0), span(0, 0, 3, 3)] })];
  const rules = new Map([['R101', { id: 'R101', title: '順序を修正', summary: '保存完了後にだけ完了通知を送信する' }]]);
  const html = renderToStaticMarkup(createElement(ResultCode, { content: '@@ -3 +3 @@\n-notify(); save();\n+save(); notify();\n', isDiff: true, changes, rules }));
  assert.equal((html.match(/class="results-code-rules-row"/g) || []).length, 1);
  assert.equal((html.match(/<code>R101<\/code>/g) || []).length, 1);
  assert.match(html, /<code>R101<\/code>[\s\S]*?：[\s\S]*?保存後に完了通知を移動した/);
  assert.doesNotMatch(html, /保存完了後にだけ完了通知を送信する/);
  assert.ok(html.indexOf('保存後に完了通知を移動した') < html.indexOf('-notify(); save();'));
  assert.doesNotMatch(html, /results-change-report|<details|位置未特定/);
});


test('held reasons appear at verified unchanged context and source lines even without a change or rule id', () => {
  const held = item('held', 'needs_human', { ruleId: '', change: '', reason: '送信先の契約を確認してください', lineRanges: [span(2, 3, 2, 3)] });
  const diff = '@@ -1,4 +1,4 @@\n-old\n+new\n deliver();\n acknowledge();\n end();\n';
  const html = renderToStaticMarkup(createElement(ResultCode, { content: diff, isDiff: true, changes: [held] }));
  assert.equal((html.match(/送信先の契約を確認してください/g) || []).length, 1, 'continuous context does not repeat the same hold');
  assert.ok(html.indexOf('送信先の契約を確認してください') < html.indexOf(' deliver();'));
  assert.match(html, /change-needs_human/);
  assert.doesNotMatch(html, /<code>|位置未特定|表示範囲外/);
  for (const view of ['before', 'after']) {
    const source = renderToStaticMarkup(createElement(ResultCode, { content: 'start();\ndeliver();\nacknowledge();\nend();', isDiff: false, view, changes: [held] }));
    assert.equal((source.match(/送信先の契約を確認してください/g) || []).length, 1);
    assert.ok(source.indexOf('送信先の契約を確認してください') < source.indexOf('data-source-line="2"'));
  }
});

test('held navigation chooses a verified source coordinate and never uses prose or old broad coordinates', () => {
  const held = item('held', 'needs_human', { change: '', reason: '認証の引き継ぎ方を確認する', lineRanges: [span(40, 40, 42, 42)] });
  const counts = { before: 50, after: 55 };
  assert.deepEqual(heldSourceTarget(held, 'after', counts), { side: 'after', line: 42 });
  assert.deepEqual(heldSourceTarget(held, 'before', counts), { side: 'before', line: 40 });
  assert.deepEqual(heldSourceTarget(held, 'after', { ...counts, after: 3 }), { side: 'before', line: 40 });
  assert.equal(heldSourceTarget({ ...held, attributionVersion: undefined, location: '42行目' }, 'after', counts), null);
  const detail = { before: 'line\n'.repeat(50), after: 'line\n'.repeat(55), cumulative: true };
  const menu = renderToStaticMarkup(createElement(HeldChangeMenu, { changes: [held, { ...held, id: 'old', attributionVersion: undefined }], detail, view: 'diff', onChoose: noOp }));
  assert.match(menu, /要確認 <b>2<\/b>件/);
  assert.match(menu, /変更後の 42 行目を表示/);
  assert.match(menu, /位置未特定・ファイル全体を表示/);
  assert.match(menu, /認証の引き継ぎ方を確認する/);
});

test('an unaccepted candidate-only hold opens its original attempt instead of guessing an accepted-file position', () => {
  const held = item('review-issue', 'needs_human', { lineRanges: [span(0, 0, 82, 82)] });
  const history = [attempt([], { id: 'first' }), attempt([held], { id: 'rejected' })];
  const cumulative = { ...held, id: 'attempt:rejected:0:review-issue', sourceAttemptId: 'rejected', lineRanges: [] };
  const detail = { before: 'original', after: 'accepted', cumulative: true };
  assert.deepEqual(heldChangeDestination(cumulative, detail, 'diff', history), { side: 'after', line: 82, attemptIndex: 1 });
  assert.equal(heldChangeDestination({ ...cumulative, id: 'attempt:rejected:1:review-issue' }, detail, 'diff', history), null);
  assert.equal(heldChangeDestination({ ...cumulative, sourceAttemptId: 'unknown' }, detail, 'diff', history), null);
  const menu = renderToStaticMarkup(createElement(HeldChangeMenu, { changes: [cumulative], detail, view: 'diff', history, onChoose: noOp }));
  assert.match(menu, /試行 2・変更後の 82 行目を表示/);
});

test('jumping to a held line past the normal rendering limit displays and marks the requested source window', () => {
  const held = item('late', 'needs_human', { change: '', reason: 'この値の出所を確認する', lineRanges: [span(10003, 10003, 10003, 10003)] });
  const content = Array.from({ length: 10005 }, (_, index) => `source_${index + 1}`).join('\n');
  const html = renderToStaticMarkup(createElement(ResultCode, { content, isDiff: false, view: 'after', changes: [held], focus: { line: 10003, request: 1 } }));
  assert.match(html, /class="source is-held-target" data-source-line="10003"/);
  assert.match(html, /この値の出所を確認する/);
  assert.doesNotMatch(html, /data-source-line="1"/);
});

test('partial same-rule adoption places fixed work and held scope beside their own code', () => {
  const changes = [
    item('safe', 'fixed', { change: '専用接続を保存後に解放した', lineRanges: [{ beforeStart: 1, beforeEnd: 1, afterStart: 1, afterEnd: 1 }] }),
    item('held', 'needs_human', { change: '共有接続の所有権を確認する', reason: '共有接続を解放してよいか確認してください', lineRanges: [{ beforeStart: 2, beforeEnd: 2, afterStart: 2, afterEnd: 2 }] }),
  ];
  const html = renderToStaticMarkup(createElement(ResultCode, {
    content: '@@ -1,2 +1,2 @@\n-save();\n+saveAndRelease();\n releaseShared();\n', isDiff: true, changes,
  }));
  assert.equal((html.match(/専用接続を保存後に解放した/g) || []).length, 1);
  assert.equal((html.match(/共有接続を解放してよいか確認してください/g) || []).length, 1);
  assert.ok(html.indexOf('専用接続を保存後に解放した') < html.indexOf('-save();'));
  assert.ok(html.indexOf('共有接続を解放してよいか確認してください') > html.indexOf('+saveAndRelease();'));
  assert.ok(html.indexOf('共有接続を解放してよいか確認してください') < html.indexOf(' releaseShared();'));
  assert.match(html, /change-fixed/);
  assert.match(html, /change-needs_human/);
});
