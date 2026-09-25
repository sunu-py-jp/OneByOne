// Validate and copy an acquired Git distribution. Downloading and verifying the
// upstream archives belongs to the runtime preparation command, not packaging.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';

const pinnedWindowsARM64Git = JSON.parse(fs.readFileSync(new URL('../build/runtime-lock.json', import.meta.url), 'utf8')).targets['windows/arm64'].git;

function sha256(file) {
  const hash = createHash('sha256');
  const chunk = Buffer.alloc(1024 * 1024);
  const fd = fs.openSync(file, 'r');
  try {
    let count;
    while ((count = fs.readSync(fd, chunk, 0, chunk.length, null)) > 0) hash.update(chunk.subarray(0, count));
  } finally { fs.closeSync(fd); }
  return hash.digest('hex');
}

function inside(root, file) {
  const relative = path.relative(root, file);
  return relative === '' || (relative !== '..' && !relative.startsWith(`..${path.sep}`) && !path.isAbsolute(relative));
}

export function canonicalBuildPath(file) {
  const absolute = path.resolve(file);
  const missing = [];
  let ancestor = absolute;
  while (!fs.existsSync(ancestor)) {
    const parent = path.dirname(ancestor);
    if (parent === ancestor) throw new Error(`Build path has no existing parent: ${file}`);
    missing.unshift(path.basename(ancestor));
    ancestor = parent;
  }
  return path.join(fs.realpathSync(ancestor), ...missing);
}

function requiredFile(file, label) {
  const stat = fs.statSync(file, { throwIfNoEntry: false });
  if (!stat?.isFile() || stat.size === 0) throw new Error(`${label} is missing or empty: ${file}`);
}

// Inspect every executable and library without running foreign code. Ordinary
// scripts, source archives and documentation return null.
export function gitBinaryInfo(file) {
  const fd = fs.openSync(file, 'r');
  const header = Buffer.alloc(4096);
  let size;
  try { size = fs.readSync(fd, header, 0, header.length, 0); } finally { fs.closeSync(fd); }
  if (size < 4) return null;
  if (header.toString('ascii', 0, 2) === 'MZ') {
    if (size < 64) throw new Error(`Invalid Git PE header: ${file}`);
    const offset = header.readUInt32LE(0x3c);
    if (offset + 6 > size || header.toString('ascii', offset, offset + 4) !== 'PE\0\0') throw new Error(`Invalid Git PE header: ${file}`);
    // Credential managers may ship cross-platform .NET assemblies. Their PE
    // machine value can be i386/AnyCPU even in an ARM64 macOS distribution.
    const optional = offset + 24;
    if (optional + 2 <= size) {
      const magic = header.readUInt16LE(optional);
      const cliDirectory = optional + (magic === 0x20b ? 112 : 96) + 14 * 8;
      if ((magic === 0x10b || magic === 0x20b) && cliDirectory + 8 <= size && header.readUInt32LE(cliDirectory) !== 0) return { managed: true };
    }
    return { platform: 'windows', architectures: [({ 0x14c: '386', 0x8664: 'amd64', 0xaa64: 'arm64' })[header.readUInt16LE(offset + 4)]].filter(Boolean) };
  }
  const architectures = { 0x01000007: 'amd64', 0x0100000c: 'arm64' };
  if (header.readUInt32LE(0) === 0xfeedfacf) {
    if (size < 32) throw new Error(`Invalid Git Mach-O header: ${file}`);
    return { platform: 'macos', architectures: [architectures[header.readUInt32LE(4)]].filter(Boolean) };
  }
  const magic = header.readUInt32BE(0);
  if (magic === 0xcafebabe || magic === 0xcafebabf) {
    const count = header.readUInt32BE(4);
    const stride = magic === 0xcafebabe ? 20 : 32;
    if (count > 32 || 8 + count * stride > size) throw new Error(`Invalid Git universal Mach-O header: ${file}`);
    return { platform: 'macos', architectures: Array.from({ length: count }, (_, i) => architectures[header.readUInt32BE(8 + i * stride)]).filter(Boolean) };
  }
  if (header.subarray(0, 4).equals(Buffer.from([0x7f, 0x45, 0x4c, 0x46]))) return { platform: 'linux', architectures: [] };
  return null;
}

export function inspectGitTree(directory) {
  const root = fs.realpathSync(directory);
  const files = [];
  const names = new Set();
  function walk(current) {
    for (const entry of fs.readdirSync(current, { withFileTypes: true })) {
      const file = path.join(current, entry.name);
      const relative = path.relative(root, file).split(path.sep).join('/');
      const folded = relative.normalize('NFC').toLowerCase();
      if (names.has(folded)) throw new Error(`Case-insensitive Git bundle path collision: ${relative}`);
      names.add(folded);
      if (entry.isSymbolicLink()) {
        const target = fs.readlinkSync(file);
        if (path.isAbsolute(target) || /^[a-z]:/i.test(target) || target.startsWith('\\') || !inside(root, path.resolve(path.dirname(file), target))) {
          throw new Error(`Git bundle symlink must remain relative and inside its bundle: ${relative}`);
        }
        let resolved;
        try { resolved = fs.realpathSync(file); } catch { throw new Error(`Git bundle symlink is broken or cyclic: ${relative}`); }
        if (!inside(root, resolved)) throw new Error(`Git bundle symlink escapes its bundle: ${relative}`);
      } else if (entry.isDirectory()) walk(file);
      else if (entry.isFile()) files.push({ file, relative });
      else throw new Error(`Unsupported Git bundle entry: ${relative}`);
    }
  }
  walk(root);
  return { root, files };
}

export function gitRuntimePrefix(root, platform) {
  if (platform !== 'windows') return root;
  for (const name of ['clangarm64', 'mingw64', 'mingwarm64', 'mingw32']) {
    const prefix = path.join(root, name);
    if (fs.statSync(path.join(prefix, 'libexec', 'git-core'), { throwIfNoEntry: false })?.isDirectory() &&
        fs.statSync(path.join(prefix, 'share', 'git-core', 'templates'), { throwIfNoEntry: false })?.isDirectory()) return prefix;
  }
  throw new Error('Bundled Windows Git helper/template directories are missing.');
}

function isolatedGitEnvironment(root, home) {
  const env = {};
  // Keep platform services such as SystemRoot/TEMP, while dropping Git config,
  // hooks, custom helpers and credentials inherited from the developer shell.
  for (const [key, value] of Object.entries(process.env)) {
    if (/^(git_|dyld_|ld_|home$|userprofile$|xdg_config_home$|path$)/i.test(key)) continue;
    env[key] = value;
  }
  const prefix = gitRuntimePrefix(root, process.platform === 'win32' ? 'windows' : 'macos');
  return { ...env, HOME: home, USERPROFILE: home, XDG_CONFIG_HOME: home,
    PATH: [...new Set([path.join(root, 'bin'), path.join(root, 'cmd'), path.join(prefix, 'bin'), path.join(root, 'usr', 'bin')])].join(path.delimiter),
    GIT_CONFIG_NOSYSTEM: '1', GIT_CONFIG_GLOBAL: process.platform === 'win32' ? 'NUL' : '/dev/null',
    GIT_EXEC_PATH: path.join(prefix, 'libexec', 'git-core'), GIT_TEMPLATE_DIR: path.join(prefix, 'share', 'git-core', 'templates'),
    GIT_TERMINAL_PROMPT: '0', GIT_AUTHOR_NAME: 'OneByOne build verification', GIT_AUTHOR_EMAIL: 'build@localhost',
    GIT_COMMITTER_NAME: 'OneByOne build verification', GIT_COMMITTER_EMAIL: 'build@localhost' };
}

export function verifyNativeGit(root, executable, expectedVersion) {
  const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-git-verification-'));
  const env = isolatedGitEnvironment(root, temporary);
  const run = args => execFileSync(executable, args, { env, cwd: temporary, encoding: 'utf8', timeout: 30000, windowsHide: true, maxBuffer: 1024 * 1024 });
  try {
    const version = run(['--version']).trim();
    if (version !== `git version ${expectedVersion}`) throw new Error(`Bundled Git version mismatch: expected ${expectedVersion}, received ${version}`);
    const repository = path.join(temporary, 'repository with spaces');
    const worktree = path.join(temporary, 'worktree with spaces');
    run(['-c', 'init.defaultBranch=main', 'init', repository]);
    run(['-C', repository, '-c', 'commit.gpgsign=false', '-c', 'core.hooksPath=', 'commit', '--allow-empty', '-m', 'Verify bundled Git']);
    run(['-C', repository, '-c', 'core.hooksPath=', 'worktree', 'add', '--detach', worktree, 'HEAD']);
    if (run(['-C', worktree, 'rev-parse', '--is-inside-work-tree']).trim() !== 'true') throw new Error('Bundled Git worktree verification failed.');
    return version;
  } finally { fs.rmSync(temporary, { recursive: true, force: true }); }
}

export function inspectGitBundle(options, target, environment = process.env) {
  const configured = options['git-bundle-dir'] || environment.GIT_BUNDLE_DIR;
  if (!configured) throw new Error('Git bundle is required. Run runtime preparation or pass --git-bundle-dir / GIT_BUNDLE_DIR.');
  const { root, files } = inspectGitTree(path.resolve(configured));
  const version = options['git-bundle-version'] || environment.GIT_BUNDLE_VERSION;
  const archiveSha256 = options['git-bundle-sha256'] || environment.GIT_BUNDLE_SHA256;
  const source = options['git-bundle-source'] || environment.GIT_BUNDLE_SOURCE;
  if (!version || !/^\d+\.\d+\.\d+(?:[.\-+][a-zA-Z0-9.-]+)?$/.test(version)) throw new Error('Git bundle version is missing or invalid.');
  if (!archiveSha256 || !/^[0-9a-f]{64}$/i.test(archiveSha256)) throw new Error('Git bundle archive SHA256 is missing or invalid.');
  let url;
  try { url = new URL(source); } catch { /* Report consistently below. */ }
  if (!url || url.protocol !== 'https:' || url.username || url.password) throw new Error('Git bundle source must be an HTTPS upstream archive URL.');
  const executableRelative = target.platform === 'windows' ? 'cmd/git.exe' : 'bin/git';
  const executable = path.join(root, ...executableRelative.split('/'));
  requiredFile(executable, 'Bundled Git executable');
  const licenses = files.filter(file => file.relative.startsWith('licenses/'));
  const sources = files.filter(file => file.relative.startsWith('sources/'));
  if (!licenses.length) throw new Error('Git bundle must contain original notices and licenses under licenses/.');
  if (!sources.some(file => /\.(?:tar(?:\.(?:gz|xz|bz2|zst))?|tgz|zip)$/i.test(file.relative))) throw new Error('Git bundle must include corresponding source archives under sources/.');
  for (const file of [...licenses, ...sources]) requiredFile(file.file, 'Git license or source asset');
  const binaries = files.map(file => ({ ...file, info: gitBinaryInfo(file.file) })).filter(file => file.info && !file.info.managed);
  const mainBinary = gitBinaryInfo(executable);
  if (!mainBinary || mainBinary.managed) throw new Error('Bundled Git must be a native executable, not a shell shim or managed assembly.');
  if (mainBinary.platform !== target.platform || !mainBinary.architectures.includes(target.architecture)) throw new Error(`Git executable does not match ${target.platform}/${target.architecture}: ${executable}`);
  const officialARM64Git = target.platform === 'windows' && target.architecture === 'arm64' &&
    archiveSha256.toLowerCase() === pinnedWindowsARM64Git.sha256 && url.href === pinnedWindowsARM64Git.url;
  for (const { file, relative, info } of binaries) {
    // MinGit amd64 deliberately carries a 32-bit MSYS probe. It inspects a
    // 32-bit process and is not a library loaded into the 64-bit Git process.
    if (target.platform === 'windows' && (target.architecture === 'amd64' || officialARM64Git) && relative === 'usr/libexec/getprocaddr32.exe' && info.platform === 'windows' && info.architectures.includes('386')) continue;
    // The pinned upstream ARM64 MinGit keeps its MSYS tools as a separate x64
    // compatibility runtime. Only that known distribution and its MSYS paths
    // may differ; cmd/git.exe and every clangarm64 helper remain ARM64-only.
    const msysHelper = /^usr\/bin\/[^/]+\.(?:exe|dll)$/.test(relative) ||
      /^usr\/lib\/ssh\/ssh-(?:keysign|pkcs11-helper|sk-helper)\.exe$/.test(relative) || relative === 'usr/libexec/getprocaddr64.exe';
    if (officialARM64Git && msysHelper && info.platform === 'windows' && info.architectures.includes('amd64')) continue;
    if (info.platform !== target.platform || !info.architectures.includes(target.architecture)) throw new Error(`Git executable or library does not match ${target.platform}/${target.architecture}: ${file}`);
  }
  const native = target.platform === ({ darwin: 'macos', win32: 'windows' })[process.platform] && target.architecture === (process.arch === 'x64' ? 'amd64' : process.arch);
  if (native) verifyNativeGit(root, executable, version);
  return { root, binaries, executableRelative, native, metadata: { version, archiveSha256: archiveSha256.toLowerCase(), source: url.href,
    executable: executableRelative, executableSha256: sha256(executable),
    licenses: licenses.map(file => ({ path: file.relative, sha256: sha256(file.file) })),
    correspondingSources: sources.map(file => ({ path: file.relative, sha256: sha256(file.file) })),
    verification: native ? 'Native git version, init, empty commit and linked worktree verified with isolated settings and bundled PATH.' : 'Executable/library headers checked. Archive provenance supplied by runtime preparation; foreign Git was not executed.' } };
}

export function copyGitBundle(bundle, destination) {
  if (fs.lstatSync(destination, { throwIfNoEntry: false })?.isSymbolicLink()) throw new Error('Git bundle output must not be a symlink.');
  const resolved = canonicalBuildPath(destination);
  if (inside(bundle.root, resolved) || inside(resolved, bundle.root)) throw new Error('Git bundle input and output must be separate.');
  // The destination is an owned build artifact. Replace the entire subtree so
  // upgrading Git cannot retain a helper/library removed by upstream.
  fs.rmSync(resolved, { recursive: true, force: true });
  fs.mkdirSync(path.dirname(resolved), { recursive: true });
  fs.cpSync(bundle.root, resolved, { recursive: true, dereference: false, verbatimSymlinks: true });
  const notice = `Git ${bundle.metadata.version}\n\nUpstream binary archive: ${bundle.metadata.source}\nArchive SHA256: ${bundle.metadata.archiveSha256}\nExecutable SHA256 before distribution signing: ${bundle.metadata.executableSha256}\nVerification: ${bundle.metadata.verification}\n\nOriginal licenses and notices: licenses/\nCorresponding source and build assets: sources/\nThese assets are copied without modification from the prepared distribution.\n`;
  fs.writeFileSync(path.join(resolved, 'ONEBYONE-NOTICE.txt'), notice, 'utf8');
  fs.writeFileSync(path.join(path.dirname(resolved), 'runtime.json'), JSON.stringify({ format: 1, git: bundle.metadata }, null, 2) + '\n', 'utf8');
}

export function signMacGitBundle(root) {
  const tree = inspectGitTree(root);
  for (const { file } of tree.files) {
    const info = gitBinaryInfo(file);
    if (info?.platform !== 'macos') continue;
    execFileSync('/usr/bin/codesign', ['--force', '--sign', '-', file], { stdio: 'pipe' });
    execFileSync('/usr/bin/codesign', ['--verify', '--strict', file], { stdio: 'pipe' });
  }
}
