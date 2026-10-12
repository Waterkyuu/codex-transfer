#!/usr/bin/env node
import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const packageRoot = path.dirname(fileURLToPath(import.meta.url));
const releaseBase = 'https://github.com/Waterkyuu/codex-transfer/releases/download';

// releaseInfo keeps npm downloads aligned with the GoReleaser archive naming contract.
export function releaseInfo(version, platform = process.platform, architecture = process.arch, baseURL = releaseBase) {
  const normalized = version.replace(/^v/, '');
  if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(normalized)) {
    throw new Error(`Invalid release version: ${version}`);
  }
  const goos = { darwin: 'darwin', linux: 'linux', win32: 'windows' }[platform];
  const goarch = { x64: 'amd64', arm64: 'arm64' }[architecture];
  if (!goos || !goarch) { throw new Error(`Unsupported platform: ${platform}/${architecture}`); }
  const extension = goos === 'windows' ? 'zip' : 'tar.gz';
  const assetName = `codex-transfer_${normalized}_${goos}_${goarch}.${extension}`;
  const tagURL = `${baseURL.replace(/\/$/, '')}/v${normalized}`;
  return {
    assetName,
    assetURL: `${tagURL}/${assetName}`,
    checksumURL: `${tagURL}/checksums.txt`,
    executableName: goos === 'windows' ? 'codex-transfer.exe' : 'codex-transfer',
  };
}

// expectedChecksum requires the exact published filename, never a matching path basename.
export function expectedChecksum(text, assetName) {
  const matches = text.split(/\r?\n/).map((line) => line.trim().match(/^([a-fA-F0-9]{64})\s+\*?(.+)$/))
    .filter((match) => match && match[2] === assetName);
  if (matches.length !== 1) { throw new Error(`No SHA-256 checksum uniquely published for ${assetName}`); }
  return matches[0][1].toLowerCase();
}

// verifyChecksum rejects corrupt archives before any existing installation is changed.
export function verifyChecksum(archive, expected) {
  const actual = createHash('sha256').update(archive).digest('hex');
  if (actual !== expected) { throw new Error('SHA-256 mismatch; previous installation retained'); }
}

// download bounds release fetches and reports inaccessible or unpublished releases explicitly.
async function download(url, fetch) {
  const response = await fetch(url, { signal: AbortSignal.timeout(60_000), redirect: 'follow' });
  if (!response.ok) { throw new Error(`Download failed (${response.status}): ${url}; a reachable published release is required`); }
  return Buffer.from(await response.arrayBuffer());
}

// extract restores only the expected executable from the verified release archive.
function extract(archive, destination, executableName, platform) {
  let command = 'tar';
  let args = ['-xzf', archive, '-C', destination, executableName];
  let env = process.env;
  if (platform === 'win32') {
    command = 'powershell.exe';
    args = ['-NoProfile', '-NonInteractive', '-Command',
      "$ErrorActionPreference='Stop'; Add-Type -AssemblyName System.IO.Compression.FileSystem; " +
      '$zip=[IO.Compression.ZipFile]::OpenRead($env:CODEX_TRANSFER_ARCHIVE); try { ' +
      "$entries=@($zip.Entries | Where-Object { $_.FullName -eq 'codex-transfer.exe' }); " +
      "if ($entries.Count -ne 1) { throw 'Missing or duplicate binary in archive' }; " +
      '[IO.Compression.ZipFileExtensions]::ExtractToFile($entries[0], ' +
      "(Join-Path $env:CODEX_TRANSFER_DESTINATION 'codex-transfer.exe')) } finally { $zip.Dispose() }"];
    env = { ...process.env, CODEX_TRANSFER_ARCHIVE: archive, CODEX_TRANSFER_DESTINATION: destination };
  }
  const result = spawnSync(command, args, { env, encoding: 'utf8' });
  if (result.error || result.status !== 0) { throw new Error(`Archive extraction failed: ${result.error?.message || result.stderr}`); }
}

// install prepares a verified binary before atomically replacing the npm-owned executable.
export async function install(options = {}) {
  const root = options.packageRoot || packageRoot;
  const metadata = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
  const version = options.version || process.env.CODEX_TRANSFER_VERSION || metadata.version;
  const platform = options.platform || process.platform;
  const info = releaseInfo(version, platform, options.architecture || process.arch,
    options.baseURL || process.env.CODEX_TRANSFER_RELEASE_BASE_URL || releaseBase);
  const directory = fs.mkdtempSync(path.join(os.tmpdir(), 'codex-transfer-npm-'));
  let staged = '';
  try {
    const fetch = options.fetch || globalThis.fetch;
    const [checksums, archive] = await Promise.all([
      download(info.checksumURL, fetch), download(info.assetURL, fetch),
    ]);
    verifyChecksum(archive, expectedChecksum(checksums.toString('utf8'), info.assetName));
    const archivePath = path.join(directory, info.assetName);
    const extracted = path.join(directory, 'extracted');
    fs.mkdirSync(extracted);
    fs.writeFileSync(archivePath, archive);
    extract(archivePath, extracted, info.executableName, platform);
    const source = path.join(extracted, info.executableName);
    if (!fs.lstatSync(source, { throwIfNoEntry: false })?.isFile()) {
      throw new Error(`Archive does not contain a regular ${info.executableName}`);
    }
    const vendor = path.join(root, 'vendor');
    fs.mkdirSync(vendor, { recursive: true });
    staged = fs.mkdtempSync(path.join(vendor, '.install-'));
    const stagedBinary = path.join(staged, info.executableName);
    fs.copyFileSync(source, stagedBinary);
    if (platform !== 'win32') { fs.chmodSync(stagedBinary, 0o755); }
    fs.renameSync(stagedBinary, path.join(vendor, info.executableName));
    console.log(`Installed codex-transfer ${version} for ${platform}/${process.arch}.`);
  } finally {
    if (staged) { fs.rmSync(staged, { force: true, recursive: true }); }
    fs.rmSync(directory, { recursive: true, force: true });
  }
}

// runInstaller enables Node's supported environment proxy flag before fetching release assets.
function runInstaller() {
  const proxy = ['HTTPS_PROXY', 'HTTP_PROXY', 'https_proxy', 'http_proxy'].some((name) => process.env[name]);
  const flag = '--use-env-proxy';
  if (proxy && process.allowedNodeEnvironmentFlags.has(flag) && !process.execArgv.includes(flag) && process.env.NODE_USE_ENV_PROXY !== '1') {
    const result = spawnSync(process.execPath, [flag, fileURLToPath(import.meta.url)], { stdio: 'inherit' });
    if (result.error) { throw result.error; }
    process.exitCode = result.status ?? 1;
    return;
  }
  install().catch((error) => { console.error(`Unable to install codex-transfer: ${error.message}`); process.exitCode = 1; });
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) { runInstaller(); }
