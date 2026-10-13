import { appendFileSync, existsSync, readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { spawnSync } from "node:child_process";

const installRoot = process.argv[2];
if (!installRoot || !path.isAbsolute(installRoot)) {
  throw new Error("Supply the absolute isolated Codex installation directory.");
}

const packageName = `codex-${process.platform}-${process.arch}`;
const vendor = path.join(installRoot, "node_modules", "@openai", packageName, "vendor");
const packages = readdirSync(vendor, { withFileTypes: true }).filter((entry) =>
  entry.isDirectory() && existsSync(path.join(vendor, entry.name, "codex-package.json")),
);
if (packages.length !== 1) {
  throw new Error(`Expected one native package for ${process.platform}/${process.arch}.`);
}

const packageRoot = path.join(vendor, packages[0].name);
const metadata = JSON.parse(readFileSync(path.join(packageRoot, "codex-package.json"), "utf8"));
const binary = path.join(packageRoot, metadata.entrypoint);
if (!existsSync(binary)) {
  throw new Error("The installed native Codex entry point is missing.");
}
const version = spawnSync(binary, ["--version"], { encoding: "utf8", timeout: 10000 });
if (version.status !== 0) {
  throw new Error(`Native Codex did not start: ${version.stderr}`);
}
process.stdout.write(version.stdout);
process.stdout.write(`${binary}\n`);
if (process.env.GITHUB_ENV) {
  appendFileSync(process.env.GITHUB_ENV, `CODEX_TRANSFER_CODEX_BINARY=${binary}\n`);
}
