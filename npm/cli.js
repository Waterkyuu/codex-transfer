#!/usr/bin/env node
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const executable = path.join(path.dirname(fileURLToPath(import.meta.url)), 'vendor',
  process.platform === 'win32' ? 'codex-transfer.exe' : 'codex-transfer');
if (!fs.existsSync(executable)) {
  console.error('Binary missing. Run npm rebuild --global codex-transfer to complete installation.');
  process.exit(1);
}
const child = spawn(executable, process.argv.slice(2), { stdio: 'inherit', windowsHide: false });
// PID-directed shutdown must reach the native process before the wrapper exits.
// Windows retains Node's existing launcher behavior because POSIX signals differ there.
const signals = process.platform === 'win32' ? [] : ['SIGINT', 'SIGTERM'];
const handlers = new Map(signals.map((signal) => [signal, () => child.kill(signal)]));
for (const [signal, handler] of handlers) process.on(signal, handler);

// Release signal ownership before mirroring a child signal to avoid recursion.
function removeSignalHandlers() {
  for (const [signal, handler] of handlers) process.removeListener(signal, handler);
}
child.on('error', (error) => {
  removeSignalHandlers();
  console.error(`Unable to start codex-transfer: ${error.message}`);
  process.exitCode = 1;
});
child.on('exit', (code, signal) => {
  removeSignalHandlers();
  if (signal) { process.kill(process.pid, signal); return; }
  process.exitCode = code ?? 1;
});
