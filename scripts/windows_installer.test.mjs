import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { buildWindowsInstaller, nsisString, payloadFiles, payloadInstructions, validateWebView2Installer } from './windows-installer.mjs';

function fixture(t) {
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-installer-test-'));
  t.after(() => fs.rmSync(directory, { recursive: true, force: true }));
  for (const name of ['OneByOne.exe', 'onebyone-cli.exe', 'bin/rg.exe', 'tools/git/cmd/git.exe', 'THIRD_PARTY_LICENSES.txt']) {
    fs.mkdirSync(path.dirname(path.join(directory, name)), { recursive: true });
    fs.writeFileSync(path.join(directory, name), 'test');
  }
  return directory;
}

function peFixture(file, size = 10 * 1024 * 1024) {
  const bytes = Buffer.alloc(size);
  bytes.write('MZ');
  bytes.writeUInt32LE(0x80, 0x3c);
  bytes.write('PE\0\0', 0x80);
  bytes.writeUInt16LE(0x14c, 0x84); // Standalone installer wrapper may be x86.
  fs.writeFileSync(file, bytes);
  return createHash('sha256').update(bytes).digest('hex');
}

test('NSIS literals preserve spaces, Unicode, dollar signs and quotes without evaluating them', () => {
  assert.equal(nsisString('C:\\folder name\\日本語\\$INSTDIR\\"file"'), '"C:\\folder name\\日本語\\$INSTDIR\\$\\"file$\\""');
  assert.throws(() => nsisString('one\n!system command'), /control characters/);
  assert.throws(() => nsisString('${untrusted}'), /preprocessor/);
});

test('payload requires bundled tools and rejects Windows path collisions and links', t => {
  const directory = fixture(t);
  assert.equal(payloadFiles(directory).length, 5);
  fs.rmSync(path.join(directory, 'tools/git/cmd/git.exe'));
  assert.throws(() => payloadFiles(directory), /Bundled Git/);
  fs.writeFileSync(path.join(directory, 'tools/git/cmd/git.exe'), 'test');
  // Windows cannot create a regular file called NUL.txt. Inject its directory
  // entry so the same rejection is exercised on Windows and macOS.
  const readdir = fs.readdirSync.bind(fs);
  const mockedRead = t.mock.method(fs, 'readdirSync', (file, options) => {
    const entries = readdir(file, options);
    return file === directory ? [...entries, { name: 'NUL.txt' }] : entries;
  });
  assert.throws(() => payloadFiles(directory), /Windows filename/);
  mockedRead.mock.restore();
  // Symbolic-link creation requires a privilege on Windows; the source and
  // path validation itself remains covered there without requiring elevation.
  if (process.platform !== 'win32') {
    fs.symlinkSync('OneByOne.exe', path.join(directory, 'link.exe'));
    assert.throws(() => payloadFiles(directory), /links or special/);
  }
});

test('uninstall removes only shipped files and empty directories, preserving user-created data', t => {
  const directory = fixture(t);
  const instructions = payloadInstructions(directory, payloadFiles(directory));
  assert.match(instructions.install, /SetOutPath "\$INSTDIR\\tools\\git\\cmd"/);
  assert.match(instructions.uninstall, /Delete "\$INSTDIR\\tools\\git\\cmd\\git.exe"/);
  assert.ok(instructions.uninstall.indexOf('RMDir "$INSTDIR\\tools\\git\\cmd"') < instructions.uninstall.indexOf('RMDir "$INSTDIR\\tools"'));
  assert.doesNotMatch(instructions.uninstall, /RMDir \/r|\*|LOCALAPPDATA|workspaces|private/);
  assert.match(instructions.uninstall, /\$\{Errors\}[\s\S]*SetErrorLevel 1[\s\S]*Abort/);
  assert.match(instructions.checks, /OBO_REQUIRE_REAL_PATH "\$INSTDIR\\tools\\git"/);
  assert.match(instructions.checks, /OBO_REQUIRE_REAL_PATH "\$INSTDIR\\tools\\git\\cmd\\git.exe"/);
});

test('WebView2 requires a pinned full offline PE payload, with matching SHA256', t => {
  const directory = fixture(t);
  const file = path.join(directory, 'standalone.exe');
  const hash = peFixture(file);
  assert.equal(validateWebView2Installer(file, hash.toUpperCase()), hash);
  assert.throws(() => validateWebView2Installer(file, '0'.repeat(64)), /mismatch/);
  assert.throws(() => validateWebView2Installer(file, ''), /verified WebView2 SHA256/);
  peFixture(file, 256);
  assert.throws(() => validateWebView2Installer(file, hash), /online bootstrapper/);
  fs.writeFileSync(file, Buffer.alloc(10 * 1024 * 1024));
  assert.throws(() => validateWebView2Installer(file, hash), /Windows PE/);
});

test('source archives avoid recompression while normal application files stay compressed', () => {
  const instructions = payloadInstructions('/payload', ['OneByOne.exe', 'tools/git/sources/bash.src.tar.zst', 'tools/git/sources/manifest.json']);
  assert.match(instructions.install, /SetCompress off\nFile [^\n]*bash\.src\.tar\.zst/);
  assert.match(instructions.install, /SetCompress auto\nFile [^\n]*manifest\.json/);
});

test('installer refuses unsafe output placement and invalid target metadata before compiling', t => {
  const directory = fixture(t);
  const options = { directory, webview2Installer: 'unused.exe' };
  assert.throws(() => buildWindowsInstaller({ ...options, output: path.join(directory, 'setup.exe') }), /outside the package/);
  assert.throws(() => buildWindowsInstaller({ ...options, arch: '386' }), /arch/);
  assert.throws(() => buildWindowsInstaller({ ...options, version: '1.2.3"' }), /version/);
  assert.throws(() => buildWindowsInstaller({ ...options, version: '1.2.65536' }), /version/);
});

test('installer source checks installed WebView2, installs offline only if needed, and retains shared data', () => {
  const source = fs.readFileSync(new URL('../build/windows/installer/OneByOne.nsi', import.meta.url), 'utf8');
  assert.match(source, /RequestExecutionLevel user/);
  assert.match(source, /SetRegView 32/);
  assert.match(source, /SetRegView 64/);
  assert.match(source, /OBO_READ_WEBVIEW2 HKCU/);
  assert.match(source, /OBO_READ_WEBVIEW2 HKLM/);
  assert.match(source, /F3017226-FE2A-4295-8BDF-00C3A9A7E4C5/);
  assert.ok(source.indexOf('Using installed WebView2 Runtime') < source.indexOf('ExecWait \'"$PLUGINSDIR\\WebView2Standalone.exe'));
  assert.match(source, /ExecWait.*WebView2Standalone\.exe.*\/silent \/install/);
  assert.match(source, /WebView2ExitCode == 3010/);
  assert.match(source, /Call DetectWebView2[\s\S]*Call DetectWebView2/);
  assert.doesNotMatch(source, /inetc::|NSISdl::|RMDir \/r|DeleteRegKey HKLM/);
  assert.match(source, /Keep shared WebView2/);
  assert.match(source, /OBO_MIN_WEBVIEW2 "94\.0\.992\.31"/);
  assert.match(source, /VersionCompare.*OBO_MIN_WEBVIEW2/);
});

test('updates use the registered prior uninstaller to remove stale owned helpers and reject linked paths', () => {
  const source = fs.readFileSync(new URL('../build/windows/installer/OneByOne.nsi', import.meta.url), 'utf8');
  assert.match(source, /ReadRegStr \$0 HKCU "Software\\OneByOne\\Installer" "InstallDir"[\s\S]*\$0 != \$INSTDIR/);
  assert.match(source, /ExecWait '"\$INSTDIR\\Uninstall.exe" \/S _\?=\$INSTDIR'/);
  assert.ok(source.indexOf('Call RemovePreviousVersion') < source.indexOf('!include "${OBO_INSTALL_FILES}"'));
  assert.match(source, /unmanaged Git files/);
  assert.match(source, /FILE_ATTRIBUTE_REPARSE_POINT/);
  assert.match(source, /Call un.CheckPayloadPaths/);
});
