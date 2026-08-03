import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { run } from "../src/process.js";
import { checksumFromFile } from "../scripts/release-manifest.js";
import { compareVersions, RELEASE_ASSET_NAMES } from "../src/release.js";
import {
  executableAsset,
  installerCommand,
  installerFromPath,
  parseUpdateInstaller,
  updateRuk,
  windowsReplacementPlan,
} from "../src/update.js";

const reporter = { write: () => {}, stdio: "ignore" as const };

function releaseFetch(
  version: string,
  binary?: Uint8Array,
  assetName = "ruk-linux-x64",
  assetUrl?: string,
  manifestDigest?: string,
): typeof fetch {
  const digest = manifestDigest ?? (binary
    ? crypto.createHash("sha256").update(binary).digest("hex")
    : "0".repeat(64));
  const manifest = {
    schemaVersion: 1,
    repository: "xenoviz/ruk",
    version,
    package: { name: "@xenoviz/ruk", version },
    assets: Object.fromEntries(
      RELEASE_ASSET_NAMES.map((name) => [
        name,
        { sha256: name === assetName ? digest : "0".repeat(64), size: name === assetName && binary ? binary.byteLength : 1 },
      ]),
    ),
  };
  const assets = [
    ...RELEASE_ASSET_NAMES.flatMap((name) => [
      {
        name,
        browser_download_url: name === assetName && assetUrl
          ? assetUrl
          : `https://github.com/xenoviz/ruk/releases/download/v${version}/${name}`,
      },
      {
        name: `${name}.sha256`,
        browser_download_url: `https://github.com/xenoviz/ruk/releases/download/v${version}/${name}.sha256`,
      },
    ]),
    {
      name: "ruk-release.json",
      browser_download_url: `https://github.com/xenoviz/ruk/releases/download/v${version}/ruk-release.json`,
    },
  ];
  return async (input) => {
    const url = String(input);
    if (url.endsWith("/releases?per_page=10")) {
      return Response.json([{ tag_name: `v${version}`, draft: false, prerelease: false, assets }]);
    }
    if (url.endsWith("ruk-release.json")) return Response.json(manifest);
    if (url.endsWith(assetName)) return new Response(binary);
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
  assert.equal(parseUpdateInstaller("bun"), "bun");
  assert.throws(() => parseUpdateInstaller("corepack"), /Unsupported update installer/);
  const plan = windowsReplacementPlan("C:\\Program Files\\Ruk\\ruk.exe", "C:\\Program Files\\Ruk\\ruk.new", "1.2.3", 42);
  assert.match(plan.script, /PID eq 42/);
  assert.match(plan.script, /ruk\.exe" --version \| findstr \/X "1\.2\.3"/);
  assert.match(plan.script, /:rollback/);
  assert.equal(plan.helper, "C:\\Program Files\\Ruk\\ruk.new.cmd");
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
    entrypoint: "/home/me/.bun/install/global/node_modules/@xenoviz/ruk/dist/bin/ruk.js",
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

test("update discovery ignores a newer release until its readiness manifest exists", async () => {
  const ready = releaseFetch("0.2.0");
  const fetchImpl: typeof fetch = async (input, init) => {
    if (String(input).endsWith("/releases?per_page=10")) {
      const response = await ready(input, init);
      const releases: unknown = await response.json();
      assert.ok(Array.isArray(releases));
      return Response.json([
        { tag_name: "v0.3.0", draft: false, prerelease: false, assets: [] },
        ...releases,
      ]);
    }
    return ready(input, init);
  };
  const result = await updateRuk({
    distribution: "package",
    checkOnly: true,
    reporter,
    fetchImpl,
    installer: "npm",
  });
  assert.equal(result.latestVersion, "0.2.0");
});

test("update discovery falls back when a newer release has an invalid manifest", async () => {
  const older = releaseFetch("0.2.0");
  const newer = releaseFetch("0.3.0");
  const newerResponse = await newer("https://api.github.com/repos/xenoviz/ruk/releases?per_page=10");
  const newerReleases: unknown = await newerResponse.json();
  assert.ok(Array.isArray(newerReleases));
  const olderResponse = await older("https://api.github.com/repos/xenoviz/ruk/releases?per_page=10");
  const olderReleases: unknown = await olderResponse.json();
  assert.ok(Array.isArray(olderReleases));

  const fetchImpl: typeof fetch = async (input, init) => {
    const url = String(input);
    if (url.endsWith("/releases?per_page=10")) {
      return Response.json([...newerReleases, ...olderReleases]);
    }
    if (url.includes("/v0.3.0/ruk-release.json")) {
      return Response.json({ schemaVersion: 999 });
    }
    return older(input, init);
  };

  const result = await updateRuk({
    distribution: "package",
    checkOnly: true,
    reporter,
    fetchImpl,
    installer: "npm",
  });
  assert.equal(result.latestVersion, "0.2.0");
});

test("update discovery fails clearly when no completed release exists", async () => {
  const fetchImpl: typeof fetch = async () => Response.json([
    { tag_name: "v0.2.0", draft: false, prerelease: false, assets: [] },
  ]);
  await assert.rejects(
    updateRuk({
      distribution: "package",
      checkOnly: true,
      reporter,
      fetchImpl,
      installer: "npm",
    }),
    /No completed Ruk release is available yet/,
  );
});

test("standalone updates reject assets outside the canonical release path", async () => {
  const binary = new TextEncoder().encode("untrusted");
  await assert.rejects(
    updateRuk({
      distribution: "standalone",
      checkOnly: false,
      reporter,
      fetchImpl: releaseFetch("0.2.0", binary, "ruk-linux-x64", "https://example.com/ruk-linux-x64"),
      platform: "linux",
      architecture: "x64",
      musl: false,
      executable: "/tmp/ruk",
    }),
    /untrusted URL/,
  );
});

test("Windows standalone updates defer replacement until process exit", async (t) => {
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-update-windows-"));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const executable = path.join(temporary, "ruk.exe");
  await fs.writeFile(executable, "current");
  const binary = new TextEncoder().encode("replacement");
  const scheduled: Array<{ executable: string; candidate: string; version: string }> = [];
  const result = await updateRuk({
    distribution: "standalone",
    checkOnly: false,
    reporter,
    fetchImpl: releaseFetch("0.2.0", binary, "ruk-windows-x64.exe"),
    platform: "win32",
    architecture: "x64",
    executable,
    scheduleWindowsImpl: async (target, candidate, version) => {
      scheduled.push({ executable: target, candidate, version });
      assert.deepEqual(new Uint8Array(await fs.readFile(candidate)), binary);
    },
  });
  assert.equal(result.status, "scheduled");
  assert.equal(scheduled.length, 1);
  assert.equal(scheduled[0]?.executable, executable);
  assert.equal(scheduled[0]?.version, "0.2.0");
});

test("standalone update verifies and atomically replaces the executable", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX replacement is covered on POSIX runners");
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-update-"));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const executable = path.join(temporary, "ruk");
  await fs.writeFile(executable, "#!/bin/sh\necho 0.1.0\n", { mode: 0o755 });
  const binary = new TextEncoder().encode("#!/bin/sh\necho 0.2.0\n");
  const result = await updateRuk({
    distribution: "standalone",
    checkOnly: false,
    reporter,
    fetchImpl: releaseFetch("0.2.0", binary),
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
      fetchImpl: releaseFetch("0.2.0", invalid, "ruk-linux-x64", undefined, "0".repeat(64)),
      platform: "linux",
      architecture: "x64",
      musl: false,
      executable,
    }),
    /Checksum verification failed/,
  );
  assert.equal(await fs.readFile(executable, "utf8"), original);

  const wrongVersion = new TextEncoder().encode("#!/bin/sh\necho 9.9.9\n");
  await assert.rejects(
    updateRuk({
      distribution: "standalone",
      checkOnly: false,
      reporter,
      fetchImpl: releaseFetch("0.2.0", wrongVersion),
      platform: "linux",
      architecture: "x64",
      musl: false,
      executable,
    }),
    /failed its version check/,
  );
  assert.equal(await fs.readFile(executable, "utf8"), original);
});
