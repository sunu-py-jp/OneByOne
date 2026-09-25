import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { downloadPinned, validateArchivePaths, treeDigest, runtimeLock, installRuntimeAsset } from './prepare-runtime.mjs';

function temporary(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-runtime-input-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  return root;
}

test('runtime downloads verify full streamed bytes and reuse only a matching cache', async t => {
  const root = temporary(t), bytes = Buffer.from('runtime\n'.repeat(8192));
  const asset = { url: 'https://example.test/runtime', sha256: createHash('sha256').update(bytes).digest('hex') };
  let calls = 0;
  const fetcher = async () => { calls++; return new Response(bytes); };
  const file = await downloadPinned(asset, root, fetcher);
  assert.deepEqual(fs.readFileSync(file), bytes);
  assert.equal(await downloadPinned(asset, root, fetcher), file);
  assert.equal(calls, 1);
  fs.writeFileSync(file, 'corrupted');
  await assert.rejects(downloadPinned(asset, root, fetcher), /Cached runtime SHA256 mismatch/);
});

test('mismatching or incomplete downloads never become prepared inputs', async t => {
  const root = temporary(t);
  await assert.rejects(downloadPinned({ url: 'https://example.test/runtime', sha256: 'a'.repeat(64) }, root, async () => new Response('wrong')), /SHA256 mismatch/);
  assert.deepEqual(fs.readdirSync(root), []);
  await assert.rejects(downloadPinned({ url: 'http://example.test/runtime', sha256: 'a'.repeat(64) }, root), /HTTPS/);
});

test('runtime archive entries reject absolute paths and parent traversal', () => {
  validateArchivePaths('./\n./bin/git\nshare/git-core/templates/info/exclude\n');
  for (const entry of ['/etc/config', '../outside', 'bin/../../outside', 'C:\\outside', '..\\outside']) assert.throws(() => validateArchivePaths(entry), /Unsafe/);
});

test('a notice inside a verified upstream archive preserves its exact bytes without extracting other files', t => {
  const root = temporary(t), output = path.join(root, 'licenses');
  fs.mkdirSync(output);
  const notice = Buffer.from('Original notice\r\nCopyright example\r\n');
  fs.writeFileSync(path.join(root, 'NOTICE.txt'), notice);
  fs.writeFileSync(path.join(root, 'unused.txt'), 'not a license');
  const archive = path.join(root, 'upstream.tar');
  execFileSync('tar', ['-cf', archive, '-C', root, 'NOTICE.txt', 'unused.txt']);
  installRuntimeAsset({ name: 'upstream-NOTICE.txt', archiveMember: 'NOTICE.txt' }, archive, output);
  assert.deepEqual(fs.readdirSync(output), ['upstream-NOTICE.txt']);
  assert.deepEqual(fs.readFileSync(path.join(output, 'upstream-NOTICE.txt')), notice);
  assert.throws(() => installRuntimeAsset({ name: 'bad.txt', archiveMember: '../outside' }, archive, output), /Unsafe/);
  assert.throws(() => installRuntimeAsset({ name: '../outside' }, archive, output), /Invalid/);
});

test('prepared runtime integrity changes when a helper changes', t => {
  const root = temporary(t);
  fs.mkdirSync(path.join(root, 'bin'));
  fs.writeFileSync(path.join(root, 'bin', 'git'), 'original');
  const before = treeDigest(root);
  fs.writeFileSync(path.join(root, '.prepared.json'), '{}');
  assert.equal(treeDigest(root), before);
  fs.writeFileSync(path.join(root, 'bin', 'git'), 'modified');
  assert.notEqual(treeDigest(root), before);
});

test('runtime links stay valid under an aliased cache path and cannot escape it', { skip: process.platform === 'win32' }, t => {
  const parent = temporary(t), root = path.join(parent, 'bundle'), alias = path.join(parent, 'alias');
  fs.mkdirSync(path.join(root, 'bin'), { recursive: true });
  fs.writeFileSync(path.join(root, 'bin', 'git'), 'binary');
  fs.symlinkSync('git', path.join(root, 'bin', 'git-helper'));
  fs.symlinkSync('bundle', alias);
  assert.equal(treeDigest(alias), treeDigest(root));
  fs.writeFileSync(path.join(parent, 'outside'), 'outside');
  fs.symlinkSync('../../outside', path.join(root, 'bin', 'escape'));
  assert.throws(() => treeDigest(alias), /escapes its bundle/);
});

test('every supported native target pins Git, rg, and offline Windows runtime inputs', () => {
  assert.deepEqual(Object.keys(runtimeLock.targets).sort(), ['macos/amd64', 'macos/arm64', 'windows/amd64', 'windows/arm64']);
  for (const [target, spec] of Object.entries(runtimeLock.targets)) {
    for (const asset of [spec.rg, spec.git, ...spec.git.sources, ...spec.git.licenses, ...(target.startsWith('windows') ? [spec.webview2] : [])]) {
      assert.match(asset.url, /^https:\/\//);
      assert.match(asset.sha256, /^[a-f0-9]{64}$/);
    }
    assert.ok(spec.git.sources.length > 0);
    if (target.startsWith('windows')) assert.match(spec.webview2.url, /MicrosoftEdgeWebView2RuntimeInstaller(?:X64|ARM64)\.exe$/);
  }
});
