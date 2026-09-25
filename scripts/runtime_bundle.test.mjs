import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { fileURLToPath } from 'node:url';

const script = fileURLToPath(new URL('./package.mjs', import.meta.url));
const builtApp = fileURLToPath(new URL('../build/bin/OneByOne.app', import.meta.url));

function fixture(t) {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-runtime-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const input = path.join(root, 'official rg');
  const output = path.join(root, 'built app');
  const git = path.join(root, 'official git');
  fs.mkdirSync(input);
  fs.mkdirSync(output);
  const pe = Buffer.alloc(128);
  pe.write('MZ');
  pe.writeUInt32LE(64, 0x3c);
  pe.write('PE\0\0', 64);
  pe.writeUInt16LE(0x8664, 68);
  const rg = path.join(input, 'rg.exe');
  const app = path.join(output, 'OneByOne.exe');
  fs.writeFileSync(rg, pe);
  fs.writeFileSync(app, pe);
  for (const directory of ['cmd', 'licenses', 'sources']) fs.mkdirSync(path.join(git, directory), { recursive: true });
  fs.writeFileSync(path.join(git, 'cmd', 'git.exe'), pe);
  fs.writeFileSync(path.join(git, 'licenses', 'COPYING'), 'Git license fixture\n');
  fs.writeFileSync(path.join(git, 'sources', 'git-source.tar.gz'), 'Corresponding source archive fixture\n');
  for (const name of ['COPYING', 'LICENSE-MIT', 'UNLICENSE']) fs.writeFileSync(path.join(input, name), `Original ${name}\n`);
  return { input, output, rg, app, git, hash: createHash('sha256').update(pe).digest('hex') };
}

// Header fixtures intentionally cannot execute: foreign builds must only
// inspect their architecture and verify the supplied SHA256.
const crossOnly = { skip: process.platform !== 'darwin' };
function run(f, extra = [], rg = f.rg) {
  return spawnSync(process.execPath, [script, '--runtime-only', '--wails-target', 'windows/amd64', '--app', f.app,
    '--rg', rg, '--rg-license-dir', f.input, '--rg-version', '15.2.0', '--rg-sha256', f.hash,
    '--git-bundle-dir', f.git, '--git-bundle-version', '2.53.0.windows.1', '--git-bundle-sha256', 'a'.repeat(64),
    '--git-bundle-source', 'https://example.invalid/MinGit-fixture.zip', ...extra],
  { encoding: 'utf8', env: { ...process.env, PATH: '', RG_BINARY: '', RG_LICENSE_DIR: '' } });
}

test('build hook includes Git, rg and original licenses beside a Windows app without a CLI or PATH', crossOnly, t => {
  const f = fixture(t);
  const result = run(f);
  assert.equal(result.status, 0, result.stderr);
  assert.deepEqual(fs.readFileSync(path.join(f.output, 'bin', 'rg.exe')), fs.readFileSync(f.rg));
  assert.deepEqual(fs.readFileSync(path.join(f.output, 'tools', 'git', 'cmd', 'git.exe')), fs.readFileSync(path.join(f.git, 'cmd', 'git.exe')));
  assert.equal(JSON.parse(fs.readFileSync(path.join(f.output, 'tools', 'runtime.json'), 'utf8')).git.version, '2.53.0.windows.1');
  assert.match(fs.readFileSync(path.join(f.output, 'tools', 'git', 'ONEBYONE-NOTICE.txt'), 'utf8'), /Corresponding source/);
  for (const name of ['COPYING', 'LICENSE-MIT', 'UNLICENSE']) {
    assert.deepEqual(fs.readFileSync(path.join(f.output, 'licenses', 'ripgrep', name)), fs.readFileSync(path.join(f.input, name)));
  }
});

test('missing Git source assets fail before creating a runnable-looking build output', crossOnly, t => {
  const f = fixture(t);
  fs.rmSync(path.join(f.git, 'sources'), { recursive: true });
  const result = run(f);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /corresponding source archives/);
  assert.equal(fs.existsSync(path.join(f.output, 'bin')), false);
});

test('distribution packages preserve the complete Git bundle and provenance with the CLI', crossOnly, t => {
  const f = fixture(t);
  const cli = path.join(f.output, 'onebyone-cli.exe');
  fs.copyFileSync(f.app, cli);
  const out = path.join(f.output, 'distribution with spaces');
  const result = spawnSync(process.execPath, [script, '--platform', 'windows', '--arch', 'amd64', '--app', f.app,
    '--cli', cli, '--out', out, '--no-archive', '--rg', f.rg, '--rg-license-dir', f.input,
    '--rg-version', '15.2.0', '--rg-sha256', f.hash,
    '--git-bundle-dir', f.git, '--git-bundle-version', '2.53.0.windows.1', '--git-bundle-sha256', 'a'.repeat(64),
    '--git-bundle-source', 'https://example.invalid/MinGit-fixture.zip'], { encoding: 'utf8', env: { ...process.env, PATH: '' } });
  assert.equal(result.status, 0, result.stderr);
  const info = JSON.parse(fs.readFileSync(path.join(out, 'build-info.json'), 'utf8'));
  assert.equal(info.git.version, '2.53.0.windows.1');
  assert.equal(info.git.archiveSha256, 'a'.repeat(64));
  assert.equal(info.crossPackaged, true);
  assert.equal(info.guiRuntimeVerifiedByPackager, false);
  assert.ok(fs.existsSync(path.join(out, 'tools', 'git', 'sources', 'git-source.tar.gz')));
  assert.ok(fs.existsSync(path.join(out, 'tools', 'runtime.json')));
  assert.ok(fs.existsSync(path.join(out, 'onebyone-cli.exe')));
});

test('missing rg fails the build instead of producing an incomplete runtime', crossOnly, t => {
  const f = fixture(t);
  const result = run(f, [], path.join(f.input, 'missing.exe'));
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /rg is missing/);
  assert.equal(fs.existsSync(path.join(f.output, 'bin')), false);
});

test('wrong architecture and hash are rejected before copying runtime files', crossOnly, t => {
  const f = fixture(t);
  let result = run(f, ['--rg-sha256', '0'.repeat(64)]);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /SHA256 mismatch/);
  const arm = fs.readFileSync(f.rg);
  arm.writeUInt16LE(0xaa64, 68);
  fs.writeFileSync(f.rg, arm);
  result = run(f);
  assert.notEqual(result.status, 0);
  assert.match(result.stderr, /does not match windows\/amd64/);
  assert.equal(fs.existsSync(path.join(f.output, 'bin')), false);
});

test('Wails macOS executable path produces a signed bundle and adjacent CLI runtime without PATH', {
  skip: process.platform !== 'darwin' || !process.env.RG_BINARY || !process.env.GIT_BUNDLE_DIR || !fs.existsSync(builtApp),
}, t => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-native-runtime-'));
  t.after(() => fs.rmSync(root, { recursive: true, force: true }));
  const app = path.join(root, 'OneByOne.app');
  fs.cpSync(builtApp, app, { recursive: true });
  fs.rmSync(path.join(app, 'Contents', 'MacOS', 'bin'), { recursive: true, force: true });
  const arch = process.arch === 'x64' ? 'amd64' : process.arch;
  const result = spawnSync(process.execPath, [script, '--runtime-only', '--wails-target', `darwin/${arch}`,
    '--app', path.join(app, 'Contents', 'MacOS', 'OneByOne')],
  { encoding: 'utf8', env: { ...process.env, PATH: '' } });
  assert.equal(result.status, 0, result.stderr);
  for (const binary of [path.join(root, 'bin', 'rg'), path.join(app, 'Contents', 'MacOS', 'bin', 'rg')]) {
    const version = spawnSync(binary, ['--no-config', '--version'], { encoding: 'utf8', env: { PATH: '' } });
    assert.equal(version.status, 0, version.stderr);
    assert.match(version.stdout, /^ripgrep /);
  }
  assert.equal(fs.existsSync(path.join(app, 'Contents', 'Resources', 'licenses', 'ripgrep', 'COPYING')), true);
  for (const gitRoot of [path.join(root, 'tools', 'git'), path.join(app, 'Contents', 'MacOS', 'tools', 'git')]) {
    const version = spawnSync(path.join(gitRoot, 'bin', 'git'), ['--version'], { encoding: 'utf8', env: { PATH: '' } });
    assert.equal(version.status, 0, version.stderr);
    assert.match(version.stdout, /^git version /);
    assert.ok(fs.existsSync(path.join(path.dirname(gitRoot), 'runtime.json')));
  }
  const signature = spawnSync('/usr/bin/codesign', ['--verify', '--deep', '--strict', app], { encoding: 'utf8' });
  assert.equal(signature.status, 0, signature.stderr);
});
