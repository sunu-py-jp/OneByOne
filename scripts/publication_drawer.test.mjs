import test from 'node:test';
import assert from 'node:assert/strict';
import { createRequire } from 'node:module';
import { fileURLToPath } from 'node:url';
import { build } from '../frontend/node_modules/esbuild/lib/main.js';

const frontend = fileURLToPath(new URL('../frontend/', import.meta.url));
const requireFrontend = createRequire(new URL('../frontend/package.json', import.meta.url));
const bundle = await build({ absWorkingDir: frontend, entryPoints: ['src/usePublicationDrawerResize.ts'], bundle: true, write: false, platform: 'node', format: 'cjs', external: ['react'] });

function harness() {
  const cells = [], cleanup = new Map();
  let cursor = 0, effects = [];
  const hooks = {
    useState(initial) { const i = cursor++; if (!(i in cells)) cells[i] = initial; return [cells[i], value => { cells[i] = typeof value === 'function' ? value(cells[i]) : value; }]; },
    useRef(initial) { return cells[cursor++] ??= { current: initial }; },
    useCallback(fn, deps) { const i = cursor++; if (!cells[i] || deps.some((value, n) => !Object.is(value, cells[i].deps[n]))) cells[i] = { fn, deps }; return cells[i].fn; },
    useEffect(fn, deps) { const i = cursor++; if (!cells[i] || deps.some((value, n) => !Object.is(value, cells[i][n]))) effects.push(() => { cleanup.get(i)?.(); cleanup.set(i, fn()); }); cells[i] = deps; },
  };
  const module = { exports: {} };
  const windowMock = { addEventListener() {}, removeEventListener() {} };
  const documentMock = { body: { classList: { add() {}, remove() {} } } };
  class Observer { observe() {} disconnect() {} }
  new Function('require', 'module', 'exports', 'window', 'document', 'ResizeObserver', bundle.outputFiles[0].text)(
    name => name === 'react' ? hooks : requireFrontend(name), module, module.exports, windowMock, documentMock, Observer);
  return { ...module.exports,
    render(workspace = 'workspace') { cursor = 0; effects = []; const result = module.exports.usePublicationDrawerResize(workspace); effects.forEach(fn => fn()); return result; },
    close() { for (const fn of cleanup.values()) fn?.(); },
  };
}

test('drawer resize works from the left edge with pointer, keyboard and cancel, and keeps small windows usable', () => {
  const h = harness();
  let state = h.render();
  state.ref({ clientWidth: 1000 }); h.render(); state = h.render();
  assert.equal(state.width, 660);
  const key = name => { state.handleProps.onKeyDown({ key: name, preventDefault() {}, stopPropagation() {} }); state = h.render(); };
  key('ArrowLeft'); assert.equal(state.width, 670);
  key('ArrowRight'); assert.equal(state.width, 660);
  key('End'); assert.equal(state.width, 960);
  key('Home'); assert.equal(state.width, 320);
  key('Enter'); assert.equal(state.width, 660);
  const handle = { captured: false, focus() {}, setPointerCapture() { this.captured = true; }, hasPointerCapture() { return this.captured; }, releasePointerCapture() { this.captured = false; } };
  state.handleProps.onPointerDown({ isPrimary: true, button: 0, pointerId: 1, clientX: 400, currentTarget: handle, preventDefault() {} }); state = h.render();
  state.handleProps.onPointerMove({ pointerId: 1, clientX: 300 }); state = h.render();
  assert.equal(state.width, 760, 'dragging left expands the right drawer');
  key('Escape'); assert.equal(state.width, 660); assert.equal(handle.captured, false);
  for (const container of [0, 80, 300, 640, 900]) {
    const bounds = h.publicationDrawerBounds(container);
    assert.ok(bounds.min >= 0 && bounds.min <= bounds.max && bounds.max <= container);
    assert.equal(h.clampPublicationDrawerWidth(-999, container), bounds.min);
    assert.equal(h.clampPublicationDrawerWidth(9999, container), bounds.max);
  }
  state = h.render('different-workspace'); state = h.render('different-workspace'); assert.equal(state.width, 660);
  h.close();
});
