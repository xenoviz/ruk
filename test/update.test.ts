import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { run } from "../src/process.js";
import {
  checksumFromFile,
  compareVersions,
  executableAsset,
  installerCommand,
  installerFromPath,
  updateRuk,
} from "../src/update.js";

const reporter = { write: () => {}, stdio: "ignore" as const };

function releaseFetch(version: string, binary?: Uint8Array, checksum?: string): typeof fetch {
  return async (input) => {
    const url = String(input);
    if (url.endsWith("/releases/latest")) {
      const assets = binary
        ? [
            {
              name: "ruk-linux-x64",
              browser_download_url: `https://github.com/xenoviz/ruk/releases/download/v${version}/ruk-linux-x64`,
            },
            {
              name: "ruk-linux-x64.sha256",
              browser_download_url: `https://github.com/xenoviz/ruk/releases/download/v${version}/ruk-linux-x64.sha256`,
            },
          ]
        : [];
      return Response.json({ tag_name: `v${version}`, assets });
    }
    if (url.endsWith(".sha256")) return new Response(checksum);
    if (url.endsWith("ruk-linux-x64")) return new Response(binary);
    return new Response("not found", { status: 404 });
  };
}

test("version, target, checksum, and installer selection are deterministic", () => {
  assert.equal(compareVersions("1.2.0", "1.1.9"), 1);
  assert.equal(compareVersions("1.2.0", "1.2.0"), 0);
  assert.equal(compareVersions("1.1.9", "1.2.0"), -1);
  assert.equal(compareVersions("1.2.0", "1.2.0-beta.2"), 1);
  assert.equal(compareVersions("1.2.0-beta.2", "1.2.0-beta.11"), -1);
  assert.equal(executableAsset("linux", "x64"), "ruk-linux-x64");
  assert.equal(executableAsset("linux", "x64", true), "ruk-linux-x64-musl");
  assert.equal(executableAsset("darwin", "arm64"), "ruk-macos-arm64");
  assert.equal(executableAsset("win32", "x64"), "ruk-windows-x64.exe");
  assert.throws(() => executableAsset("linux", "arm64", true), /not available/);
  assert.equal(checksumFromFile(`${"a".repeat(64)}  ruk-linux-x64\n`, "ruk-linux-x64"), "a".repeat(64));
  assert.deepEqual(installerCommand("1.2.3", "bun"), {
    command: "bun",
    args: ["add", "--global", "@xenoviz/ruk@1.2.3"],
  });
  assert.deepEqual(installerCommand("1.2.3", "npm"), {
    command: "npm",
    args: ["install", "--global", "@xenoviz/ruk@1.2.3"],
  });
  assert.equal(installerFromPath("/home/me/.bun/install/global/node_modules/@xenoviz/ruk/dist/bin/ruk.js"), "bun");
  assert.equal(installerFromPath("C:\\Users\\me\\AppData\\Local\\pnpm\\global\\5\\node_modules\\.pnpm\\ruk"), "pnpm");
  assert.equal(installerFromPath("/home/me/.config/yarn/global/node_modules/@xenoviz/ruk/dist/bin/ruk.js"), "yarn");
  assert.equal(installerFromPath("/usr/local/lib/node_modules/@xenoviz/ruk/dist/bin/ruk.js"), "npm");
});

test("package updates delegate an exact version to the selected package manager", async () => {
  const calls: Array<{ command: string; args: readonly string[] }> = [];
  const runImpl: typeof run = async (command, args = []) => {
    calls.push({ command, args });
    return { code: 0, signal: null, stdout: "", stderr: "" };
  };
  const result = await updateRuk({
    distribution: "package",
    checkOnly: false,
    reporter,
    fetchImpl: releaseFetch("0.2.0"),
    runImpl,
    installer: "pnpm",
  });
  assert.equal(result.status, "updated");
  assert.equal(result.method, "pnpm");
  assert.deepEqual(calls, [
    { command: "pnpm", args: ["add", "--global", "@xenoviz/ruk@0.2.0"] },
  ]);
});

test("check-only reports an update without invoking an installer", async () => {
  const runImpl: typeof run = async () => {
    throw new Error("installer must not run");
  };
  const result = await updateRuk({
    distribution: "package",
    checkOnly: true,
    reporter,
    fetchImpl: releaseFetch("0.2.0"),
    runImpl,
    installer: "npm",
  });
  assert.equal(result.status, "update-available");
});

test("standalone updates reject assets outside the canonical release path", async () => {
  const fetchImpl: typeof fetch = async (input) => {
    if (String(input).endsWith("/releases/latest")) {
      return Response.json({
        tag_name: "v0.2.0",
        assets: [
          { name: "ruk-linux-x64", browser_download_url: "https://example.com/ruk-linux-x64" },
          { name: "ruk-linux-x64.sha256", browser_download_url: "https://example.com/ruk-linux-x64.sha256" },
        ],
      });
    }
    throw new Error("untrusted asset must not be downloaded");
  };
  await assert.rejects(
    updateRuk({
      distribution: "standalone",
      checkOnly: false,
      reporter,
      fetchImpl,
      platform: "linux",
      architecture: "x64",
      musl: false,
      executable: "/tmp/ruk",
    }),
    /untrusted URL/,
  );
});

test("standalone update verifies and atomically replaces the executable", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX replacement is covered on POSIX runners");
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-update-"));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const executable = path.join(temporary, "ruk");
  await fs.writeFile(executable, "#!/bin/sh\necho 0.1.0\n", { mode: 0o755 });
  const binary = new TextEncoder().encode("#!/bin/sh\necho 0.2.0\n");
  const digest = crypto.createHash("sha256").update(binary).digest("hex");
  const result = await updateRuk({
    distribution: "standalone",
    checkOnly: false,
    reporter,
    fetchImpl: releaseFetch("0.2.0", binary, `${digest}  ruk-linux-x64\n`),
    platform: "linux",
    architecture: "x64",
    musl: false,
    executable,
  });
  assert.equal(result.status, "updated");
  assert.equal((await run(executable, [])).stdout.trim(), "0.2.0");
});

test("checksum failure and failed verification preserve the current executable", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX rollback is covered on POSIX runners");
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-update-rollback-"));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const executable = path.join(temporary, "ruk");
  const original = "#!/bin/sh\necho 0.1.0\n";
  await fs.writeFile(executable, original, { mode: 0o755 });

  const invalid = new TextEncoder().encode("#!/bin/sh\necho 0.2.0\n");
  await assert.rejects(
    updateRuk({
      distribution: "standalone",
      checkOnly: false,
      reporter,
      fetchImpl: releaseFetch("0.2.0", invalid, `${"0".repeat(64)}  ruk-linux-x64\n`),
      platform: "linux",
      architecture: "x64",
      musl: false,
      executable,
    }),
    /Checksum verification failed/,
  );
  assert.equal(await fs.readFile(executable, "utf8"), original);

  const wrongVersion = new TextEncoder().encode("#!/bin/sh\necho 9.9.9\n");
  const digest = crypto.createHash("sha256").update(wrongVersion).digest("hex");
  await assert.rejects(
    updateRuk({
      distribution: "standalone",
      checkOnly: false,
      reporter,
      fetchImpl: releaseFetch("0.2.0", wrongVersion, `${digest}  ruk-linux-x64\n`),
      platform: "linux",
      architecture: "x64",
      musl: false,
      executable,
    }),
    /failed its version check/,
  );
  assert.equal(await fs.readFile(executable, "utf8"), original);
});
