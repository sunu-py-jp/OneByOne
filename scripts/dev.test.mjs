import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import childProcess from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
import { fileURLToPath } from 'node:url';
import { main } from './dev.mjs';

const repository = fileURLToPath(new URL('..', import.meta.url));
const supportedHost = { skip: !['darwin', 'win32'].includes(process.platform) };

function fixture(t, failWails = false) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-dev commands-'));
  const suffix = process.platform === 'win32' ? '.exe' : '';
  const go = path.join(directory, `go${suffix}`);
  const wails = path.join(directory, `wails${suffix}`);
  for (const executable of [go, wails]) fs.writeFileSync(executable, 'not a real compiler', { mode: 0o755 });
  const calls = [];
  const originalStat = fs.statSync;
  const originalMkdir = fs.mkdirSync;
  const nodeModules = path.join(repository, 'frontend', 'node_modules') + path.sep;
  t.mock.method(fs, 'statSync', (file, ...args) => {
    if (String(file).startsWith(nodeModules)) return { isFile: () => true };
    return originalStat(file, ...args);
  });
  t.mock.method(fs, 'mkdirSync', (file, ...args) => {
    if (file === path.join(repository, 'build', 'bin')) return;
    return originalMkdir(file, ...args);
  });
  t.mock.method(childProcess, 'spawnSync', (executable, args, options) => {
    calls.push({ executable, args, options });
    return { status: failWails && executable === wails ? 27 : 0 };
  });
  syncBuiltinESMExports();
  t.after(() => {
    t.mock.restoreAll();
    syncBuiltinESMExports();
    fs.rmSync(directory, { recursive: true, force: true });
  });
  return { directory, go, wails, calls, env: { ...process.env, RG_BINARY: path.join(directory, 'rg'), GIT_BUNDLE_DIR: path.join(directory, 'git'), WEBVIEW2_INSTALLER: path.join(directory, 'WebView2.exe'), WEBVIEW2_SHA256: 'b'.repeat(64), GO_BINARY: go, WAILS_BINARY: wails, ONEBYONE_BUILD_TEST: 'inherited value' } };
}

test('cross-build keeps the selected runtime and target through GUI, CLI, and package stages', supportedHost, async t => {
  const f = fixture(t);
  const rg = path.join('relative runtime', 'rg.exe');
  const licenses = path.join('relative runtime', 'original licenses');
  const output = path.join(f.directory, 'output with spaces');
  await main(['package', '--platform', 'windows/amd64', '--rg', rg, '--rg-license-dir', licenses,
    '--rg-version', '15.2.0', '--rg-sha256', 'a'.repeat(64), '--out', output], f.env);
  const gui = f.calls.findIndex(call => call.executable === f.wails);
  const cli = f.calls.findIndex(call => call.executable === f.go);
  const archive = f.calls.findIndex(call => call.args[0]?.endsWith(`${path.sep}package.mjs`));
  assert.ok(gui >= 0 && cli > gui && archive > cli, 'CLI must be rebuilt after Wails; only completed builds may be packaged');
  assert.ok(f.calls[gui].args.includes('-s'), 'Wails must reuse the already-built frontend');
  assert.equal(f.calls.filter(call => call.args[0]?.endsWith(`${path.sep}vite.js`)).length, 1);
  assert.ok(f.calls[gui].args.includes('windows/amd64'));
  assert.equal(f.calls[cli].options.env.GOOS, 'windows');
  assert.equal(f.calls[cli].options.env.GOARCH, 'amd64');
  assert.ok(f.calls[cli].args.some(arg => arg.endsWith(`${path.sep}onebyone-cli.exe`)));
  assert.ok(!f.calls[archive].args.includes('--adhoc-sign'), 'Windows packages must not inherit Mac signing');
  assert.equal(f.calls[archive].args.at(-1), output);
  const setup = f.calls.findIndex(call => call.args[0]?.endsWith(`${path.sep}windows-installer.mjs`));
  assert.ok(setup > archive, 'setup is created only after the distribution is complete');
  assert.ok(f.calls[setup].args.includes(f.env.WEBVIEW2_INSTALLER));
  for (const call of f.calls) {
    assert.equal(call.options.shell, false);
    assert.equal(call.options.env.RG_BINARY, path.resolve(rg));
    assert.equal(call.options.env.RG_LICENSE_DIR, path.resolve(licenses));
    assert.equal(call.options.env.RG_VERSION, '15.2.0');
    assert.equal(call.options.env.RG_SHA256, 'a'.repeat(64));
    assert.equal(call.options.env.ONEBYONE_BUILD_TEST, 'inherited value');
  }
  assert.equal(f.env.RG_BINARY, path.join(f.directory, 'rg'), 'normalizing runtime paths must not mutate the caller environment');
});

test('a failed Wails runtime hook stops before rebuilding CLI or assembling a package directory', supportedHost, async t => {
  const f = fixture(t, true);
  await assert.rejects(() => main(['package', '--platform', 'windows/amd64'], f.env), /Wails GUI and runtime failed \(exit 27\)/);
  assert.equal(f.calls.at(-1).executable, f.wails);
  assert.ok(!f.calls.some(call => call.executable === f.go));
  assert.ok(!f.calls.some(call => call.args[0]?.endsWith(`${path.sep}package.mjs`)));
});

test('Mac packages repair the signature by default and allow an explicit separate signing step', {
  skip: process.platform !== 'darwin',
}, async t => {
  const f = fixture(t);
  await main(['package', '--skip-build'], f.env);
  assert.equal(f.calls.length, 1, 'skip-build must not require a frontend or compiler run');
  assert.ok(f.calls[0].args.includes('--adhoc-sign'));
  f.calls.length = 0;
  await main(['package', '--skip-build', '--no-adhoc-sign', '--no-archive'], f.env);
  assert.equal(f.calls.length, 1);
  assert.ok(!f.calls[0].args.includes('--adhoc-sign'));
  assert.ok(f.calls[0].args.includes('--no-archive'));
});

test('conflicting target or signing options are rejected before a build can overwrite outputs', supportedHost, async t => {
  const f = fixture(t);
  await assert.rejects(() => main(['package', '--platform', 'windows/amd64', '--arch', 'arm64'], f.env), /different architectures/);
  await assert.rejects(() => main(['package', '--adhoc-sign', '--no-adhoc-sign'], f.env), /cannot be combined/);
  assert.deepEqual(f.calls, []);
});

test('test timeout is bounded by default and can be extended without dropping race checks', supportedHost, async t => {
  const f = fixture(t);
  const suffix = process.platform === 'win32' ? '.exe' : '';
  f.env.RG_BINARY = path.join(f.directory, `rg${suffix}`);
  const gitDirectory = path.join(f.env.GIT_BUNDLE_DIR, process.platform === 'win32' ? 'cmd' : 'bin');
  fs.mkdirSync(gitDirectory, { recursive: true });
  for (const file of [f.env.RG_BINARY, path.join(gitDirectory, `git${suffix}`)]) {
    fs.writeFileSync(file, 'test fixture', { mode: 0o755 });
  }
  await main(['test'], f.env);
  let goTests = f.calls.find(call => call.executable === f.go && call.args[0] === 'test');
  assert.ok(goTests.args.includes(process.platform === 'win32' ? '-timeout=30m' : '-timeout=10m'));
  f.calls.length = 0;
  await main(['test', '--timeout', '45m', '--race'], f.env);
  goTests = f.calls.find(call => call.executable === f.go && call.args[0] === 'test');
  assert.ok(goTests.args.includes('-timeout=45m'));
  assert.ok(goTests.args.includes('-race'));
  assert.ok(f.calls.some(call => call.executable === f.go && call.args[0] === 'vet'));
});

test('invalid or unlimited test timeouts are rejected before running tools', async () => {
  for (const timeout of ['0', '0s', '-1m', 'forever', '10']) {
    await assert.rejects(() => main(['test', '--timeout', timeout], {}), /--timeout/);
  }
});
