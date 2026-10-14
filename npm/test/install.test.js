import assert from 'node:assert/strict';
import { createHash } from 'node:crypto';
import { mkdtemp, mkdir, writeFile, readFile, rm, copyFile, readdir, realpath } from 'node:fs/promises';
import { createServer } from 'node:http';
import { once } from 'node:events';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { test } from 'node:test';
import { releaseInfo, expectedChecksum, verifyChecksum } from '../install.js';

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

// createArchive exercises the host extraction format, including duplicate ZIP entries.
async function createArchive(directory, info, entries) {
  const archivePath = path.join(directory, info.assetName);
  await rm(archivePath, { force: true });
  if (process.platform === 'win32') {
    // Windows PowerShell needs the core compression assembly for ZipArchiveMode as well as ZipFile's assembly.
    await execute('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command',
      "$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.IO.Compression, System.IO.Compression.FileSystem; " +
      '$zip=[IO.Compression.ZipFile]::Open($env:FIXTURE_ARCHIVE, [IO.Compression.ZipArchiveMode]::Create); ' +
      'try { foreach ($item in (ConvertFrom-Json $env:FIXTURE_ENTRIES)) { ' +
      '$entry=$zip.CreateEntry($item.name); $writer=New-Object IO.StreamWriter($entry.Open()); ' +
      'try { $writer.Write($item.content) } finally { $writer.Dispose() } } } finally { $zip.Dispose() }'],
    { env: { ...process.env, FIXTURE_ARCHIVE: archivePath, FIXTURE_ENTRIES: JSON.stringify(entries) } });
  } else {
    const fixture = path.join(directory, 'fixture');
    await mkdir(fixture, { recursive: true });
    for (const entry of entries) await writeFile(path.join(fixture, entry.name), entry.content, { mode: 0o755 });
    await execute('tar', ['-czf', archivePath, '-C', fixture, ...entries.map((entry) => entry.name)]);
  }
  return readFile(archivePath);
}

test('npm entry point installs and replaces verified local releases, preserving failed updates', { timeout: 120000 }, async (t) => {
  const directory = await realpath(await mkdtemp(path.join(tmpdir(), 'codex-transfer-npm-test-')));
  t.after(() => rm(directory, { recursive: true, force: true }));
  const root = path.join(directory, 'package');
  const temporary = path.join(directory, 'temporary');
  await mkdir(root);
  await mkdir(temporary);
  await writeFile(path.join(root, 'package.json'), JSON.stringify({ type: 'module', version: '1.2.3' }));
  await copyFile(new URL('../install.js', import.meta.url), path.join(root, 'install.js'));
  const info = releaseInfo('1.2.3');
  let archive;
  let checksum;
  const requests = [];
  const server = createServer((request, response) => {
    requests.push(request.url);
    if (request.url === `/v1.2.3/${info.assetName}`) { response.end(archive); return; }
    if (request.url === '/v1.2.3/checksums.txt') { response.end(`${checksum}  ${info.assetName}\n`); return; }
    response.writeHead(404).end();
  });
  server.listen(0, '127.0.0.1');
  await once(server, 'listening');
  t.after(() => new Promise((resolve, reject) => {
    server.closeAllConnections();
    server.close((error) => error ? reject(error) : resolve());
  }));
  const env = { ...process.env, CODEX_TRANSFER_VERSION: '1.2.3',
    CODEX_TRANSFER_RELEASE_BASE_URL: `http://127.0.0.1:${server.address().port}`,
    TMPDIR: temporary, TMP: temporary, TEMP: temporary };
  for (const name of ['HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy']) delete env[name];
  const binary = path.join(root, 'vendor', info.executableName);
  const first = '#!/bin/sh\nprintf "first fixture\\n"\n';
  const replacement = '#!/bin/sh\nprintf "replacement fixture\\n"\n';
  const cases = [
    { name: 'first install', entries: [{ name: info.executableName, content: first }], expected: first },
    { name: 'replacement', entries: [{ name: info.executableName, content: replacement }], expected: replacement },
    { name: 'corrupt download', entries: [{ name: info.executableName, content: first }], corrupt: true, error: /SHA-256 mismatch/ },
    { name: 'missing executable', entries: [{ name: 'README.txt', content: 'missing executable' }], error: /Archive extraction failed/ },
  ];
  if (process.platform === 'win32') cases.push({ name: 'duplicate executable', entries: [
    { name: info.executableName, content: first }, { name: info.executableName, content: replacement },
  ], error: /Missing or duplicate binary/ });
  for (const scenario of cases) {
    await t.test(scenario.name, async () => {
      archive = await createArchive(directory, info, scenario.entries);
      checksum = createHash('sha256').update(archive).digest('hex');
      if (scenario.corrupt) archive = Buffer.from('tampered');
      requests.length = 0;
      const invocation = execute(process.execPath, [path.join(root, 'install.js')], { env, timeout: 30000 });
      if (scenario.error) await assert.rejects(invocation, scenario.error);
      else assert.match((await invocation).stdout, /Installed codex-transfer 1.2.3/);
      assert.equal(await readFile(binary, 'utf8'), scenario.expected || replacement);
      assert.deepEqual(requests.sort(), ['/v1.2.3/checksums.txt', `/v1.2.3/${info.assetName}`].sort());
      assert.deepEqual(await readdir(temporary), [], 'installer temporary downloads must be removed');
      assert.deepEqual(await readdir(path.join(root, 'vendor')), [info.executableName], 'staging directories must be removed');
    });
  }
  if (process.platform !== 'win32') assert.match((await execute(binary)).stdout, /replacement fixture/);
});
