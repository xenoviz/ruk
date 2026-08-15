import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { readPackageVersion } from "./lib/package.js";
import { run } from "./lib/process.js";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-binary-"));
const executable = path.join(temporary, process.platform === "win32" ? "ruk.exe" : "ruk");
const expectedVersion = process.env["RUK_VERSION"] ?? await readPackageVersion(root);
const minimumBinarySize = 1_000_000;

try {
  await run("bun", [path.join(root, "scripts", "build-binary.ts")], {
    cwd: root,
    env: {
      ...process.env,
      RUK_BINARY_OUTFILE: executable,
      RUK_VERSION: expectedVersion,
      RUK_DISTRIBUTION: "standalone",
    },
    stdio: "inherit",
  });

  const stat = await fs.stat(executable);
  assert.ok(stat.isFile() && stat.size > minimumBinarySize, `binary is too small: ${stat.size} bytes`);
  if (process.platform !== "win32") assert.ok((stat.mode & 0o111) !== 0, "binary is not executable");

  const versionResult = await run(executable, ["--version"], { cwd: temporary });
  assert.equal(versionResult.stdout, `${expectedVersion}\n`);
  const help = await run(executable, ["--help"], { cwd: temporary });
  assert.match(help.stdout, /^Ruk —/);

  const repository = path.join(temporary, "repository");
  await fs.mkdir(repository);
  await run("git", ["init", "-q"], { cwd: repository });
  const listed = await run(executable, ["list", "--json"], { cwd: repository });
  const worktrees: unknown = JSON.parse(listed.stdout);
  assert.ok(Array.isArray(worktrees));
  assert.equal(worktrees.length, 1);

  process.stdout.write("Verified the native Ruk executable.\n");
} finally {
  await fs.rm(temporary, {
    recursive: true,
    force: true,
    maxRetries: 5,
    retryDelay: 200,
  });
}
