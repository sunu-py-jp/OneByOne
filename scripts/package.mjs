#!/usr/bin/env node
// Assemble native build outputs. No network access, npm dependencies,
// installation, notarization, or publishing. Optional macOS ad hoc signing only.
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { inspectGitBundle, copyGitBundle, signMacGitBundle, verifyNativeGit, canonicalBuildPath } from './bundled-git.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');

function usage() {
  process.stdout.write(`Usage: node scripts/package.mjs [options]

  --rg PATH              Native rg binary (or RG_BINARY, then PATH)
  --rg-license-dir PATH  Directory containing original COPYING/LICENSE-MIT/UNLICENSE
                         (or RG_LICENSE_DIR, then rg's directory)
  --platform NAME        macos or windows (defaults to the host platform)
  --arch NAME            arm64 or amd64 (defaults to the host architecture)
  --rg-version VERSION   Foreign rg version (or RG_VERSION); required when cross-building
  --rg-sha256 HEX        Verified binary hash (or RG_SHA256); required when cross-building
  --git-bundle-dir PATH  Prepared Git distribution, including licenses/ and sources/
  --git-bundle-version V Git version (or GIT_BUNDLE_VERSION)
  --git-bundle-sha256 H  Upstream archive SHA256 (or GIT_BUNDLE_SHA256)
  --git-bundle-source U  Upstream archive URL (or GIT_BUNDLE_SOURCE)
  --app PATH             Built OneByOne.app or OneByOne.exe
  --cli PATH             Built onebyone or onebyone-cli.exe
  --out PATH             New package directory (must not already exist)
  --no-archive           Prepare the directory without creating a ZIP
  --adhoc-sign           macOS only: rebuild the local ad hoc bundle signature
  --runtime-only         Add Git, rg and licenses to the build output, without a ZIP
                         or CLI input. macOS bundles are ad hoc signed afterward.
  --wails-target TARGET  Build-hook target (darwin/arm64, windows/amd64, etc.)

Defaults use build/bin and build/package/OneByOne-<platform>-<architecture>.
Native packaging is preferred. Cross packages explicitly record unverified runtime.
No distribution signing or publishing.
`);
}

function parseArguments() {
  const result = {};
  for (let i = 2; i < process.argv.length; i++) {
    const key = process.argv[i];
    if (key === '--help' || key === '-h') {
      usage();
      process.exit(0);
    }
    if (key === '--no-archive') {
      result.noArchive = true;
      continue;
    }
    if (key === '--adhoc-sign') {
      result.adhocSign = true;
      continue;
    }
    if (key === '--runtime-only') {
      result.runtimeOnly = true;
      continue;
    }
    const pathOptions = ['--rg', '--rg-license-dir', '--git-bundle-dir', '--app', '--cli', '--out'];
    if (![...pathOptions, '--platform', '--arch', '--rg-version', '--rg-sha256', '--git-bundle-version', '--git-bundle-sha256', '--git-bundle-source', '--wails-target'].includes(key)) {
      throw new Error(`Unknown option: ${key}`);
    }
    const value = process.argv[++i];
    if (!value || value.startsWith('--')) throw new Error(`${key} requires a value`);
    result[key.slice(2)] = pathOptions.includes(key) ? path.resolve(value) : value;
  }
  return result;
}

function requireFile(file, label) {
  if (!fs.statSync(file, { throwIfNoEntry: false })?.isFile()) {
    throw new Error(`${label} is missing: ${file}`);
  }
}

function findRG(configured, targetWindows) {
  if (configured) {
    requireFile(configured, 'rg');
    return fs.realpathSync(configured);
  }
  const name = targetWindows ? 'rg.exe' : 'rg';
  for (const directory of (process.env.PATH || '').split(path.delimiter)) {
    if (!directory) continue;
    const candidate = path.join(directory.replace(/^"|"$/g, ''), name);
    if (fs.statSync(candidate, { throwIfNoEntry: false })?.isFile()) return fs.realpathSync(candidate);
  }
  throw new Error('rg was not found. Pass --rg with the native binary from the official release archive.');
}

function copyFile(source, destination, executable = false) {
  fs.mkdirSync(path.dirname(destination), { recursive: true });
  fs.copyFileSync(source, destination);
  if (executable && process.platform !== 'win32') fs.chmodSync(destination, 0o755);
}

function inside(parent, child) {
  const relative = path.relative(parent, child);
  return relative === '' || (!relative.startsWith(`..${path.sep}`) && relative !== '..' && !path.isAbsolute(relative));
}

function assertUniqueNames(names) {
  const seen = new Set();
  for (const name of names) {
    const folded = name.replaceAll('\\', '/').normalize('NFC').toLowerCase();
    if (seen.has(folded)) throw new Error(`Case-insensitive package path collision: ${name}`);
    seen.add(folded);
  }
}

function packagePaths(root, prefix = '') {
  const result = [];
  for (const item of fs.readdirSync(root, { withFileTypes: true })) {
    const relative = prefix ? `${prefix}/${item.name}` : item.name;
    result.push(relative);
    if (item.isDirectory()) result.push(...packagePaths(path.join(root, item.name), relative));
  }
  return result;
}

function binaryArchitectures(file, targetPlatform) {
  const handle = fs.openSync(file, 'r');
  const header = Buffer.alloc(4096);
  let length;
  try {
    length = fs.readSync(handle, header, 0, header.length, 0);
  } finally {
    fs.closeSync(handle);
  }
  if (length < 64) throw new Error(`Executable header is missing: ${file}`);
  if (targetPlatform === 'windows') {
    if (header.toString('ascii', 0, 2) !== 'MZ') throw new Error(`Not a Windows PE executable: ${file}`);
    const pe = header.readUInt32LE(0x3c);
    if (pe + 6 > length || header.toString('ascii', pe, pe + 4) !== 'PE\0\0') throw new Error(`Invalid PE header: ${file}`);
    const arch = ({ 0x8664: 'amd64', 0xaa64: 'arm64' })[header.readUInt16LE(pe + 4)];
    return arch ? [arch] : [];
  }
  const architectures = { 0x01000007: 'amd64', 0x0100000c: 'arm64' };
  if (header.readUInt32LE(0) === 0xfeedfacf) {
    const arch = architectures[header.readUInt32LE(4)];
    return arch ? [arch] : [];
  }
  const magic = header.readUInt32BE(0);
  if (magic === 0xcafebabe || magic === 0xcafebabf) {
    const count = header.readUInt32BE(4);
    const stride = magic === 0xcafebabe ? 20 : 32;
    if (count > 32 || 8 + count * stride > length) throw new Error(`Invalid universal Mach-O header: ${file}`);
    const result = [];
    for (let i = 0; i < count; i++) {
      const arch = architectures[header.readUInt32BE(8 + i * stride)];
      if (arch) result.push(arch);
    }
    return result;
  }
  throw new Error(`Not a supported macOS Mach-O executable: ${file}`);
}

function main() {
  const options = parseArguments();
  options['rg-version'] ||= process.env.RG_VERSION;
  options['rg-sha256'] ||= process.env.RG_SHA256;
  if (options['wails-target']) {
    if (!options.runtimeOnly) throw new Error('--wails-target requires --runtime-only.');
    const match = /^(darwin|windows)\/(arm64|amd64)$/.exec(options['wails-target']);
    if (!match) throw new Error('Unsupported Wails target. Build a macOS or Windows arm64/amd64 application.');
    options.platform = match[1] === 'darwin' ? 'macos' : 'windows';
    options.arch = match[2];
  }
  if (options.runtimeOnly && (options.out || options.cli || options.noArchive)) {
    throw new Error('--runtime-only operates on --app; --out, --cli and --no-archive are not supported.');
  }
  if (!['darwin', 'win32'].includes(process.platform)) {
    throw new Error('Native packaging currently supports macOS and Windows. Run on the target operating system.');
  }
  const hostOS = process.platform === 'darwin' ? 'macos' : 'windows';
  const hostArchitecture = process.arch === 'x64' ? 'amd64' : process.arch;
  const osName = options.platform || hostOS;
  const architecture = options.arch || hostArchitecture;
  if (!['macos', 'windows'].includes(osName)) throw new Error('--platform must be macos or windows.');
  if (!['arm64', 'amd64'].includes(architecture)) throw new Error('--arch must be arm64 or amd64.');
  const mac = osName === 'macos';
  if (mac && hostOS !== 'macos') throw new Error('macOS bundles must be assembled on macOS to preserve bundle structure and modes.');
  if (options.adhocSign && (!mac || hostOS !== 'macos')) throw new Error('--adhoc-sign requires a macOS target and host.');
  const cross = osName !== hostOS || architecture !== hostArchitecture;
  const appName = mac ? 'OneByOne.app' : 'OneByOne.exe';
  const cliName = mac ? 'onebyone' : 'onebyone-cli.exe';
  assertUniqueNames([appName, cliName, 'bin', 'tools', 'licenses', 'README.md', 'THIRD_PARTY_LICENSES.txt', 'docs', 'build-info.json']);
  let app = options.app || path.join(repository, 'build', 'bin', appName);
  // Wails' ${bin} is the executable inside the macOS bundle, not the .app
  // directory. Resolve that layout so signing and the standalone CLI copy
  // target the same locations as a distribution package.
  if (options.runtimeOnly && mac && path.basename(path.dirname(app)) === 'MacOS') {
    const contents = path.dirname(path.dirname(app));
    const bundle = path.dirname(contents);
    if (path.basename(contents) === 'Contents' && bundle.endsWith('.app')) app = bundle;
  }
  const cli = options.cli || path.join(repository, 'build', 'bin', cliName);
  const out = options.out || path.join(repository, 'build', 'package', `OneByOne-${osName}-${architecture}`);
  const archive = `${out}.zip`;
  const rg = findRG(options.rg || process.env.RG_BINARY, !mac);
  const licenses = options['rg-license-dir'] || process.env.RG_LICENSE_DIR || path.dirname(rg);
  const appBundle = mac && app.endsWith('.app');
  const appExecutable = appBundle ? path.join(app, 'Contents', 'MacOS', 'OneByOne') : app;

  if (!options.runtimeOnly && (fs.existsSync(out) || (!options.noArchive && fs.existsSync(archive)))) {
    throw new Error(`Output already exists. Use a fresh --out path or remove the previous generated package: ${out}`);
  }
  if (!options.runtimeOnly && (inside(app, out) || inside(out, app) || inside(out, cli) || inside(out, rg))) {
    throw new Error('Package output must be separate from its input files.');
  }
  if (mac && (!options.runtimeOnly || appBundle)) {
    if (!fs.statSync(app, { throwIfNoEntry: false })?.isDirectory()) throw new Error(`App bundle is missing: ${app}`);
    requireFile(path.join(app, 'Contents', 'MacOS', 'OneByOne'), 'App executable');
  } else {
    requireFile(app, 'Desktop executable');
  }
  if (!options.runtimeOnly) requireFile(cli, 'CLI executable');
  requireFile(path.join(repository, 'THIRD_PARTY_LICENSES.txt'), 'Third-party license notices');
  const licenseNames = ['COPYING', 'LICENSE-MIT', 'UNLICENSE'];
  for (const name of licenseNames) requireFile(path.join(licenses, name), `Original ripgrep ${name}`);
  const rgHash = createHash('sha256').update(fs.readFileSync(rg)).digest('hex');
  for (const executable of [appExecutable, ...(options.runtimeOnly ? [] : [cli]), rg]) {
    if (!binaryArchitectures(executable, osName).includes(architecture)) throw new Error(`Executable does not match ${osName}/${architecture}: ${executable}`);
  }
  if (options['rg-sha256'] && !/^[0-9a-f]{64}$/i.test(options['rg-sha256'])) throw new Error('--rg-sha256 must contain 64 hexadecimal digits.');
  if (options['rg-sha256'] && options['rg-sha256'].toLowerCase() !== rgHash) throw new Error('ripgrep binary SHA256 mismatch.');
  let version;
  let versionVerification;
  if (cross) {
    if (!options['rg-version'] || !options['rg-sha256']) {
      throw new Error('Foreign rg is never executed. Cross packaging requires --rg-version and --rg-sha256 from a verified official archive.');
    }
    if (!/^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][a-zA-Z0-9.-]+)?$/.test(options['rg-version'])) throw new Error('Invalid declared ripgrep version.');
    version = options['rg-version'];
    versionVerification = 'Declared from verified archive; binary SHA256 checked. Foreign binary was not executed.';
  } else {
    const versionOutput = execFileSync(rg, ['--no-config', '--version'], { encoding: 'utf8', timeout: 10000, windowsHide: true, maxBuffer: 65536 });
    version = /^ripgrep\s+([0-9]+\.[0-9]+\.[0-9]+[^\s]*)/m.exec(versionOutput)?.[1];
    if (!version) throw new Error('The provided binary did not report a ripgrep version.');
    if (options['rg-version'] && options['rg-version'] !== version) throw new Error('Declared ripgrep version does not match --version output.');
    versionVerification = 'Executed native rg --version.';
  }
  const wails = JSON.parse(fs.readFileSync(path.join(repository, 'wails.json'), 'utf8'));
  const git = inspectGitBundle(options, { platform: osName, architecture });
  if (!options.runtimeOnly && (inside(git.root, canonicalBuildPath(out)) || inside(canonicalBuildPath(out), git.root))) throw new Error('Package output must be separate from the Git bundle input.');
  const verifyGitAt = destination => {
    if (git.native) verifyNativeGit(destination, path.join(destination, ...git.executableRelative.split('/')), git.metadata.version);
  };
  const populateGit = destination => {
    copyGitBundle(git, destination);
    if (mac && (options.runtimeOnly || options.adhocSign)) signMacGitBundle(destination);
    verifyGitAt(destination);
  };

  if (options.runtimeOnly) {
    // Wails' output is launchable directly from Finder/Explorer. Populate the
    // exact locations used by the runtime resolver before calling it a build.
    const outputRoot = path.dirname(app);
    const rgDestinations = [path.join(outputRoot, 'bin', mac ? 'rg' : 'rg.exe')];
    const licenseDestinations = [path.join(outputRoot, 'licenses')];
    const gitDestinations = [path.join(outputRoot, 'tools', 'git')];
    if (appBundle) {
      rgDestinations.push(path.join(app, 'Contents', 'MacOS', 'bin', 'rg'));
      licenseDestinations.push(path.join(app, 'Contents', 'Resources', 'licenses'));
      gitDestinations.push(path.join(app, 'Contents', 'MacOS', 'tools', 'git'));
    }
    for (const destination of rgDestinations) {
      // Avoid modifying the supplied executable during the bundle signature.
      if (fs.existsSync(destination) && fs.realpathSync(destination) === rg) {
        throw new Error('The rg input must be separate from the build output.');
      }
    }
    for (const destination of rgDestinations) copyFile(rg, destination, true);
    for (const destination of licenseDestinations) {
      copyFile(path.join(repository, 'THIRD_PARTY_LICENSES.txt'), path.join(destination, 'THIRD_PARTY_LICENSES.txt'));
      for (const name of licenseNames) copyFile(path.join(licenses, name), path.join(destination, 'ripgrep', name));
    }
    for (const destination of gitDestinations) populateGit(destination);
    if (appBundle) {
      execFileSync('/usr/bin/codesign', ['--force', '--deep', '--sign', '-', app], { stdio: 'inherit' });
      execFileSync('/usr/bin/codesign', ['--verify', '--deep', '--strict', app], { stdio: 'inherit' });
      verifyGitAt(path.join(app, 'Contents', 'MacOS', 'tools', 'git'));
    }
    process.stdout.write(`Bundled Git ${git.metadata.version} and ripgrep ${version} for ${osName}/${architecture}.\n`);
    return;
  }

  fs.mkdirSync(out, { recursive: true });
  if (mac) {
    fs.cpSync(app, path.join(out, appName), { recursive: true, dereference: false, verbatimSymlinks: true });
    copyFile(rg, path.join(out, appName, 'Contents', 'MacOS', 'bin', 'rg'), true);
    copyFile(path.join(repository, 'THIRD_PARTY_LICENSES.txt'), path.join(out, appName, 'Contents', 'Resources', 'licenses', 'THIRD_PARTY_LICENSES.txt'));
    for (const name of licenseNames) copyFile(path.join(licenses, name), path.join(out, appName, 'Contents', 'Resources', 'licenses', 'ripgrep', name));
    populateGit(path.join(out, appName, 'Contents', 'MacOS', 'tools', 'git'));
  } else {
    copyFile(app, path.join(out, appName));
  }
  copyFile(cli, path.join(out, cliName), true);
  // The standalone CLI also discovers its own adjacent bin/rg, so it works
  // independently of the .app bundle's install location.
  copyFile(rg, path.join(out, 'bin', mac ? 'rg' : 'rg.exe'), true);
  populateGit(path.join(out, 'tools', 'git'));
  for (const name of licenseNames) copyFile(path.join(licenses, name), path.join(out, 'licenses', 'ripgrep', name));
  copyFile(path.join(repository, 'README.md'), path.join(out, 'README.md'));
  copyFile(path.join(repository, 'THIRD_PARTY_LICENSES.txt'), path.join(out, 'THIRD_PARTY_LICENSES.txt'));
  fs.cpSync(path.join(repository, 'docs'), path.join(out, 'docs'), { recursive: true });

  const notice = `ripgrep ${version}

Source: https://github.com/BurntSushi/ripgrep
Release: https://github.com/BurntSushi/ripgrep/releases/tag/${version}
Binary SHA256: ${rgHash}
Version verification: ${versionVerification}

COPYING, LICENSE-MIT, and UNLICENSE are copied without modification from the
supplied ripgrep distribution. They are the upstream notices and license texts.
This notice does not replace them or assert that other dependencies have been audited.
`;
  fs.writeFileSync(path.join(out, 'licenses', 'ripgrep', 'NOTICE.txt'), notice, 'utf8');
  if (mac) fs.writeFileSync(path.join(out, appName, 'Contents', 'Resources', 'licenses', 'ripgrep', 'NOTICE.txt'), notice, 'utf8');
  fs.writeFileSync(path.join(out, 'build-info.json'), JSON.stringify({
    product: 'OneByOne', version: wails.info?.productVersion || 'unknown', platform: osName, architecture,
    createdAt: new Date().toISOString(), ripgrep: { version, sha256: rgHash, verification: versionVerification }, git: git.metadata,
    packagingHost: `${hostOS}/${hostArchitecture}`, crossPackaged: cross, guiRuntimeVerifiedByPackager: false,
    signing: options.adhocSign ? 'Local ad hoc signature only. No Developer ID signature or notarization.' : 'No signing performed by this packaging script.',
  }, null, 2) + '\n', 'utf8');

  if (options.adhocSign) {
    // Adding rg changes Wails' original resource seal. Explicit ad hoc signing
    // repairs local loadability; it is not a trusted distribution signature.
    execFileSync('/usr/bin/codesign', ['--force', '--deep', '--sign', '-', path.join(out, appName)], { stdio: 'inherit' });
    execFileSync('/usr/bin/codesign', ['--verify', '--deep', '--strict', path.join(out, appName)], { stdio: 'inherit' });
    verifyGitAt(path.join(out, appName, 'Contents', 'MacOS', 'tools', 'git'));
  }
  assertUniqueNames(packagePaths(out));

  if (!options.noArchive) {
    if (hostOS === 'macos') {
      execFileSync('/usr/bin/ditto', ['-c', '-k', '--sequesterRsrc', '--keepParent', out, archive], { stdio: 'inherit' });
    } else {
      // Pass paths through the environment rather than interpolating them into
      // PowerShell syntax. Quotes, spaces and dollar signs remain literal.
      execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command',
        "$ErrorActionPreference = 'Stop'; Compress-Archive -LiteralPath $env:ONEBYONE_PACKAGE_DIR -DestinationPath $env:ONEBYONE_PACKAGE_ZIP -CompressionLevel Optimal"],
      { stdio: 'inherit', windowsHide: true, env: { ...process.env, ONEBYONE_PACKAGE_DIR: out, ONEBYONE_PACKAGE_ZIP: archive } });
    }
  }
  process.stdout.write(JSON.stringify({ directory: out, archive: options.noArchive ? null : archive, ripgrep: version }, null, 2) + '\n');
}

try {
  main();
} catch (error) {
  process.stderr.write(`Packaging failed: ${error.message}\n`);
  process.exitCode = 1;
}
