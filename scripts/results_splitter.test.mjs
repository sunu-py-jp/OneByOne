import test from 'node:test';
import assert from 'node:assert/strict';
import { readFile } from 'node:fs/promises';
import ts from '../frontend/node_modules/typescript/lib/typescript.js';

const source = await readFile(new URL('../frontend/src/results-splitter.ts', import.meta.url), 'utf8');
const { outputText } = ts.transpileModule(source, { compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.ES2022 } });
const { resultSplitBounds, fitResultListWidth, clampResultListWidth, RESULTS_SPLITTER_WIDTH } = await import(`data:text/javascript;base64,${Buffer.from(outputText).toString('base64')}`);

test('initial list width fits short content and gives the detail most space for long paths', () => {
  assert.equal(fitResultListWidth(280, 1200), 280);
  for (const container of [390, 620, 960, 1440, 2400]) {
    const width = fitResultListWidth(4000, container);
    const available = container - RESULTS_SPLITTER_WIDTH;
    assert.ok(width <= 460);
    assert.ok(width < available / 2, `detail should be wider at ${container}px`);
  }
});

test('dragging at either edge preserves space for both panels and clamps again after window shrink', () => {
  const { min, max } = resultSplitBounds(1100);
  assert.equal(clampResultListWidth(-800, 1100), min);
  assert.equal(clampResultListWidth(5000, 1100), max);
  assert.ok(1100 - RESULTS_SPLITTER_WIDTH - max >= 320);
  const requestedWidth = clampResultListWidth(730, 1100);
  const smaller = clampResultListWidth(requestedWidth, 500);
  assert.ok(smaller <= resultSplitBounds(500).max);
  assert.ok(500 - RESULTS_SPLITTER_WIDTH - smaller >= 280);
  assert.equal(clampResultListWidth(requestedWidth, 1100), requestedWidth);
});

test('hidden or unusually narrow containers do not create negative tracks or inverted bounds', () => {
  for (const container of [0, 3, 9, 70, 200, 400]) {
    const { min, max, available } = resultSplitBounds(container);
    const width = fitResultListWidth(700, container);
    assert.ok(min >= 0 && max >= min && max <= available);
    assert.ok(width >= min && width <= max);
    assert.ok(available - width >= 0);
  }
});
