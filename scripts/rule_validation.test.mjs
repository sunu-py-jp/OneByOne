import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from '../frontend/node_modules/typescript/lib/typescript.js';

async function load(name) {
  const source = await readFile(new URL(`../frontend/src/${name}`, import.meta.url), 'utf8');
  const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } });
  return import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);
}
const { ruleDraftErrors, ruleIdError, nextRuleId, isCommonRule } = await load('rule-validation.ts');
const { ruleScope, ruleScopeLabels } = await load('rule-scope.ts');

test('rule metadata requires an id, a name and a description, including whitespace-only drafts', () => {
  assert.deepEqual(ruleDraftErrors({ id: '1', name: 'Save safely', description: 'Preserve errors and the calling contract' }), []);
  assert.equal(ruleDraftErrors({ id: '', name: '', description: '' }).length, 3);
  assert.deepEqual(ruleDraftErrors({ id: '1', name: '　\t', description: 'Valid' }), ['名称を入力してください。']);
  assert.deepEqual(ruleDraftErrors({ id: '1', name: 'Valid', description: '\n\r ' }), ['説明を入力してください。']);
});

test('a new rule id must be a safe identifier that no saved rule uses', () => {
  const taken = new Set(['1', 'r019']);
  assert.equal(ruleIdError('2', taken), '');
  assert.equal(ruleIdError(' ', taken), 'IDを入力してください。');
  for (const bad of ['-1', 'R 1', 'ルール1', 'a'.repeat(65)]) assert.match(ruleIdError(bad, taken), /英数字で始まる/);
  assert.equal(ruleIdError('R019', taken), '同じIDのルールが既にあります。');
  assert.equal(ruleIdError('R019'), '', 'a saved rule keeps its own id');
});

test('new ids continue the numeric sequence without reusing any id', () => {
  assert.equal(nextRuleId([]), '1');
  assert.equal(nextRuleId(['1', '2', '13']), '14');
  assert.equal(nextRuleId(['R019', 'api']), '1');
  assert.equal(nextRuleId(['007', '1']), '8');
});

test('pattern state distinguishes path, content, both and common rules', () => {
  assert.equal(ruleScope({ pathPattern: '', contentPattern: '' }), 'common');
  assert.equal(ruleScope({ pathPattern: 'src/**/*.tsx', contentPattern: ' ' }), 'path');
  assert.equal(ruleScope({ pathPattern: '', contentPattern: '\\bStore\\b' }), 'content');
  assert.equal(ruleScope({ pathPattern: '**/*.js', contentPattern: 'save\\(' }), 'both');
  assert.deepEqual(ruleScopeLabels, { common: '共通', path: 'パス', content: '内容', both: 'パス＋内容' });
  assert.equal(isCommonRule({ pathPattern: '', contentPattern: '' }), true);
  assert.equal(isCommonRule({ pathPattern: 'src/**', contentPattern: '' }), false);
});
