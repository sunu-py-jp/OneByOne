// Download only pinned build-time inputs. Nothing is fetched by the shipped app.
import fs from 'node:fs';
import path from 'node:path';
import { createHash, randomUUID } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';

const repository = fileURLToPath(new URL('..', import.meta.url));
export const runtimeLock = JSON.parse(fs.readFileSync(new URL('../build/runtime-lock.json', import.meta.url), 'utf8'));
export function sha256(file) { return createHash('sha256').update(fs.readFileSync(file)).digest('hex'); }

function archiveEnvironment() {
  // macOS BSD tar does not recognize the C.UTF-8 locale used by some shells.
  return process.platform === 'darwin' ? { ...process.env, LANG: 'en_US.UTF-8', LC_ALL: 'en_US.UTF-8' } : process.env;
}

export async function downloadPinned(asset, cache, fetcher = fetch) {
  if (!/^https:\/\//.test(asset.url) || !/^[a-f0-9]{64}$/.test(asset.sha256)) throw new Error('Runtime input requires an HTTPS URL and a pinned SHA256.');
  fs.mkdirSync(cache, { recursive: true });
  const destination = path.join(cache, asset.sha256);
  if (fs.existsSync(destination)) {
    if (sha256(destination) !== asset.sha256) throw new Error(`Cached runtime SHA256 mismatch: ${destination}`);
    return destination;
  }
  process.stdout.write(`Downloading runtime: ${asset.url}\n`);
  const response = await fetcher(asset.url, { signal: AbortSignal.timeout(300000) });
  if (!response.ok || !response.body) throw new Error(`Runtime download failed (${response.status}): ${asset.url}`);
  const temporary = `${destination}.${randomUUID()}.partial`;
  const hash = createHash('sha256');
  const handle = fs.openSync(temporary, 'wx', 0o600);
  let bytes = 0;
  try {
    for await (const chunk of response.body) {
      bytes += chunk.length;
      if (bytes > 1024 * 1024 * 1024) throw new Error('Runtime archive exceeds 1 GiB.');
      hash.update(chunk);
      let offset = 0;
      while (offset < chunk.length) offset += fs.writeSync(handle, chunk, offset);
    }
    if (hash.digest('hex') !== asset.sha256) throw new Error(`Runtime download SHA256 mismatch: ${asset.url}`);
  } catch (error) {
    fs.closeSync(handle);
    fs.rmSync(temporary, { force: true });
    throw error;
  }
  fs.closeSync(handle);
  // Concurrent builds may have fetched the same immutable input.
  if (fs.existsSync(destination)) fs.rmSync(temporary, { force: true });
  else fs.renameSync(temporary, destination);
  return destination;
}

export function validateArchivePaths(listing) {
  for (const entry of listing.split(/\r?\n/).filter(Boolean)) {
    const name = entry.replaceAll('\\', '/');
    if (name.startsWith('/') || /^[a-z]:/i.test(name) || name.split('/').includes('..')) throw new Error(`Unsafe runtime archive path: ${entry}`);
  }
}

function unpack(archive, destination) {
  const options = { encoding: 'utf8', windowsHide: true, maxBuffer: 16 * 1024 * 1024, env: archiveEnvironment() };
  validateArchivePaths(execFileSync('tar', ['-tf', archive], options));
  fs.mkdirSync(destination, { recursive: true });
  execFileSync('tar', ['-xf', archive, '-C', destination], { ...options, stdio: 'pipe' });
}

export function installRuntimeAsset(asset, downloaded, directory) {
  if (!asset.name || path.basename(asset.name) !== asset.name) throw new Error('Invalid runtime source filename.');
  const destination = path.join(directory, asset.name);
  if (asset.archiveMember === undefined) {
    fs.copyFileSync(downloaded, destination);
    return;
  }
  if (typeof asset.archiveMember !== 'string' || !asset.archiveMember || /[\r\n\0]/.test(asset.archiveMember)) throw new Error('Invalid runtime notice archive member.');
  validateArchivePaths(asset.archiveMember);
  // Some upstream notices are published only inside the official package.
  // Stream just that pinned member; never unpack the package into the bundle.
  const content = execFileSync('tar', ['-xOf', downloaded, asset.archiveMember], { windowsHide: true, timeout: 60000, maxBuffer: 16 * 1024 * 1024, env: archiveEnvironment() });
  if (!content.length) throw new Error(`Empty runtime notice: ${asset.archiveMember}`);
  fs.writeFileSync(destination, content);
}

export function treeDigest(root) {
  root = fs.realpathSync(root);
  const hash = createHash('sha256');
  const visit = (directory, relative = '') => {
    for (const name of fs.readdirSync(directory).sort()) {
      if (!relative && name === '.prepared.json') continue;
      const file = path.join(directory, name), key = `${relative}/${name}`;
      const stat = fs.lstatSync(file);
      hash.update(`${key}\0${stat.mode & 0o777}\0`);
      if (stat.isSymbolicLink()) {
        const target = fs.readlinkSync(file);
        const resolved = fs.realpathSync(file), rel = path.relative(root, resolved);
        if (path.isAbsolute(target) || rel === '..' || rel.startsWith(`..${path.sep}`) || path.isAbsolute(rel)) throw new Error(`Runtime symlink escapes its bundle: ${key}`);
        hash.update(`link\0${target}\0`);
      } else if (stat.isDirectory()) visit(file, key);
      else if (stat.isFile()) hash.update(fs.readFileSync(file));
      else throw new Error(`Unsupported runtime entry: ${key}`);
    }
  };
  visit(root);
  return hash.digest('hex');
}

async function prepareBundle(kind, specification, target, cache) {
  const archive = await downloadPinned(specification, path.join(cache, 'downloads'));
  const identity = createHash('sha256').update(JSON.stringify(specification)).digest('hex');
  const directory = path.join(cache, 'prepared', target.replace('/', '-'), `${kind}-${identity.slice(0, 16)}`);
  const stamp = path.join(directory, '.prepared.json');
  if (fs.existsSync(stamp)) {
    const previous = JSON.parse(fs.readFileSync(stamp, 'utf8'));
    if (previous.identity !== identity || previous.digest !== treeDigest(directory)) throw new Error(`Prepared runtime changed; remove this generated cache and retry: ${directory}`);
    return specification.subdir ? path.join(directory, specification.subdir) : directory;
  }
  fs.mkdirSync(path.dirname(directory), { recursive: true });
  const staging = fs.mkdtempSync(`${directory}.staging-`);
  try {
    unpack(archive, staging);
    if (kind === 'git') {
      for (const category of ['sources', 'licenses']) {
        fs.mkdirSync(path.join(staging, category), { recursive: true });
        for (const asset of specification[category] || []) {
          const downloaded = await downloadPinned(asset, path.join(cache, 'downloads'));
          installRuntimeAsset(asset, downloaded, path.join(staging, category));
        }
      }
      if (fs.existsSync(path.join(staging, 'LICENSE.txt'))) fs.copyFileSync(path.join(staging, 'LICENSE.txt'), path.join(staging, 'licenses', 'LICENSE.txt'));
      const notice = path.join(staging, 'libexec', 'git-core', 'NOTICE');
      if (fs.existsSync(notice)) fs.copyFileSync(notice, path.join(staging, 'licenses', 'git-credential-manager-NOTICE.txt'));
      fs.writeFileSync(path.join(staging, 'sources', 'manifest.json'), JSON.stringify(specification, null, 2) + '\n');
    }
    fs.writeFileSync(path.join(staging, '.prepared.json'), JSON.stringify({ identity, digest: treeDigest(staging) }));
    if (fs.existsSync(directory)) throw new Error(`Runtime preparation already exists: ${directory}`);
    fs.renameSync(staging, directory);
  } finally {
    fs.rmSync(staging, { recursive: true, force: true });
  }
  return specification.subdir ? path.join(directory, specification.subdir) : directory;
}

export async function prepareRuntime({ platform, arch, env = process.env, installer = false }) {
  const target = `${platform === 'darwin' ? 'macos' : platform}/${arch}`;
  const specification = runtimeLock.targets[target];
  if (!specification) throw new Error(`No pinned runtime for ${target}.`);
  const result = { ...env };
  const cache = path.resolve(env.ONEBYONE_RUNTIME_CACHE || path.join(repository, '.cache', 'runtimes'));
  if (!result.RG_BINARY) {
    const directory = await prepareBundle('rg', specification.rg, target, cache);
    result.RG_BINARY = path.join(directory, target.startsWith('windows') ? 'rg.exe' : 'rg');
    result.RG_LICENSE_DIR = directory;
    result.RG_VERSION = specification.rg.version;
    result.RG_SHA256 = sha256(result.RG_BINARY);
  }
  if (!result.GIT_BUNDLE_DIR) {
    result.GIT_BUNDLE_DIR = await prepareBundle('git', specification.git, target, cache);
    result.GIT_BUNDLE_VERSION = specification.git.version;
    result.GIT_BUNDLE_SHA256 = specification.git.sha256;
    result.GIT_BUNDLE_SOURCE = specification.git.url;
  }
  if (installer && target.startsWith('windows') && !result.WEBVIEW2_INSTALLER) {
    if (!specification.webview2) throw new Error(`No pinned WebView2 installer for ${target}.`);
    const downloaded = await downloadPinned(specification.webview2, path.join(cache, 'downloads'));
    // Keep the .exe suffix for Windows Authenticode/SIP detection. The cached
    // hash-named blob itself is never executed or installed by this script.
    result.WEBVIEW2_INSTALLER = path.join(cache, 'installers', specification.webview2.sha256, path.basename(new URL(specification.webview2.url).pathname));
    fs.mkdirSync(path.dirname(result.WEBVIEW2_INSTALLER), { recursive: true });
    if (!fs.existsSync(result.WEBVIEW2_INSTALLER)) fs.copyFileSync(downloaded, result.WEBVIEW2_INSTALLER);
    if (sha256(result.WEBVIEW2_INSTALLER) !== specification.webview2.sha256) throw new Error('Prepared WebView2 installer SHA256 mismatch.');
    result.WEBVIEW2_SHA256 = specification.webview2.sha256;
  }
  return result;
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const platform = process.argv[2] || ({ darwin: 'macos', win32: 'windows' })[process.platform];
  const arch = process.argv[3] || (process.arch === 'x64' ? 'amd64' : process.arch);
  try {
    const env = await prepareRuntime({ platform, arch, installer: platform === 'windows' });
    for (const key of ['RG_BINARY', 'RG_LICENSE_DIR', 'GIT_BUNDLE_DIR', 'WEBVIEW2_INSTALLER']) if (env[key]) process.stdout.write(`${key}=${env[key]}\n`);
  } catch (error) { process.stderr.write(`${error.message}\n`); process.exitCode = 1; }
}
