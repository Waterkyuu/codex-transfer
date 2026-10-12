import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, writeFile, readFile, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { test } from 'node:test';
import { releaseInfo, expectedChecksum, verifyChecksum, install } from '../install.js';

const execute = promisify(execFile);

test('maps all six release targets and rejects unsupported platforms', () => {
  for (const platform of ['darwin', 'linux', 'win32']) {
    for (const arch of ['x64', 'arm64']) {
      const info = releaseInfo('1.2.3', platform, arch);
      assert.match(info.assetName, /codex-transfer_1\.2\.3_/);
      assert.ok(info.assetName.endsWith(platform === 'win32' ? '.zip' : '.tar.gz'));
    }
  }
  assert.throws(() => releaseInfo('1.2.3', 'freebsd', 'x64'), /Unsupported/);
  assert.throws(() => releaseInfo('../invalid'), /Invalid/);
});

test('requires the exact checksum entry and rejects corruption', () => {
  const bytes = Buffer.from('fixture');
  const hash = createHash('sha256').update(bytes).digest('hex');
  assert.equal(expectedChecksum(`${hash}  archive.tar.gz\n`, 'archive.tar.gz'), hash);
  assert.throws(() => expectedChecksum(`${hash}  nested/archive.tar.gz`, 'archive.tar.gz'), /No SHA-256/);
  assert.throws(() => verifyChecksum(Buffer.from('bad'), hash), /SHA-256 mismatch/);
});

test('installs a verified archive and preserves the previous binary on a failed update', async (t) => {
  if (process.platform === 'win32') { t.skip('Windows installation is covered by the PowerShell fixture'); return; }
  const directory = await mkdtemp(path.join(tmpdir(), 'codex-transfer-npm-test-'));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const root = path.join(directory, 'package');
  const fixture = path.join(directory, 'fixture');
  await mkdir(root);
  await mkdir(fixture);
  await writeFile(path.join(root, 'package.json'), JSON.stringify({ version: '1.2.3' }));
  await writeFile(path.join(fixture, 'codex-transfer'), '#!/bin/sh\nprintf "fixture %s\\n" "$1"\n', { mode: 0o755 });
  const archivePath = path.join(directory, 'archive.tar.gz');
  await execute('tar', ['-czf', archivePath, '-C', fixture, 'codex-transfer']);
  const archive = await readFile(archivePath);
  const info = releaseInfo('1.2.3');
  const digest = createHash('sha256').update(archive).digest('hex');
  const fetch = async (url) => new Response(url.endsWith('checksums.txt') ? `${digest}  ${info.assetName}\n` : archive);
  await install({ packageRoot: root, fetch });
  const binary = path.join(root, 'vendor', 'codex-transfer');
  assert.match((await execute(binary, ['help'])).stdout, /fixture help/);
  const corruptFetch = async (url) => new Response(url.endsWith('checksums.txt') ? `${digest}  ${info.assetName}\n` : 'tampered');
  await assert.rejects(install({ packageRoot: root, fetch: corruptFetch }), /SHA-256 mismatch/);
  assert.match((await execute(binary, ['help'])).stdout, /fixture help/);
});
