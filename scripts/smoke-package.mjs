// Exercise a native distribution without the developer's Git, rg or settings.
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import assert from 'node:assert/strict';
import { execFileSync } from 'node:child_process';

const directory = path.resolve(process.argv[2]);
const windows = process.platform === 'win32';
const cli = path.join(directory, windows ? 'onebyone-cli.exe' : 'onebyone');
const temporary = fs.mkdtempSync(path.join(os.tmpdir(), 'onebyone-distribution-'));
try {
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !/^(git_|path$|home$|userprofile$|tmpdir$|tmp$|temp$|onebyone_|azure_openai_|openai_|anthropic_)/i.test(key)));
  Object.assign(env, { HOME: temporary, USERPROFILE: temporary, TMPDIR: temporary, TMP: temporary, TEMP: temporary,
    PATH: windows ? path.join(process.env.SystemRoot || 'C:\\Windows', 'System32') : '',
    ONEBYONE_PRIVATE_DIR: path.join(temporary, 'private') });
  const config = path.join(temporary, 'application', 'settings.json');
  const run = command => JSON.parse(execFileSync(cli, [command, '--config', config, '--json'], { env, encoding: 'utf8', timeout: 120000, windowsHide: true, maxBuffer: 16 * 1024 * 1024 }));
  const demo = run('demo');
  assert.equal(demo.lastError || '', '');
  assert.equal(demo.tasks.length, 23);
  assert.equal(demo.rules.length, 13);
  const restored = run('status');
  assert.equal(restored.tasks.length, 23);
  assert.equal(restored.config.root, demo.config.root);
  process.stdout.write('Native package smoke passed: bundled Git/rg, demo, scan and persisted status; no LLM request.\n');
} finally { fs.rmSync(temporary, { recursive: true, force: true }); }
