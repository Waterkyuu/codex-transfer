#!/usr/bin/env node
import { spawn } from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const executable = path.join(path.dirname(fileURLToPath(import.meta.url)), 'vendor',
  process.platform === 'win32' ? 'codex-transfer.exe' : 'codex-transfer');
if (!fs.existsSync(executable)) {
  console.error('Binary missing. Run npm rebuild --global @waterkyuu/codex-transfer to complete installation.');
  process.exit(1);
}
const child = spawn(executable, process.argv.slice(2), { stdio: 'inherit', windowsHide: false });
child.on('error', (error) => { console.error(`Unable to start codex-transfer: ${error.message}`); process.exitCode = 1; });
child.on('exit', (code, signal) => {
  if (signal) { process.kill(process.pid, signal); return; }
  process.exitCode = code ?? 1;
});
