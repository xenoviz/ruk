import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { readPackageVersion } from "./lib/package.js";
import { run } from "./lib/process.js";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const version = process.env["RUK_VERSION"] ?? await readPackageVersion(root);
const minimumBinarySize = 1_000_000;
const targets = [
  { name: "bun-linux-x64-baseline", goos: "linux", goarch: "amd64", windows: false },
  { name: "bun-linux-arm64", goos: "linux", goarch: "arm64", windows: false },
  { name: "bun-linux-x64-musl-baseline", goos: "linux", goarch: "amd64", windows: false },
  { name: "bun-darwin-x64", goos: "darwin", goarch: "amd64", windows: false },
  { name: "bun-darwin-arm64", goos: "darwin", goarch: "arm64", windows: false },
  { name: "bun-windows-x64-baseline", goos: "windows", goarch: "amd64", windows: true },
  { name: "bun-windows-arm64", goos: "windows", goarch: "arm64", windows: true },
] as const;
const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-cross-binaries-"));

try {
  for (const target of targets) {
    const executable = path.join(temporary, target.windows ? `${target.name}.exe` : target.name);
    await run("bun", [path.join(root, "scripts", "build-binary.ts")], {
      cwd: root,
      env: {
        ...process.env,
        CGO_ENABLED: "0",
        GOOS: target.goos,
        GOARCH: target.goarch,
        RUK_BINARY_TARGET: target.name,
        RUK_BINARY_OUTFILE: executable,
        RUK_VERSION: version,
        RUK_DISTRIBUTION: "standalone",
      },
      stdio: "inherit",
    });
    const stat = await fs.stat(executable);
    assert.ok(stat.isFile() && stat.size > minimumBinarySize, `${target.name} did not produce a standalone runtime`);
    await fs.rm(executable);
  }
  process.stdout.write(`Verified ${targets.length} cross-compilation targets.\n`);
} finally {
  await fs.rm(temporary, { recursive: true, force: true });
}
