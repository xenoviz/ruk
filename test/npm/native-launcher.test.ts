import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  installerFromEnvironment,
  installNativeLauncher,
  platformTarget,
  windowsCommandDestination,
} from "../../scripts/npm/launcher.mjs";

const version = "0.1.2";

async function fixture() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-"));
  const packageRoot = path.join(root, "node_modules", "@xenoviz", "ruk-linux-x64");
  const native = path.join(packageRoot, "native", "ruk");
  await fs.mkdir(path.dirname(native), { recursive: true });
  await fs.mkdir(path.join(root, "bin"), { recursive: true });
  const contents = Buffer.from("native-ruk-binary");
  await fs.writeFile(native, contents);
  await fs.chmod(native, 0o755);
  const sha256 = crypto.createHash("sha256").update(contents).digest("hex");
  await fs.writeFile(
    path.join(root, "package.json"),
    JSON.stringify({
      name: "@xenoviz/ruk",
      version,
      ruk: { distribution: "package", binaryPath: "bin/ruk" },
    }),
  );
  await fs.writeFile(
    path.join(packageRoot, "package.json"),
    JSON.stringify({
      name: "@xenoviz/ruk-linux-x64",
      version,
      os: ["linux"],
      cpu: ["x64"],
      ruk: {
        distribution: "package",
        target: "bun-linux-x64-baseline",
        binary: "native/ruk",
        sha256,
      },
    }),
  );
  return { root, native, contents };
}

test("platformTarget preserves the seven native package mappings", () => {
  assert.deepEqual(platformTarget("linux", "x64", "glibc"), {
    packageName: "@xenoviz/ruk-linux-x64",
    target: "bun-linux-x64-baseline",
  });
  assert.deepEqual(platformTarget("linux", "x64", "musl"), {
    packageName: "@xenoviz/ruk-linux-x64-musl",
    target: "bun-linux-x64-musl-baseline",
  });
  assert.deepEqual(platformTarget("linux", "arm64", "glibc"), {
    packageName: "@xenoviz/ruk-linux-arm64",
    target: "bun-linux-arm64",
  });
  assert.deepEqual(platformTarget("darwin", "x64"), {
    packageName: "@xenoviz/ruk-darwin-x64",
    target: "bun-darwin-x64",
  });
  assert.deepEqual(platformTarget("darwin", "arm64"), {
    packageName: "@xenoviz/ruk-darwin-arm64",
    target: "bun-darwin-arm64",
  });
  assert.deepEqual(platformTarget("win32", "x64"), {
    packageName: "@xenoviz/ruk-windows-x64",
    target: "bun-windows-x64-baseline",
  });
  assert.deepEqual(platformTarget("win32", "arm64"), {
    packageName: "@xenoviz/ruk-windows-arm64",
    target: "bun-windows-arm64",
  });
  assert.throws(() => platformTarget("linux", "arm64", "musl"), /not available/);
});

test("package template keeps the exact command, marker, and optional package set", async () => {
  const manifest = JSON.parse(await fs.readFile(new URL("../../npm/ruk/package.json", import.meta.url), "utf8")) as {
    bin: Record<string, string>;
    optionalDependencies: Record<string, string>;
    ruk: { distribution: string; binaryPath: string };
  };
  assert.equal(manifest.bin.ruk, "bin/ruk");
  assert.deepEqual(Object.keys(manifest.optionalDependencies).sort(), [
    "@xenoviz/ruk-darwin-arm64",
    "@xenoviz/ruk-darwin-x64",
    "@xenoviz/ruk-linux-arm64",
    "@xenoviz/ruk-linux-x64",
    "@xenoviz/ruk-linux-x64-musl",
    "@xenoviz/ruk-windows-arm64",
    "@xenoviz/ruk-windows-x64",
  ]);
  assert.equal(manifest.ruk.distribution, "package");
  assert.equal(manifest.ruk.binaryPath, "bin/ruk");
});

test("installer validates and atomically places the exact native binary", async (t) => {
  const value = await fixture();
  t.after(() => fs.rm(value.root, { recursive: true, force: true }));

  const result = await installNativeLauncher({ root: value.root, platform: "linux", arch: "x64", libc: "glibc" });
  assert.equal(result.packageName, "@xenoviz/ruk-linux-x64");
  assert.deepEqual(await fs.readFile(path.join(value.root, "bin", "ruk")), value.contents);
  assert.deepEqual(
    JSON.parse(await fs.readFile(`${path.join(value.root, "bin", "ruk")}.ruk-distribution`, "utf8")),
    { schemaVersion: 1, distribution: "package", installer: "npm" },
  );
});

test("installer ownership is derived from the package manager lifecycle", () => {
  assert.equal(installerFromEnvironment({ npm_execpath: "/home/me/.bun/bin/bun" }), "bun");
  assert.equal(installerFromEnvironment({ npm_execpath: "/pnpm/pnpm.cjs" }), "pnpm");
  assert.equal(installerFromEnvironment({ npm_execpath: "/yarn/bin/yarn.js" }), "yarn");
  assert.equal(installerFromEnvironment({ npm_execpath: "/npm/bin/npm-cli.js" }), "npm");
  assert.equal(installerFromEnvironment({}), "npm");
});

test("Windows installation places an executable ahead of npm's Node shim", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-windows-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const packageRoot = path.join(root, "node_modules", "@xenoviz", "ruk-windows-x64");
  const native = path.join(packageRoot, "native", "ruk.exe");
  const contents = Buffer.from("native-windows-ruk-binary");
  await fs.mkdir(path.dirname(native), { recursive: true });
  await fs.mkdir(path.join(root, "bin"), { recursive: true });
  await fs.writeFile(native, contents);
  const sha256 = crypto.createHash("sha256").update(contents).digest("hex");
  await fs.writeFile(path.join(root, "package.json"), JSON.stringify({
    name: "@xenoviz/ruk",
    version,
    ruk: { distribution: "package", binaryPath: "bin/ruk" },
  }));
  await fs.writeFile(path.join(packageRoot, "package.json"), JSON.stringify({
    name: "@xenoviz/ruk-windows-x64",
    version,
    ruk: {
      distribution: "package",
      target: "bun-windows-x64-baseline",
      binary: "native/ruk.exe",
      sha256,
    },
  }));
  const commandDestination = path.join(root, "node_modules", ".bin", "ruk.exe");

  const result = await installNativeLauncher({
    root,
    platform: "win32",
    arch: "x64",
    commandDestination,
    environment: { npm_execpath: "C:\\pnpm\\pnpm.cjs" },
  });

  assert.equal(result.destination, path.join(root, "bin", "ruk.exe"));
  assert.deepEqual(await fs.readFile(result.destination), contents);
  assert.deepEqual(await fs.readFile(commandDestination), contents);
  assert.deepEqual(
    JSON.parse(await fs.readFile(`${commandDestination}.ruk-distribution`, "utf8")),
    { schemaVersion: 1, distribution: "package", installer: "pnpm" },
  );
});

test("Windows command placement follows npm local and global prefixes", () => {
  assert.equal(
    windowsCommandDestination("C:\\project\\node_modules\\@xenoviz\\ruk", {
      npm_config_global: "false",
      npm_config_local_prefix: "C:\\project",
    }),
    path.resolve("C:\\project", "node_modules", ".bin", "ruk.exe"),
  );
  assert.equal(
    windowsCommandDestination("C:\\prefix\\node_modules\\@xenoviz\\ruk", {
      npm_config_global: "true",
      npm_config_prefix: "C:\\prefix",
    }),
    path.resolve("C:\\prefix", "ruk.exe"),
  );
});

test("installer fails before replacing the destination on checksum or path violations", async (t) => {
  const value = await fixture();
  t.after(() => fs.rm(value.root, { recursive: true, force: true }));
  const destination = path.join(value.root, "bin", "ruk");
  await fs.writeFile(destination, "previous-native");
  const packageJSON = path.join(value.root, "node_modules", "@xenoviz", "ruk-linux-x64", "package.json");

  await fs.writeFile(packageJSON, JSON.stringify({
    name: "@xenoviz/ruk-linux-x64",
    version,
    ruk: { distribution: "package", target: "bun-linux-x64-baseline", binary: "native/ruk", sha256: "0".repeat(64) },
  }));
  await assert.rejects(
    installNativeLauncher({ root: value.root, platform: "linux", arch: "x64", libc: "glibc" }),
    /checksum mismatch/,
  );
  assert.equal(await fs.readFile(destination, "utf8"), "previous-native");

  await fs.writeFile(packageJSON, JSON.stringify({
    name: "@xenoviz/ruk-linux-x64",
    version,
    ruk: { distribution: "package", target: "bun-linux-x64-baseline", binary: "../escape", sha256: "0".repeat(64) },
  }));
  await assert.rejects(
    installNativeLauncher({ root: value.root, platform: "linux", arch: "x64", libc: "glibc" }),
    /must stay inside/,
  );
  assert.equal(await fs.readFile(destination, "utf8"), "previous-native");
});

test("installer reports missing optional packages and unsupported hosts clearly", async (t) => {
  const value = await fixture();
  t.after(() => fs.rm(value.root, { recursive: true, force: true }));
  await assert.rejects(
    installNativeLauncher({ root: value.root, platform: "linux", arch: "arm64", libc: "musl" }),
    /not available/,
  );
  await fs.rm(path.join(value.root, "node_modules", "@xenoviz", "ruk-linux-x64"), { recursive: true });
  await assert.rejects(
    installNativeLauncher({ root: value.root, platform: "linux", arch: "x64", libc: "glibc" }),
    /optional native package .* is missing/,
  );
});
