import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { inspectGitBundle, inspectGitTree, copyGitBundle, gitBinaryInfo, gitRuntimePrefix } from './bundled-git.mjs';

function executable(platform, architecture = 'amd64', managed = false) {
  const bytes = Buffer.alloc(512);
  if (platform === 'windows') {
    bytes.write('MZ'); bytes.writeUInt32LE(64, 0x3c); bytes.write('PE\0\0', 64);
    bytes.writeUInt16LE(({ arm64: 0xaa64, amd64: 0x8664, '386': 0x14c })[architecture], 68);
    if (managed) { bytes.writeUInt16LE(0x14c, 68); bytes.writeUInt16LE(0x10b, 88); bytes.writeUInt32LE(0x2000, 88 + 96 + 14 * 8); }
  } else {
    bytes.writeUInt32LE(0xfeedfacf, 0); bytes.writeUInt32LE(architecture === 'arm64' ? 0x0100000c : 0x01000007, 4);
  }
  return bytes;
}

function fixture(t, platform = process.platform === 'win32' ? 'macos' : 'windows', architecture = 'amd64') {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-git-bundle-'));
  t.after(() => fs.rmSync(root, { force: true, recursive: true }));
  const directory = path.join(root, 'upstream bundle');
  // A foreign fixture is deliberately only a header, and must never execute.
  const relative = platform === 'windows' ? 'cmd/git.exe' : 'bin/git';
  for (const name of [path.dirname(relative), 'licenses', 'sources', 'helpers']) fs.mkdirSync(path.join(directory, name), { recursive: true });
  fs.writeFileSync(path.join(directory, relative), executable(platform, architecture));
  fs.writeFileSync(path.join(directory, 'licenses', 'COPYING'), 'Original upstream notice fixture\n');
  fs.writeFileSync(path.join(directory, 'sources', 'git-source.tar.gz'), 'Corresponding source archive fixture\n');
  const options = { 'git-bundle-dir': directory, 'git-bundle-version': '2.53.0', 'git-bundle-sha256': 'f'.repeat(64), 'git-bundle-source': 'https://example.invalid/git-fixture.tar.gz' };
  const target = { platform, architecture };
  return { root, directory, relative, platform, options, target };
}

test('a complete foreign distribution preserves notices, sources and archive provenance without execution', t => {
  const f = fixture(t);
  const bundle = inspectGitBundle(f.options, f.target, {});
  assert.equal(bundle.native, false);
  assert.equal(bundle.metadata.archiveSha256, 'f'.repeat(64));
  assert.match(bundle.metadata.executableSha256, /^[a-f0-9]{64}$/);
  assert.equal(bundle.metadata.correspondingSources[0].path, 'sources/git-source.tar.gz');
  const destination = path.join(f.root, 'output', 'tools', 'git');
  copyGitBundle(bundle, destination);
  assert.deepEqual(fs.readFileSync(path.join(destination, f.relative)), fs.readFileSync(path.join(f.directory, f.relative)));
  assert.deepEqual(fs.readFileSync(path.join(destination, 'licenses', 'COPYING')), fs.readFileSync(path.join(f.directory, 'licenses', 'COPYING')));
  const marker = JSON.parse(fs.readFileSync(path.join(f.root, 'output', 'tools', 'runtime.json'), 'utf8'));
  assert.equal(marker.git.version, '2.53.0');
  fs.writeFileSync(path.join(destination, 'obsolete-helper'), 'stale');
  copyGitBundle(bundle, destination);
  assert.equal(fs.existsSync(path.join(destination, 'obsolete-helper')), false);
});

test('wrong helper architecture, non-native main and missing original license/source fail validation', t => {
  const f = fixture(t);
  const helper = path.join(f.directory, 'helpers', 'native-helper');
  fs.writeFileSync(helper, executable(f.platform, 'arm64'));
  assert.throws(() => inspectGitBundle(f.options, f.target, {}), /does not match/);
  fs.rmSync(helper);
  fs.writeFileSync(path.join(f.directory, f.relative), '#!/bin/sh\ngit "$@"\n');
  assert.throws(() => inspectGitBundle(f.options, f.target, {}), /native executable/);
  fs.writeFileSync(path.join(f.directory, f.relative), executable(f.platform));
  fs.rmSync(path.join(f.directory, 'licenses', 'COPYING'));
  assert.throws(() => inspectGitBundle(f.options, f.target, {}), /original notices/);
  fs.writeFileSync(path.join(f.directory, 'licenses', 'COPYING'), 'original');
  fs.rmSync(path.join(f.directory, 'sources', 'git-source.tar.gz'));
  fs.writeFileSync(path.join(f.directory, 'sources', 'README.txt'), 'A URL alone is not corresponding source');
  assert.throws(() => inspectGitBundle(f.options, f.target, {}), /corresponding source archives/);
});

test('managed AnyCPU assemblies do not masquerade as incompatible native libraries', t => {
  const f = fixture(t);
  const assembly = path.join(f.directory, 'helpers', 'credential-manager.dll');
  fs.writeFileSync(assembly, executable('windows', 'amd64', true));
  assert.deepEqual(gitBinaryInfo(assembly), { managed: true });
  assert.doesNotThrow(() => inspectGitBundle(f.options, f.target, {}));
});

test('the upstream 32-bit MinGit probe is allowed without accepting a 32-bit main executable', { skip: process.platform === 'win32' }, t => {
  const f = fixture(t);
  const probe = path.join(f.directory, 'usr', 'libexec', 'getprocaddr32.exe');
  fs.mkdirSync(path.dirname(probe), { recursive: true });
  fs.writeFileSync(probe, executable('windows', '386'));
  assert.doesNotThrow(() => inspectGitBundle(f.options, f.target, {}));
  fs.writeFileSync(path.join(f.directory, f.relative), executable('windows', '386'));
  assert.throws(() => inspectGitBundle(f.options, f.target, {}), /Git executable does not match/);
});

test('ARM64 permits only the pinned upstream MSYS compatibility paths and keeps the main Git native', {
  skip: process.platform === 'win32' && process.arch === 'arm64',
}, t => {
  const f = fixture(t, 'windows', 'arm64');
  const pinned = JSON.parse(fs.readFileSync(new URL('../build/runtime-lock.json', import.meta.url), 'utf8')).targets['windows/arm64'].git;
  Object.assign(f.options, { 'git-bundle-version': pinned.version, 'git-bundle-source': pinned.url, 'git-bundle-sha256': pinned.sha256 });
  for (const relative of ['usr/bin/sh.exe', 'usr/bin/msys-2.0.dll', 'usr/lib/ssh/ssh-keysign.exe', 'usr/libexec/getprocaddr64.exe', 'usr/libexec/getprocaddr32.exe']) {
    const file = path.join(f.directory, relative);
    fs.mkdirSync(path.dirname(file), { recursive: true });
    fs.writeFileSync(file, executable('windows', relative.endsWith('getprocaddr32.exe') ? '386' : 'amd64'));
  }
  assert.doesNotThrow(() => inspectGitBundle(f.options, f.target, {}));
  assert.throws(() => inspectGitBundle({ ...f.options, 'git-bundle-sha256': 'a'.repeat(64) }, f.target, {}), /does not match/);
  assert.throws(() => inspectGitBundle({ ...f.options, 'git-bundle-source': 'https://example.invalid/other.zip' }, f.target, {}), /does not match/);
  const incorrect = path.join(f.directory, 'clangarm64', 'bin', 'incorrect.dll');
  fs.mkdirSync(path.dirname(incorrect), { recursive: true });
  fs.writeFileSync(incorrect, executable('windows', 'amd64'));
  assert.throws(() => inspectGitBundle(f.options, f.target, {}), /does not match/);
  fs.rmSync(incorrect);
  fs.writeFileSync(path.join(f.directory, f.relative), executable('windows', 'amd64'));
  assert.throws(() => inspectGitBundle(f.options, f.target, {}), /Git executable does not match/);
});

test('Windows native Git smoke locates clangarm64 helpers and templates instead of a hardcoded mingw64 path', t => {
  const f = fixture(t);
  const prefix = path.join(f.directory, 'clangarm64');
  fs.mkdirSync(path.join(prefix, 'libexec', 'git-core'), { recursive: true });
  assert.throws(() => gitRuntimePrefix(f.directory, 'windows'), /directories are missing/);
  fs.mkdirSync(path.join(prefix, 'share', 'git-core', 'templates'), { recursive: true });
  assert.equal(gitRuntimePrefix(f.directory, 'windows'), prefix);
});

test('provenance cannot be omitted or claim an authenticated/non-HTTPS upstream URL', t => {
  const f = fixture(t);
  assert.throws(() => inspectGitBundle({}, f.target, {}), /Git bundle is required/);
  assert.throws(() => inspectGitBundle({ ...f.options, 'git-bundle-sha256': '' }, f.target, {}), /SHA256/);
  for (const url of ['http://example.invalid/git.zip', 'https://secret@example.invalid/git.zip']) {
    assert.throws(() => inspectGitBundle({ ...f.options, 'git-bundle-source': url }, f.target, {}), /HTTPS upstream/);
  }
});

test('relative internal symlinks are preserved while escaping, absolute and broken links are rejected', t => {
  const f = fixture(t);
  const link = path.join(f.directory, 'helpers', 'git-link');
  try { fs.symlinkSync(`../${f.relative}`, link); } catch (error) {
    if (process.platform === 'win32' && error.code === 'EPERM') { t.skip('Windows user has no symlink privilege'); return; }
    throw error;
  }
  const bundle = inspectGitBundle(f.options, f.target, {});
  const destination = path.join(f.root, 'copy');
  copyGitBundle(bundle, destination);
  // Windows readlink normalizes separators; compare the relative target while
  // still rejecting a copy that silently turns it into an absolute link.
  const copiedTarget = fs.readlinkSync(path.join(destination, 'helpers', 'git-link'));
  assert.equal(path.isAbsolute(copiedTarget), false);
  assert.equal(path.normalize(copiedTarget), path.normalize(`../${f.relative}`));
  for (const target of ['../../outside', path.join(f.directory, f.relative), '../missing']) {
    fs.unlinkSync(link); fs.symlinkSync(target, link);
    assert.throws(() => inspectGitTree(f.directory), /symlink/);
  }
});

test('copy rejects overlapping input/output before deleting original contents', t => {
  const f = fixture(t);
  const bundle = inspectGitBundle(f.options, f.target, {});
  assert.throws(() => copyGitBundle(bundle, path.join(f.directory, 'nested-output')), /must be separate/);
  assert.throws(() => copyGitBundle(bundle, f.root), /must be separate/);
  assert.ok(fs.existsSync(path.join(f.directory, f.relative)));
});
