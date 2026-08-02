import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "../src/process.js";
import { isRecord } from "../src/types.js";

interface PackedFile {
  path: string;
}

interface PackResult {
  filename: string;
  files: PackedFile[];
}

function parsePackResult(value: unknown): PackResult {
  if (!Array.isArray(value) || value.length !== 1 || !isRecord(value[0])) {
    throw new Error("npm pack returned an invalid result");
  }
  const result = value[0];
  const files = result["files"];
  if (
    typeof result["filename"] !== "string" ||
    !Array.isArray(files) ||
    !files.every((file) => isRecord(file) && typeof file["path"] === "string")
  ) {
    throw new Error("npm pack returned invalid file metadata");
  }
  return { filename: result["filename"], files: files as PackedFile[] };
}

const root = fileURLToPath(new URL("..", import.meta.url));
const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-pack-"));
const npmEnvironment = {
  ...process.env,
  npm_config_cache: path.join(temporary, "npm-cache"),
};
try {
  const packed = await run(
    "npm",
    ["pack", "--json", "--ignore-scripts", "--pack-destination", temporary],
    { cwd: root, env: npmEnvironment },
  );
  const { filename, files } = parsePackResult(JSON.parse(packed.stdout) as unknown);
  const names = new Set(files.map((file) => file.path));
  for (const required of ["dist/bin/ruk.js", "dist/src/cli.js", "README.md", "LICENSE"]) {
    if (!names.has(required)) throw new Error(`Packed artifact is missing ${required}`);
  }
  for (const forbidden of [
    ".github/workflows/release.yml",
    "config/github/main-ruleset.json",
    "bin/ruk.ts",
    "src/cli.ts",
  ]) {
    if (names.has(forbidden)) throw new Error(`Packed artifact unexpectedly contains ${forbidden}`);
  }
  const installRoot = path.join(temporary, "install");
  await fs.mkdir(installRoot);
  await run("npm", ["init", "-y"], { cwd: installRoot, env: npmEnvironment });
  await run("npm", ["install", "--ignore-scripts", path.join(temporary, filename)], {
    cwd: installRoot,
    env: npmEnvironment,
  });
  const executable = path.join(installRoot, "node_modules", ".bin", process.platform === "win32" ? "ruk.cmd" : "ruk");
  const help = await run(executable, ["--help"], { cwd: installRoot });
  if (!help.stdout.startsWith("Ruk —")) throw new Error("Installed executable failed its help smoke test");
  const installedEntry = path.join(
    installRoot,
    "node_modules",
    "@xenoviz",
    "ruk",
    "dist",
    "bin",
    "ruk.js",
  );
  const bunHelp = await run("bun", [installedEntry, "--help"], { cwd: installRoot });
  if (!bunHelp.stdout.startsWith("Ruk —")) {
    throw new Error("Installed package failed its Bun runtime smoke test");
  }
  process.stdout.write(`Verified ${filename} (${files.length} files).\n`);
} finally {
  await fs.rm(temporary, { recursive: true, force: true });
}
