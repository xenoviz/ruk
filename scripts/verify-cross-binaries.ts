import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "../src/process.js";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const targets = [
  "bun-linux-x64-baseline",
  "bun-linux-arm64",
  "bun-linux-x64-musl-baseline",
  "bun-darwin-x64",
  "bun-darwin-arm64",
  "bun-windows-x64-baseline",
  "bun-windows-arm64",
] as const;
const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-cross-binaries-"));

try {
  for (const target of targets) {
    const executable = path.join(temporary, target.includes("windows") ? `${target}.exe` : target);
    await run("bun", [path.join(root, "scripts", "build-binary.ts")], {
      cwd: root,
      env: {
        ...process.env,
        RUK_BINARY_TARGET: target,
        RUK_BINARY_OUTFILE: executable,
      },
      stdio: "inherit",
    });
    const stat = await fs.stat(executable);
    assert.ok(stat.size > 1_000_000, `${target} did not produce a standalone runtime`);
    await fs.rm(executable);
  }
  process.stdout.write(`Verified ${targets.length} cross-compilation targets.\n`);
} finally {
  await fs.rm(temporary, { recursive: true, force: true });
}
