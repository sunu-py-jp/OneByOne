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
  absWorkingDir: frontend, stdin: { contents: 'export { RuleMarkdown } from "./src/RuleMarkdown"; export { previewResultPublication } from "./src/preview";', resolveDir: frontend }, bundle: true, write: false,
  platform: 'node', format: 'cjs', jsx: 'automatic',
  external: ['react', 'react/*', 'react-dom', 'react-dom/*'], loader: { '.css': 'empty' },
});
const module = { exports: {} };
new Function('require', 'module', 'exports', bundle.outputFiles[0].text)(requireFrontend, module, module.exports);
const render = (body, reportCells = true) => renderToStaticMarkup(createElement(module.exports.RuleMarkdown, { body, reportCells }));
const table = value => `| ID | 内容 |\n| --- | --- |\n| 7 | ${value} |`;

test('report table cells render only plain br tags as line breaks', () => {
  const html = render(table('最初<br>次<br/>最後<BR />完了'));
  assert.match(html, /最初<br\/>\n次<br\/>\n最後<br\/>\n完了/);
  assert.equal((html.match(/<td>/g) || []).length, 2);
  assert.ok(!html.includes('&lt;br'));
});

test('report escapes display literal text and user-authored inline code retains Markdown semantics', () => {
  const html = render(table('A&#124;B<br>&lt;script&gt;safe&lt;/script&gt;<br>&#96;writer.write(a &#124; b)&#96;<br>&#96;a&lt;b &amp;&amp; path\\\\name&#96;<br>`&amp;lt;`'));
  assert.equal((html.match(/<td>/g) || []).length, 2, 'pipes must not create extra cells');
  assert.match(html, /A\|B/);
  assert.match(html, /&lt;script&gt;safe&lt;\/script&gt;/);
  assert.match(html, /`writer\.write\(a \| b\)`/);
  assert.match(html, /`a&lt;b &amp;&amp; path\\name`/);
  assert.match(html, /<code>&amp;amp;lt;<\/code>/, 'user-authored code must not be rewritten');
  assert.ok(!/<script\b/.test(html));
});

test('attributes, other HTML, code literals and non-report Markdown stay non-executable', () => {
  const html = render(table('<br onclick="evil()"><img src="https://example.com/track"><script>evil()</script><br>safe'));
  assert.ok(!/<(?:script|img)\b/.test(html));
  assert.ok(!/<br\b[^>]*onclick/.test(html));
  assert.match(html, /&lt;br onclick=/);
  assert.match(html, /<br\/>\nsafe/);
  const code = render(table('`<br>`'));
  assert.match(code, /<code>&lt;br&gt;<\/code>/);
  assert.equal((code.match(/<br\/>/g) || []).length, 0);
  assert.match(render(table('one<br>two'), false), /one&lt;br&gt;two/);
  assert.match(render('Outside<br>table'), /Outside&lt;br&gt;table/);
});

test('publication counts render on separate lines and the two-column file summary keeps working links', () => {
  const result = module.exports.previewResultPublication();
  const html = render(result.message);
  const counts = html.split('<h1>全体サマリー</h1>')[1].split('<h1>修正サマリー</h1>')[0];
  assert.match(counts, /📄 対象ファイル数：\d+<br\/>\n✅ 修正完了：\d+<br\/>\n⚠️ 要確認：\d+<br\/>\n☑️ 修正不要：\d+/);
  const summary = html.split('<h1>修正サマリー</h1>')[1].split('<h1>修正一覧</h1>')[0];
  assert.equal((summary.match(/<th>/g) || []).length, 2);
  assert.match(summary, /<th>ファイル名<\/th>/);
  assert.match(summary, /<th>ステータス<\/th>/);
  assert.equal((summary.match(/<td>/g) || []).length, result.reportFiles.length * 2);
  for (const file of result.reportFiles) assert.ok(summary.includes(`href="${file.linkPath}"`));
  const names = [...summary.matchAll(/<a [^>]*>([^<]+)<\/a>/g)].map(match => match[1]);
  const detailNames = [...html.split('<h1>修正一覧</h1>')[1].matchAll(/<a [^>]*>([^<]+)<\/a>/g)].map(match => match[1]);
  assert.deepEqual(names, detailNames);
});
