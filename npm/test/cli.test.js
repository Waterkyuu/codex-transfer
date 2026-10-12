import assert from 'node:assert/strict';
import { mkdtemp, copyFile, mkdir, writeFile, rm } from 'node:fs/promises';
import os from 'node:os';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { test } from 'node:test';
import { fileURLToPath } from 'node:url';

const execute = promisify(execFile);

test('wrapper reports missing installation and preserves native arguments and exit status', async (t) => {
  const root = await mkdtemp(path.join(os.tmpdir(), 'codex-transfer-cli-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const cli = path.join(root, 'cli.mjs');
  await copyFile(fileURLToPath(new URL('../cli.js', import.meta.url)), cli);
  await assert.rejects(execute(process.execPath, [cli, '--version']), (error) => error.code === 1 && /Binary missing/.test(error.stderr));
  if (process.platform === 'win32') { return; }
  await mkdir(path.join(root, 'vendor'));
  await writeFile(path.join(root, 'vendor', 'codex-transfer'), '#!/bin/sh\nprintf "%s\\n" "$1"\nexit 7\n', { mode: 0o755 });
  await assert.rejects(execute(process.execPath, [cli, 'argument with spaces']), (error) => error.code === 7 && error.stdout === 'argument with spaces\n');
});
