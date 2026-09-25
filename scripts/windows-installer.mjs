#!/usr/bin/env node
// Build an offline-capable, per-user installer. This never runs the installer.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

export function nsisString(value) {
  if (/[\r\n\0]/.test(value)) throw new Error('Installer paths must not contain control characters.');
  if (value.includes('${')) throw new Error('Installer paths must not contain NSIS preprocessor expressions (${).');
  // Compiler-time File/include paths do not expand $INSTDIR-style runtime
  // variables. Doubling $ here changes the actual source filename.
  return `"${value.replaceAll('"', '$\\"')}"`;
}

function requireFile(file, label) {
  if (!fs.lstatSync(file, { throwIfNoEntry: false })?.isFile()) throw new Error(`${label} is missing or not a regular file: ${file}`);
}

export function payloadFiles(directory) {
  const files = [];
  const names = new Set();
  function visit(relative) {
    for (const item of fs.readdirSync(path.join(directory, relative), { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name, 'en'))) {
      if (/[<>:"\\|?*\x00-\x1f]/.test(item.name) || /[. ]$/.test(item.name) || /^(con|prn|aux|nul|com[1-9]|lpt[1-9])(?:\.|$)/i.test(item.name)) {
        throw new Error(`Payload path is not a valid Windows filename: ${item.name}`);
      }
      const name = relative ? `${relative}/${item.name}` : item.name;
      const folded = name.normalize('NFC').toLowerCase();
      if (names.has(folded)) throw new Error(`Case-insensitive payload path collision: ${name}`);
      names.add(folded);
      if (item.isDirectory()) visit(name);
      else if (item.isFile()) files.push(name);
      else throw new Error(`Installer payload cannot contain links or special files: ${name}`);
    }
  }
  visit('');
  for (const name of ['OneByOne.exe', 'onebyone-cli.exe', 'bin/rg.exe', 'THIRD_PARTY_LICENSES.txt']) {
    if (!files.includes(name)) throw new Error(`Required installer payload is missing: ${name}`);
  }
  if (!files.some(name => /^tools\/git\/(?:cmd|bin)\/git\.exe$/.test(name))) throw new Error('Bundled Git is missing from tools/git.');
  if (files.some(name => /^(?:uninstall\.exe|\.onebyone-installer.*)$/i.test(name))) throw new Error('Installer payload contains reserved installer files.');
  return files;
}

export function payloadInstructions(directory, files) {
  const install = [];
  let previous = null;
  let compression = 'auto';
  for (const relative of files) {
    const parent = path.posix.dirname(relative);
    if (parent !== previous) {
      install.push(`SetOutPath "$INSTDIR${parent === '.' ? '' : `\\${parent.replaceAll('/', '\\').replaceAll('$', () => '$$')}`}"`);
      previous = parent;
    }
    // Corresponding-source archives are already compressed. Keep their exact
    // bytes without spending minutes recompressing the source distribution.
    const nextCompression = /^tools\/git\/sources\/.*\.(?:tar\.(?:gz|xz|bz2|zst)|tgz|zip)$/i.test(relative) ? 'off' : 'auto';
    if (compression !== nextCompression) {
      install.push(`SetCompress ${nextCompression}`);
      compression = nextCompression;
    }
    install.push(`File ${nsisString(path.resolve(directory, relative))}`);
  }
  if (compression !== 'auto') install.push('SetCompress auto');
  const directories = new Set();
  const uninstall = files.flatMap(relative => {
    const target = `$INSTDIR\\${relative.replaceAll('/', '\\').replaceAll('$', () => '$$')}`;
    return [`\${If} \${FileExists} "${target}"`, `  ClearErrors`, `  Delete "${target}"`, `  \${If} \${Errors}`, `    MessageBox MB_ICONSTOP "A bundled application file could not be removed. Close OneByOne and retry: ${target}" /SD IDOK`, '    SetErrorLevel 1', '    Abort', '  \${EndIf}', '\${EndIf}'];
  });
  for (const relative of files) {
    let parent = path.posix.dirname(relative);
    while (parent !== '.') {
      directories.add(parent);
      parent = path.posix.dirname(parent);
    }
  }
  // Delete only files shipped by this installer. Never recursively delete the
  // application directory: a portable override may have put user data there.
  for (const directory of [...directories].sort((a, b) => b.split('/').length - a.split('/').length || b.localeCompare(a, 'en'))) {
    uninstall.push(`RMDir "$INSTDIR\\${directory.replaceAll('/', '\\').replaceAll('$', () => '$$')}"`);
  }
  const checks = ['!insertmacro OBO_REQUIRE_REAL_PATH "$INSTDIR"', ...[...directories].sort().map(relative => `!insertmacro OBO_REQUIRE_REAL_PATH "$INSTDIR\\${relative.replaceAll('/', '\\').replaceAll('$', () => '$$')}"`), ...files.map(relative => `!insertmacro OBO_REQUIRE_REAL_PATH "$INSTDIR\\${relative.replaceAll('/', '\\').replaceAll('$', () => '$$')}"`)];
  return { install: `${install.join('\n')}\n`, uninstall: `${uninstall.join('\n')}\n`, checks: `${checks.join('\n')}\n` };
}

export function validateWebView2Installer(file, expectedHash) {
  requireFile(file, 'WebView2 Evergreen Standalone Installer');
  if (!/^[0-9a-f]{64}$/i.test(expectedHash || '')) throw new Error('The verified WebView2 SHA256 is required (64 hexadecimal digits).');
  const data = fs.readFileSync(file);
  // The bootstrapper is only a few MB and cannot install on an offline PC.
  if (data.length < 10 * 1024 * 1024) throw new Error('Use the full WebView2 Evergreen Standalone Installer, not the small online bootstrapper.');
  const pe = data.length >= 64 ? data.readUInt32LE(0x3c) : 0;
  if (data.toString('ascii', 0, 2) !== 'MZ' || pe + 6 > data.length || data.toString('ascii', pe, pe + 4) !== 'PE\0\0' || ![0x14c, 0x8664, 0xaa64].includes(data.readUInt16LE(pe + 4))) {
    throw new Error('WebView2 installer is not a supported Windows PE executable.');
  }
  const hash = createHash('sha256').update(data).digest('hex');
  if (hash !== expectedHash.toLowerCase()) throw new Error('WebView2 installer SHA256 mismatch.');
  return hash;
}

function verifyMicrosoftSignature(file) {
  const script = `$ErrorActionPreference = 'Stop'; $s = Get-AuthenticodeSignature -LiteralPath $env:ONEBYONE_WEBVIEW2_VERIFY; if ($s.Status -ne 'Valid' -or $s.SignerCertificate.Subject -notmatch '(^|,\\s*)O=Microsoft Corporation(,|$)') { throw 'WebView2 installer must have a valid Microsoft Authenticode signature.' }`;
  execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-EncodedCommand', Buffer.from(script, 'utf16le').toString('base64')], {
    env: { ...process.env, ONEBYONE_WEBVIEW2_VERIFY: file }, encoding: 'utf8', windowsHide: true, timeout: 120000,
  });
}

export function buildWindowsInstaller({ directory, output, webview2Installer, webview2Sha256, arch = 'amd64', version = '0.1.0', makensis = process.env.MAKENSIS_BINARY || 'makensis' }) {
  if (!directory || !webview2Installer) throw new Error('directory and webview2Installer are required.');
  directory = path.resolve(directory);
  output = path.resolve(output || `${directory}-Setup.exe`);
  webview2Installer = path.resolve(webview2Installer);
  if (!['amd64', 'arm64'].includes(arch)) throw new Error('Installer arch must be amd64 or arm64.');
  if (!/^\d+\.\d+\.\d+(?:\.\d+)?$/.test(version) || version.split('.').some(part => Number(part) > 65535)) throw new Error('Installer version must have three or four numeric components (0–65535).');
  const relativeOutput = path.relative(directory, output);
  if (!relativeOutput.startsWith(`..${path.sep}`) && relativeOutput !== '..' && !path.isAbsolute(relativeOutput)) throw new Error('Installer output must be outside the package directory.');
  if (fs.existsSync(output)) throw new Error(`Installer output already exists: ${output}`);
  const files = payloadFiles(directory);
  const hash = validateWebView2Installer(webview2Installer, webview2Sha256);
  if (process.platform === 'win32') verifyMicrosoftSignature(webview2Installer);
  const generated = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-installer-'));
  const temporaryOutput = path.join(generated, 'OneByOne-Setup.exe');
  try {
    const instructions = payloadInstructions(directory, files);
    fs.writeFileSync(path.join(generated, 'payload-install.nsh'), instructions.install);
    fs.writeFileSync(path.join(generated, 'payload-uninstall.nsh'), instructions.uninstall);
    fs.writeFileSync(path.join(generated, 'payload-checks.nsh'), instructions.checks);
    const definitions = {
      OBO_OUTPUT: temporaryOutput, OBO_VERSION: version, OBO_FILE_VERSION: version.split('.').length === 3 ? `${version}.0` : version,
      OBO_ARCH: arch, OBO_WEBVIEW2_INSTALLER: webview2Installer,
      OBO_ICON: path.join(repository, 'build/windows/icon.ico'),
      OBO_INSTALL_FILES: path.join(generated, 'payload-install.nsh'), OBO_UNINSTALL_FILES: path.join(generated, 'payload-uninstall.nsh'),
      OBO_CHECK_FILES: path.join(generated, 'payload-checks.nsh'),
    };
    fs.writeFileSync(path.join(generated, 'installer.nsi'), Object.entries(definitions).map(([key, value]) => `!define ${key} ${nsisString(value)}`).join('\n') + `\n!include ${nsisString(path.join(repository, 'build/windows/installer/OneByOne.nsi'))}\n`);
    const flag = process.platform === 'win32' ? '/' : '-';
    // macOS makensis crashes while compiling Unicode installers under the C
    // locale (NSIS #1165). Keep a UTF-8 locale scoped to the compiler process.
    const env = process.platform === 'darwin' ? { ...process.env, LANG: 'en_US.UTF-8', LC_ALL: 'en_US.UTF-8' } : process.env;
    execFileSync(makensis, [`${flag}V3`, `${flag}INPUTCHARSET`, 'UTF8', path.join(generated, 'installer.nsi')], { stdio: 'inherit', windowsHide: true, env });
    requireFile(temporaryOutput, 'Compiled Windows installer');
    fs.mkdirSync(path.dirname(output), { recursive: true });
    fs.copyFileSync(temporaryOutput, output, fs.constants.COPYFILE_EXCL);
    return { output, arch, version, webview2Sha256: hash, webview2SignatureChecked: process.platform === 'win32' };
  } finally {
    fs.rmSync(generated, { recursive: true, force: true });
  }
}

function main(argv) {
  const names = { '--directory': 'directory', '--output': 'output', '--webview2-installer': 'webview2Installer', '--webview2-sha256': 'webview2Sha256', '--arch': 'arch', '--version': 'version', '--makensis': 'makensis' };
  const options = {};
  for (let index = 0; index < argv.length; index++) {
    const name = names[argv[index]];
    if (!name || !argv[index + 1] || argv[index + 1].startsWith('--')) throw new Error(`Unknown or incomplete installer option: ${argv[index]}`);
    options[name] = argv[++index];
  }
  const result = buildWindowsInstaller(options);
  process.stdout.write(`Windows Setup created: ${result.output}\n`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try { main(process.argv.slice(2)); } catch (error) { process.stderr.write(`${error.message}\n`); process.exitCode = 1; }
}
