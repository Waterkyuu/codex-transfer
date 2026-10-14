import assert from 'node:assert/strict';
import { spawn, execFile } from 'node:child_process';
import { once } from 'node:events';
import { copyFile, mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { promisify } from 'node:util';
import { test } from 'node:test';

// launch isolates the wrapper and gives each test ownership of both processes.
async function launch(t, source, mode = 0o755) {
  const root = await mkdtemp(path.join(tmpdir(), 'codex-transfer-cli-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  await mkdir(path.join(root, 'vendor'));
  await writeFile(path.join(root, 'package.json'), '{"type":"module"}');
  await copyFile(new URL('../cli.js', import.meta.url), path.join(root, 'cli.js'));
  await writeFile(path.join(root, 'vendor', 'codex-transfer'), `#!/usr/bin/env node\n${source}`, { mode });
  const wrapper = spawn(process.execPath, [path.join(root, 'cli.js')], { stdio: ['ignore', 'pipe', 'pipe'] });
  const exited = once(wrapper, 'exit');
  let nativePID;
  t.after(() => {
    wrapper.kill('SIGKILL');
    if (nativePID) {
      try { process.kill(nativePID, 'SIGKILL'); } catch (error) { if (error.code !== 'ESRCH') throw error; }
    }
  });
  return { wrapper, exited, rememberPID: (pid) => { nativePID = pid; } };
}

for (const signal of ['SIGTERM', 'SIGINT']) {
  test(`forwards PID-directed ${signal} and waits for native shutdown`, { skip: process.platform === 'win32', timeout: 10000 }, async (t) => {
    const { wrapper, exited, rememberPID } = await launch(t, `
process.on('${signal}', () => { process.stdout.write('received\\n', () => process.exit(23)); });
console.log(process.pid);
setInterval(() => {}, 1000);
`);
    const [ready] = await once(wrapper.stdout, 'data');
    const nativePID = Number(ready.toString().trim());
    rememberPID(nativePID);
    const received = once(wrapper.stdout, 'data');
    wrapper.kill(signal);
    assert.deepEqual(await exited, [23, null]);
    assert.equal((await received)[0].toString(), 'received\n');
    assert.throws(() => process.kill(nativePID, 0), { code: 'ESRCH' });
  });
}

test('mirrors a native exit code', { skip: process.platform === 'win32', timeout: 10000 }, async (t) => {
  const { exited } = await launch(t, 'process.exit(17);');
  assert.deepEqual(await exited, [17, null]);
});

test('mirrors child-originated termination without invoking forwarding handlers again', { skip: process.platform === 'win32', timeout: 10000 }, async (t) => {
  const { exited } = await launch(t, "process.kill(process.pid, 'SIGTERM');");
  assert.deepEqual(await exited, [null, 'SIGTERM']);
});

test('reports a native spawn failure', { skip: process.platform === 'win32', timeout: 10000 }, async (t) => {
  const { wrapper, exited } = await launch(t, '', 0o644);
  let stderr = '';
  wrapper.stderr.on('data', (chunk) => { stderr += chunk; });
  assert.deepEqual(await exited, [1, null]);
  assert.match(stderr, /Unable to start codex-transfer/);
});

const execute = promisify(execFile);

test('wrapper reports missing installation and preserves native arguments and exit status', async (t) => {
  const root = await mkdtemp(path.join(tmpdir(), 'codex-transfer-cli-test-'));
  t.after(() => rm(root, { recursive: true, force: true }));
  const cli = path.join(root, 'cli.mjs');
  await copyFile(new URL('../cli.js', import.meta.url), cli);
  await assert.rejects(execute(process.execPath, [cli, '--version']), (error) => error.code === 1 && /Binary missing/.test(error.stderr));
  if (process.platform === 'win32') { return; }
  await mkdir(path.join(root, 'vendor'));
  await writeFile(path.join(root, 'vendor', 'codex-transfer'), '#!/bin/sh\nprintf "%s\\n" "$1"\nexit 7\n', { mode: 0o755 });
  await assert.rejects(execute(process.execPath, [cli, 'argument with spaces']), (error) => error.code === 7 && error.stdout === 'argument with spaces\n');
});
