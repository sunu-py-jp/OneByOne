#!/usr/bin/env node
// Development entry points shared by PowerShell, cmd.exe, and Unix shells.
// Tool installation remains explicit; runtime bundling uses the Wails hook.
import fs from 'node:fs';
import path from 'node:path';
import { spawnSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { prepareRuntime } from './prepare-runtime.mjs';

const repository = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const frontend = path.join(repository, 'frontend');
const hostOS = ({ darwin: 'darwin', win32: 'windows' })[process.platform];
const hostArch = process.arch === 'x64' ? 'amd64' : process.arch;

function usage() {
  process.stdout.write(`Usage:
  npm test [-- --race --timeout 30m]
  npm run build [-- options]
  npm run package [-- options]
  node scripts/dev.mjs <test|build|package> [options]

test:    Frontend typecheck, scripts/*.test.mjs, Go tests, and Go vet.
build:   Frontend once, Wails GUI + bundled runtime, then standalone CLI.
package: Build, then assemble a ZIP and, on Windows, an offline setup EXE.

Options:
  --platform OS[/ARCH]    macos (or darwin) / windows; default: host OS/CPU
  --arch ARCH             arm64 or amd64 (when omitted from --platform)
  --rg PATH               Override the pinned rg executable (or RG_BINARY)
  --rg-license-dir PATH   Original COPYING/LICENSE-MIT/UNLICENSE directory
                          (or RG_LICENSE_DIR, then rg's directory)
  --rg-version VERSION   Cross-build rg version (or RG_VERSION)
  --rg-sha256 HEX        Cross-build rg binary SHA256 (or RG_SHA256)
  --git-bundle-dir PATH  Prepared portable Git (or GIT_BUNDLE_DIR)
  --git-bundle-version V Git version (or GIT_BUNDLE_VERSION)
  --git-bundle-sha256 H  Verified Git archive SHA256 (or GIT_BUNDLE_SHA256)
  --git-bundle-source U  Git archive source URL (or GIT_BUNDLE_SOURCE)
  --webview2-installer P Offline Evergreen installer (or WEBVIEW2_INSTALLER)
  --webview2-sha256 H    Installer SHA256 (or WEBVIEW2_SHA256)
  --makensis PATH        NSIS compiler (or MAKENSIS_BINARY)
  --no-installer         Windows: directory/ZIP only, for separate signing
  --race                  Go race detector (test only; native toolchain required)
  --timeout DURATION      Go timeout per package (test only; Windows: 30m,
                          other hosts: 10m; for example 20m or 1h30m)
  --out PATH              New package directory (package only)
  --no-archive            Prepare package directory without ZIP (package only)
  --adhoc-sign            Repair local macOS package signature (default on Mac)
  --no-adhoc-sign         Leave final signing to a separate step (package only)
  --skip-build            Package existing build/bin outputs (package only)
  -h, --help              Show this help

Go/Wails: GO_BINARY and WAILS_BINARY may point to installed executables;
otherwise they are resolved from PATH. Build tools are installed separately.
Git/rg/WebView2 inputs are fetched from build/runtime-lock.json and cached.
Run npm --prefix frontend ci once after checkout or lockfile changes.
Tests also use the prepared native Git/rg; no separate Git install is needed.
Builds require original rg licenses. Cross builds additionally require its
verified version and binary SHA256. macOS apps must be built on macOS.
See docs/build.md for outputs and prerequisites.
`);
}

function parseArguments(argv) {
  const command = argv[0];
  if (!command || command === '--help' || command === '-h') return { help: true };
  if (!['test', 'build', 'package'].includes(command)) throw new Error(`Unknown command: ${command}`);
  const options = { command };
  const common = ['--rg', '--rg-license-dir', '--git-bundle-dir', '--git-bundle-version', '--git-bundle-sha256', '--git-bundle-source'];
  const build = ['--platform', '--arch', '--rg-version', '--rg-sha256'];
  const values = [...common, ...(command === 'test' ? ['--timeout'] : build), ...(command === 'package' ? ['--out', '--webview2-installer', '--webview2-sha256', '--makensis'] : [])];
  const flags = command === 'test' ? ['--race'] : command === 'package' ? ['--no-archive', '--adhoc-sign', '--no-adhoc-sign', '--skip-build', '--no-installer'] : [];
  for (let i = 1; i < argv.length; i++) {
    const key = argv[i];
    if (key === '--help' || key === '-h') return { help: true };
    if (flags.includes(key)) {
      options[key.slice(2)] = true;
    } else if (values.includes(key)) {
      const value = argv[++i];
      if (!value || value.startsWith('--')) throw new Error(`${key} requires a value`);
      options[key.slice(2)] = value;
    } else {
      throw new Error(`Unknown option for ${command}: ${key}`);
    }
  }
  if (options['adhoc-sign'] && options['no-adhoc-sign']) throw new Error('--adhoc-sign and --no-adhoc-sign cannot be combined.');
  if (options.timeout && (!/^(?:\d+(?:\.\d+)?(?:ns|us|µs|μs|ms|s|m|h))+$/.test(options.timeout) || !/[1-9]/.test(options.timeout))) {
    throw new Error('--timeout requires a positive Go duration, such as 20m or 1h30m.');
  }
  return options;
}

function run(label, executable, args, env, cwd = repository) {
  process.stdout.write(`\n[${label}]\n`);
  const result = spawnSync(executable, args, { cwd, env, stdio: 'inherit', windowsHide: true, shell: false });
  if (result.error) throw new Error(`${label}: ${result.error.message}`);
  if (result.status !== 0) throw new Error(`${label} failed (${result.signal || `exit ${result.status}`}).`);
}

function requireFile(file, message) {
  if (!fs.statSync(file, { throwIfNoEntry: false })?.isFile()) throw new Error(`${message}: ${file}`);
  return file;
}

function resolveTool(name, configured, env) {
  const requested = configured || name;
  const isPath = path.isAbsolute(requested) || requested.includes('/') || requested.includes('\\');
  const names = process.platform === 'win32' && !path.extname(requested) ? [`${requested}.exe`, requested] : [requested];
  const candidates = isPath ? names.map(candidate => path.resolve(candidate)) :
    (env.PATH || '').split(path.delimiter).filter(Boolean).flatMap(directory =>
      names.map(candidate => path.join(directory.replace(/^"|"$/g, ''), candidate)));
  for (const candidate of candidates) {
    try {
      if (!fs.statSync(candidate).isFile()) continue;
      fs.accessSync(candidate, process.platform === 'win32' ? fs.constants.F_OK : fs.constants.X_OK);
      return path.resolve(candidate);
    } catch { /* Try the next PATH entry. */ }
  }
  const variable = ({ rg: 'RG_BINARY', go: 'GO_BINARY', wails: 'WAILS_BINARY' })[name];
  throw new Error(`${name} was not found or is not executable: ${requested}. Install it and add it to PATH${variable ? `, or set ${variable} to its executable path` : ''}.`);
}

function prependPath(env, directories) {
  const result = { ...env };
  let previous = env.PATH || '';
  // Windows environment names are case-insensitive, including an inherited Path.
  if (process.platform === 'win32') {
    for (const key of Object.keys(result)) {
      if (key.toLowerCase() !== 'path') continue;
      previous = result[key];
      delete result[key];
    }
  }
  result.PATH = [...new Set(directories.filter(Boolean)), previous].filter(Boolean).join(path.delimiter);
  return result;
}

function environment(options, inherited) {
  let env = prependPath(inherited, [path.dirname(process.execPath)]);
  for (const [option, variable] of [['rg', 'RG_BINARY'], ['rg-license-dir', 'RG_LICENSE_DIR'], ['rg-version', 'RG_VERSION'], ['rg-sha256', 'RG_SHA256'], ['git-bundle-dir', 'GIT_BUNDLE_DIR'], ['git-bundle-version', 'GIT_BUNDLE_VERSION'], ['git-bundle-sha256', 'GIT_BUNDLE_SHA256'], ['git-bundle-source', 'GIT_BUNDLE_SOURCE'], ['webview2-installer', 'WEBVIEW2_INSTALLER'], ['webview2-sha256', 'WEBVIEW2_SHA256'], ['makensis', 'MAKENSIS_BINARY']]) {
    if (options[option]) env[variable] = options[option];
  }
  // Wails executes its hook from build/bin, so relative runtime paths must be
  // resolved before passing them through that process boundary.
  for (const variable of ['RG_BINARY', 'RG_LICENSE_DIR', 'GIT_BUNDLE_DIR', 'WEBVIEW2_INSTALLER']) {
    if (env[variable]) env[variable] = path.resolve(env[variable]);
  }
  return env;
}

function target(options) {
  if (!hostOS) throw new Error('GUI build and packaging currently require a macOS or Windows host.');
  const parts = (options.platform || hostOS).split('/');
  const os = parts[0] === 'macos' ? 'darwin' : parts[0];
  if (parts.length > 2 || !['darwin', 'windows'].includes(os)) throw new Error('--platform must be macos[/ARCH], darwin[/ARCH], or windows[/ARCH].');
  if (parts.length === 2 && !parts[1]) throw new Error('--platform requires an architecture after /.');
  if (parts[1] && options.arch && parts[1] !== options.arch) throw new Error('--platform and --arch specify different architectures.');
  const arch = parts[1] || options.arch || hostArch;
  if (!['arm64', 'amd64'].includes(arch)) throw new Error('--arch must be arm64 or amd64.');
  if (os === 'darwin' && hostOS !== 'darwin') throw new Error('macOS applications must be built and packaged on macOS.');
  return { os, arch, wails: `${os}/${arch}`, packageOS: os === 'darwin' ? 'macos' : 'windows' };
}

function frontendTypecheck(env) {
  const tsc = requireFile(path.join(frontend, 'node_modules', 'typescript', 'bin', 'tsc'), 'Frontend dependencies are missing. Run npm --prefix frontend ci');
  run('Frontend typecheck', process.execPath, [tsc, '--noEmit'], env, frontend);
}

function tests(options, env) {
  const go = resolveTool('go', env.GO_BINARY, env);
  const rg = resolveTool('rg', env.RG_BINARY, env);
  const expectedRGName = process.platform === 'win32' ? 'rg.exe' : 'rg';
  if (path.basename(rg).toLowerCase() !== expectedRGName) throw new Error(`Tests discover ${expectedRGName} on PATH; use the original executable with that filename.`);
  const gitDirectory = env.GIT_BUNDLE_DIR && path.join(env.GIT_BUNDLE_DIR, process.platform === 'win32' ? 'cmd' : 'bin');
  env = prependPath(env, [path.dirname(go), path.dirname(rg), gitDirectory]);
  resolveTool('git', undefined, env);
  run('Native ripgrep prerequisite', rg, ['--no-config', '--version'], env);
  frontendTypecheck(env);
  const nodeTests = fs.readdirSync(path.join(repository, 'scripts')).filter(name => name.endsWith('.test.mjs')).sort()
    .map(name => path.join(repository, 'scripts', name));
  if (nodeTests.length) run('Node tests', process.execPath, ['--test', ...nodeTests], env);
  const timeout = options.timeout || (process.platform === 'win32' ? '30m' : '10m');
  run('Go tests', go, ['test', `-timeout=${timeout}`, ...(options.race ? ['-race'] : []), './internal/...', './cmd/...'], env);
  run('Go vet', go, ['vet', './internal/...', './cmd/...'], env);
}

function build(options, env, destination) {
  const go = resolveTool('go', env.GO_BINARY, env);
  const wails = resolveTool('wails', env.WAILS_BINARY, env);
  const expectedGoName = process.platform === 'win32' ? 'go.exe' : 'go';
  if (path.basename(go).toLowerCase() !== expectedGoName) throw new Error(`Wails discovers ${expectedGoName} on PATH; GO_BINARY must point to the standard Go executable with that filename.`);
  env = prependPath(env, [path.dirname(go), path.dirname(process.execPath)]);
  frontendTypecheck(env);
  const vite = requireFile(path.join(frontend, 'node_modules', 'vite', 'bin', 'vite.js'), 'Frontend dependencies are missing. Run npm --prefix frontend ci');
  run('Frontend build', process.execPath, [vite, 'build'], env, frontend);
  const buildEnv = { ...env, CGO_ENABLED: destination.os === 'darwin' ? '1' : '0' };
  if (destination.os === 'darwin') {
    buildEnv.CGO_CFLAGS ??= '-mmacosx-version-min=13.0';
    buildEnv.CGO_LDFLAGS ??= '-mmacosx-version-min=13.0';
  }
  // -s prevents Wails from rebuilding the frontend. Keep postBuildHooks on:
  // package.mjs validates and bundles Git, rg and notices before this succeeds.
  run('Wails GUI and runtime', wails, ['build', '-s', '-platform', destination.wails,
    ...(destination.os === 'windows' ? ['-webview2', 'embed'] : [])], buildEnv);
  const cliName = destination.os === 'windows' ? 'onebyone-cli.exe' : 'onebyone';
  const cli = path.join(repository, 'build', 'bin', cliName);
  fs.mkdirSync(path.dirname(cli), { recursive: true });
  // Wails temporarily produces OneByOne on macOS. Build the lowercase CLI
  // afterward so a case-insensitive filesystem cannot overwrite it with GUI.
  run('Standalone CLI', go, ['build', '-trimpath', '-o', cli, './cmd/onebyone'],
    { ...buildEnv, GOOS: destination.os, GOARCH: destination.arch });
}

function packageBuild(options, env, destination) {
  if (!options['skip-build']) build(options, env, destination);
  const args = [path.join(repository, 'scripts', 'package.mjs'), '--platform', destination.packageOS, '--arch', destination.arch];
  // Packaging adds NOTICE files to the bundle, invalidating Wails' earlier
  // resource seal. Repair it by default so a normal Mac package is launchable.
  if (options['adhoc-sign'] || (destination.os === 'darwin' && !options['no-adhoc-sign'])) args.push('--adhoc-sign');
  for (const flag of ['out', 'no-archive']) {
    if (!options[flag]) continue;
    args.push(`--${flag}`);
    if (flag === 'out') args.push(path.resolve(options[flag]));
  }
  run('Distribution package', process.execPath, args, env);
  if (destination.os === 'windows' && !options['no-installer']) {
    const directory = path.resolve(options.out || path.join(repository, 'build', 'package', `OneByOne-windows-${destination.arch}`));
    const version = JSON.parse(fs.readFileSync(path.join(repository, 'wails.json'), 'utf8')).info.productVersion;
    const installerArgs = [path.join(repository, 'scripts', 'windows-installer.mjs'), '--directory', directory,
      '--webview2-installer', env.WEBVIEW2_INSTALLER, '--webview2-sha256', env.WEBVIEW2_SHA256,
      '--arch', destination.arch, '--version', version];
    if (env.MAKENSIS_BINARY) installerArgs.push('--makensis', env.MAKENSIS_BINARY);
    run('Windows offline setup', process.execPath, installerArgs, env);
  }
}

export async function main(argv = process.argv.slice(2), inherited = process.env) {
  const options = parseArguments(argv);
  if (options.help) {
    usage();
  } else {
    const destination = target(options);
    const env = await prepareRuntime({ platform: destination.os, arch: destination.arch,
      env: environment(options, inherited), installer: options.command === 'package' && !options['no-installer'] });
    if (options.command === 'test') tests(options, env);
    else if (options.command === 'build') build(options, env, destination);
    else packageBuild(options, env, destination);
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    await main();
  } catch (error) {
    process.stderr.write(`Development command failed: ${error.message}\n`);
    process.exitCode = 1;
  }
}
