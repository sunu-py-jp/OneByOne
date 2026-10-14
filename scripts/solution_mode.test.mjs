import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const requireFrontend = createRequire(new URL('../frontend/package.json', import.meta.url));
const React = requireFrontend('react');
const { renderToStaticMarkup } = requireFrontend('react-dom/server');
const bundle = await build({ absWorkingDir: frontend, stdin: { contents: `
  export { WorkspaceModeSelect, SolutionSettings, SolutionReview, SolutionResultsPanel, SolutionExecutionResultsPanel } from './src/SolutionPanel';
  export { RuleBodyEditor } from './src/RuleBodyEditor';
  export { solutionConfigurationIssue } from './src/solution';
  export { solutionRuleIdError, solutionRuleDraftErrors, ruleDraftErrors } from './src/rule-validation';
  export { workflowAvailability, workflowBlockReasons } from './src/workflow-navigation';
  export { emptyState, defaultConfig, normalizeState } from './src/types';
  export { api as testApi } from './src/bridge';
`, resolveDir: frontend }, bundle: true, write: false, platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
  plugins: [{ name: 'native-stub', setup(plugin) {
    plugin.onResolve({ filter: /^\.\/(?:src\/)?bridge$/ }, () => ({ path: 'bridge', namespace: 'test' }));
    plugin.onLoad({ filter: /.*/, namespace: 'test' }, () => ({ contents: 'export const api = {};' }));
  } }],
});
function load(react = React) {
  const module = { exports: {} };
  new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(id => id === 'react' ? react : requireFrontend(id), module, module.exports);
  return module.exports;
}
const ui = load();
const noOp = () => {};
const config = { ...ui.defaultConfig.solution, path: 'src/LegacyApp.sln' };
const render = (component, props) => renderToStaticMarkup(React.createElement(component, props));
const stateOf = extra => ui.normalizeState({ ...ui.emptyState, activeWorkspaceId: 'workspace', config: { ...ui.defaultConfig, mode: 'solution', solution: config }, ...extra });
const ready = { mode: 'solution', busy: false, running: false, available: true, targetReady: true, setupReady: true,
  connectionReady: true, readOnly: false, selectionCurrent: false, readyCount: 0, selectedCount: 0, selectedRuleCount: 1 };

test('solution confirmation bypasses per-file extraction but keeps setup, connection and permission guards', () => {
  assert.equal(ui.workflowAvailability(ready).review, true);
  for (const condition of [{ connectionReady: false }, { setupReady: false }, { targetReady: false }, { selectedRuleCount: 0 }, { readOnly: true }, { running: true }, { solutionIssue: 'Choose a solution' }]) {
    assert.equal(ui.workflowAvailability({ ...ready, ...condition }).review, false, JSON.stringify(condition));
  }
  assert.equal(ui.workflowBlockReasons({ ...ready, solutionIssue: 'Choose a solution' }).review, 'Choose a solution');
  assert.equal(ui.workflowAvailability({ ...ready, solutionIssue: 'Choose a solution' }).run, true, 'the invalid setting can still be edited');
  assert.equal(ui.workflowAvailability({ ...ready, mode: 'files' }).review, false, 'file mode still requires confirmed file selection');
});

test('solution settings validate missing sln, integer limits, explicit unlimited and long builds', () => {
  assert.equal(ui.solutionConfigurationIssue(config), '');
  assert.equal(ui.solutionConfigurationIssue({ ...config, path: 'Legacy.SLN', maxIterations: 0, buildTimeoutSeconds: 86400 }), '');
  for (const change of [{ path: '' }, { path: 'Project.vbproj' }, { configuration: ' ' }, { platform: '' }, { maxIterations: -1 }, { maxIterations: 1.5 }, { maxIterations: NaN }, { buildTimeoutSeconds: 0 }, { buildTimeoutSeconds: 86401 }]) {
    assert.notEqual(ui.solutionConfigurationIssue({ ...config, ...change }), '', JSON.stringify(change));
  }
});

test('normalization handles legacy state and nullable build arrays without losing unlimited', () => {
  const legacy = ui.normalizeState({ ...ui.emptyState, config: {} });
  assert.equal(legacy.config.mode, 'files');
  assert.equal(legacy.config.solution.maxIterations, 20);
  const state = stateOf({ config: { mode: 'solution', solution: { ...config, maxIterations: 0 } }, solutionRun: { id: 'run', files: null, builds: [{ iteration: 0, diagnostics: null }], usage: null } });
  assert.equal(state.config.solution.maxIterations, 0);
  assert.deepEqual(state.solutionRun.files, []);
  assert.deepEqual(state.solutionRun.builds[0].diagnostics, []);
  assert.equal(state.solutionRun.usage.turns, 0);
});

test('settings and confirmation expose solution options without file selection or concurrency', () => {
  const settings = render(ui.SolutionSettings, { config, workspaceId: 'workspace', disabled: false, onChange: noOp });
  assert.match(settings, /\.sln を検索/);
  assert.match(settings, /MSBuild/);
  assert.match(settings, /修正回数の上限/);
  assert.match(settings, /min="0"/);
  const review = render(ui.SolutionReview, { state: stateOf({ config: { mode: 'solution', solution: { ...config, maxIterations: 0 } } }), onEdit: noOp });
  assert.match(review, /無制限/);
  assert.match(review, /src\/LegacyApp.sln/);
  assert.match(review, /再ビルド/);
  assert.doesNotMatch(settings + review, /type="checkbox"|並列数|診断JSONL/);
  const select = render(ui.WorkspaceModeSelect, { mode: 'solution', disabled: true, onChange: noOp });
  assert.match(select, /<select[^>]*disabled/);
  assert.match(select, /value="solution" selected/);
});

test('results keep initial and repaired builds, diagnostic source locations and raw logs inspectable', () => {
  const state = stateOf({ solutionRun: { id: 'run', status: 'needs_human', iteration: 1, files: [], usage: {}, note: '修正回数の上限に達しました',
    builds: [{ iteration: 0, success: false, exitCode: 1, output: 'first build', diagnostics: [] },
      { iteration: 1, success: false, exitCode: 1, output: 'compiler <log>', diagnostics: [{ file: 'App/Program.vb', line: 14, code: 'BC30451', message: "'missing' は宣言されていません。" }] }] } });
  const html = render(ui.SolutionResultsPanel, { state, busy: false, usable: true, canSetup: true, onStop: noOp, onExport: noOp, onOpenWorktree: noOp, onSetup: noOp });
  for (const expected of ['初回ビルド', '修正 1 回目', 'BC30451', 'App/Program.vb:14', 'compiler &lt;log&gt;', '確認が必要', '修正回数の上限に達しました']) assert.ok(html.includes(expected), expected);
  assert.doesNotMatch(html, /並列数|再試行に追加/);
});

test('solution rules require only a skill-compatible id, description and content', () => {
  const rule = { id: 'vbnet-null-handling', description: 'Null 判定を統一する', body: '## 手順\n関連する呼び出し元も修正する。' };
  assert.deepEqual(ui.solutionRuleDraftErrors(rule), []);
  for (const id of ['UpperCase', 'under_score', '-leading', 'trailing-', 'two--hyphens', 'a'.repeat(65)]) assert.notEqual(ui.solutionRuleIdError(id), '', id);
  assert.notEqual(ui.solutionRuleIdError(rule.id, new Set([rule.id])), '');
  assert.equal(ui.solutionRuleIdError('a'.repeat(64)), '');
  assert.deepEqual(ui.solutionRuleDraftErrors({ id: '', description: '', body: '' }), ['IDを入力してください。', '説明を入力してください。', '内容を入力してください。']);
  assert.deepEqual(ui.ruleDraftErrors({ ...rule, name: '' }), ['名称を入力してください。'], 'file mode retains its name requirement');
  const html = render(ui.RuleBodyEditor, { label: '内容', body: rule.body, onChange: noOp });
  assert.match(html, /aria-label="ルール内容"/);
  assert.match(html, />内容<\/span>/);
});

test('solution results show independent rule assessments and cumulative usage including pending accounting', () => {
  const state = stateOf({ solutionRun: { id: 'reviewed', status: 'running', iteration: 0, files: [], builds: [], requestPending: true,
    usage: { inputTokens: 1200, cachedTokens: 400, outputTokens: 80, costUsd: 0.01, turns: 2, uncertain: true },
    reviews: [{ iteration: 0, verdict: 'needs_changes', summary: '宣言と呼び出し元の型が不一致', assessments: [{ ruleId: 'vbnet-type-safety', status: 'needs_changes', reason: '呼び出し元の型変換も必要' }], originalHashes: null, candidateHashes: null, usage: null }] } });
  const html = render(ui.SolutionResultsPanel, { state, busy: false, usable: true, canSetup: false, onStop: noOp, onExport: noOp, onOpenWorktree: noOp, onSetup: noOp });
  for (const expected of ['独立レビュー', '要修正', 'vbnet-type-safety', '呼び出し元の型変換も必要', '1,200', '400', '0.010000', 'AI応答待ち']) assert.ok(html.includes(expected), expected);
  assert.deepEqual(state.solutionRun.reviews[0].originalHashes, {});
  assert.equal(state.solutionRun.reviews[0].usage.inputTokens, 0);
});

test('historical solution diffs use the execution snapshot rather than the current worktree', async () => {
  const effects = [];
  const mockedReact = { ...React, useState: initial => [typeof initial === 'function' ? initial() : initial, noOp], useRef: initial => ({ current: initial }), useEffect: effect => effects.push(effect) };
  const module = load(mockedReact);
  const calls = [];
  module.testApi.GetExecutionFileDetail = async (...args) => { calls.push(['historical', ...args]); return {}; };
  module.testApi.GetSolutionFileDetail = async (...args) => { calls.push(['current', ...args]); return {}; };
  const props = { state: stateOf({ solutionRun: { id: 'run', status: 'completed', iteration: 1, files: ['App/Program.vb'], builds: [], usage: {} } }), busy: false, usable: true, canSetup: true, onStop: noOp, onExport: noOp, onOpenWorktree: noOp, onSetup: noOp };
  module.SolutionResultsPanel({ ...props, executionId: 'saved-execution' });
  for (const effect of effects.splice(0)) effect();
  await Promise.resolve();
  assert.deepEqual(calls, [['historical', 'saved-execution', 'App/Program.vb', -1]]);
  calls.length = 0;
  module.SolutionResultsPanel({ ...props, state: { ...props.state, running: true } });
  for (const effect of effects.splice(0)) effect();
  assert.deepEqual(calls, [], 'running builds never show a transient source snapshot as final output');
});
