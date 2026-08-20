import assert from "node:assert/strict";
import { spawnSync } from "node:child_process";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  installerFromEnvironment,
  installNativeLauncher,
  ensureNativeLauncher,
  platformTarget,
  replaceWindowsOutputs,
  runPackageCommand,
  windowsCommandDestination,
  windowsUpdateProcessID,
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
        target: "linux-x64",
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
    target: "linux-x64",
  });
  assert.deepEqual(platformTarget("linux", "x64", "musl"), {
    packageName: "@xenoviz/ruk-linux-x64-musl",
    target: "linux-x64-musl",
  });
  assert.deepEqual(platformTarget("linux", "arm64", "glibc"), {
    packageName: "@xenoviz/ruk-linux-arm64",
    target: "linux-arm64",
  });
  assert.deepEqual(platformTarget("darwin", "x64"), {
    packageName: "@xenoviz/ruk-darwin-x64",
    target: "darwin-x64",
  });
  assert.deepEqual(platformTarget("darwin", "arm64"), {
    packageName: "@xenoviz/ruk-darwin-arm64",
    target: "darwin-arm64",
  });
  assert.deepEqual(platformTarget("win32", "x64"), {
    packageName: "@xenoviz/ruk-windows-x64",
    target: "windows-x64",
  });
  assert.deepEqual(platformTarget("win32", "arm64"), {
    packageName: "@xenoviz/ruk-windows-arm64",
    target: "windows-arm64",
  });
  assert.throws(() => platformTarget("linux", "arm64", "musl"), /not available/);
});

test("package template keeps the exact command, marker, and optional package set", async () => {
  const manifest = JSON.parse(await fs.readFile(new URL("../../npm/ruk/package.json", import.meta.url), "utf8")) as {
    bin: Record<string, string>;
    optionalDependencies: Record<string, string>;
    ruk: { distribution: string; binaryPath: string };
  };
  assert.equal(manifest.bin["ruk"], "bin/ruk");
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

test("installer resolves a native package hoisted beside the root package", async (t) => {
  const consumer = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-hoisted-"));
  t.after(() => fs.rm(consumer, { recursive: true, force: true }));
  const root = path.join(consumer, "node_modules", "@xenoviz", "ruk");
  const packageRoot = path.join(consumer, "node_modules", "@xenoviz", "ruk-linux-x64");
  const native = path.join(packageRoot, "native", "ruk");
  const contents = Buffer.from("hoisted-native-ruk-binary");
  await fs.mkdir(path.join(root, "bin"), { recursive: true });
  await fs.mkdir(path.dirname(native), { recursive: true });
  await fs.writeFile(native, contents);
  await fs.chmod(native, 0o755);
  await fs.writeFile(path.join(root, "package.json"), JSON.stringify({
    name: "@xenoviz/ruk",
    version,
    ruk: { distribution: "package", binaryPath: "bin/ruk" },
  }));
  await fs.writeFile(path.join(packageRoot, "package.json"), JSON.stringify({
    name: "@xenoviz/ruk-linux-x64",
    version,
    ruk: {
      distribution: "package",
      target: "linux-x64",
      binary: "native/ruk",
      sha256: crypto.createHash("sha256").update(contents).digest("hex"),
    },
  }));

  const result = await installNativeLauncher({ root, platform: "linux", arch: "x64", libc: "glibc" });

  assert.equal(result.packageName, "@xenoviz/ruk-linux-x64");
  assert.deepEqual(await fs.readFile(path.join(root, "bin", "ruk")), contents);
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
      target: "windows-x64",
      binary: "native/ruk.exe",
      sha256,
    },
  }));
  const commandDestination = path.join(root, "node_modules", ".bin", "ruk.exe");
  await fs.writeFile(path.join(root, "bin", "ruk.exe"), Buffer.from("previous-native-ruk-binary"));
  await fs.mkdir(path.dirname(commandDestination), { recursive: true });
  await fs.writeFile(commandDestination, Buffer.from("previous-command-ruk-binary"));

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
  assert.equal(result.cleanupPending, false);
  const files = await fs.readdir(root, { recursive: true });
  assert.deepEqual(files.filter((file) => file.endsWith(".ruk-backup")), []);
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

test("Windows direct replacement rejects case-insensitive duplicate destinations before staging", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-duplicate-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const destination = path.join(root, "bin", "ruk.exe");

  await assert.rejects(
    replaceWindowsOutputs([
      { contents: "first", destination },
      { contents: "second", destination: destination.toUpperCase() },
    ]),
    /duplicate destination/,
  );
  await assert.rejects(fs.access(destination));
  const files = await fs.readdir(root, { recursive: true });
  assert.deepEqual(files.filter((file) => file.endsWith(".ruk-pending") || file.endsWith(".ruk-backup")), []);
});

test("Windows direct replacement requires exactly one source or contents", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-output-validation-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const destination = path.join(root, "bin", "ruk.exe");
  const invoke = replaceWindowsOutputs as unknown as (outputs: unknown[]) => Promise<unknown>;

  await assert.rejects(
    invoke([{ source: path.join(root, "source.exe"), contents: "inline", destination }]),
    /must have exactly one source or contents/,
  );
  await assert.rejects(
    invoke([{ destination }]),
    /must have exactly one source or contents/,
  );
  const files = await fs.readdir(root, { recursive: true });
  assert.deepEqual(files.filter((file) => file.endsWith(".ruk-pending") || file.endsWith(".ruk-backup")), []);
});

test("Windows direct replacement rolls back every output when a later install fails", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-rollback-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const source = path.join(root, "source.exe");
  const destination = path.join(root, "bin", "ruk.exe");
  const commandDestination = path.join(root, "node_modules", ".bin", "ruk.exe");
  const marker = `${destination}.ruk-distribution`;
  await fs.mkdir(path.dirname(destination), { recursive: true });
  await fs.mkdir(path.dirname(commandDestination), { recursive: true });
  await fs.writeFile(source, "new-binary");
  await fs.writeFile(destination, "old-primary");
  await fs.writeFile(commandDestination, "old-command");
  await fs.writeFile(marker, "old-marker");

  const fileSystem = {
    mkdir: fs.mkdir,
    copyFile: (...args: Parameters<typeof fs.copyFile>) => fs.copyFile(...args),
    writeFile: (...args: Parameters<typeof fs.writeFile>) => fs.writeFile(...args),
    chmod: (...args: Parameters<typeof fs.chmod>) => fs.chmod(...args),
    rm: (...args: Parameters<typeof fs.rm>) => fs.rm(...args),
    rename: async (...args: Parameters<typeof fs.rename>) => {
      const [from, to] = args;
      if (String(to) === marker && String(from).endsWith(".ruk-pending")) {
        throw new Error("injected marker replacement failure");
      }
      await fs.rename(...args);
    },
  };

  await assert.rejects(
    replaceWindowsOutputs([
      { source, destination },
      { source, destination: commandDestination },
      { contents: "new-marker", destination: marker },
    ], fileSystem),
    /injected marker replacement failure/,
  );
  assert.equal(await fs.readFile(destination, "utf8"), "old-primary");
  assert.equal(await fs.readFile(commandDestination, "utf8"), "old-command");
  assert.equal(await fs.readFile(marker, "utf8"), "old-marker");
  const files = await fs.readdir(root, { recursive: true });
  assert.deepEqual(files.filter((file) => file.endsWith(".ruk-pending") || file.endsWith(".ruk-backup")), []);
});

test("Windows install reports committed backup cleanup failures", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-cleanup-pending-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const packageRoot = path.join(root, "node_modules", "@xenoviz", "ruk-windows-x64");
  const native = path.join(packageRoot, "native", "ruk.exe");
  const contents = Buffer.from("cleanup-pending-windows-ruk-binary");
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
    ruk: { distribution: "package", target: "windows-x64", binary: "native/ruk.exe", sha256 },
  }));
  const destination = path.join(root, "bin", "ruk.exe");
  await fs.writeFile(destination, "previous-native-ruk-binary");

  const fileSystem = {
    mkdir: fs.mkdir,
    copyFile: (...args: Parameters<typeof fs.copyFile>) => fs.copyFile(...args),
    writeFile: (...args: Parameters<typeof fs.writeFile>) => fs.writeFile(...args),
    chmod: (...args: Parameters<typeof fs.chmod>) => fs.chmod(...args),
    rename: (...args: Parameters<typeof fs.rename>) => fs.rename(...args),
    rm: async (target: Parameters<typeof fs.rm>[0], options: Parameters<typeof fs.rm>[1]) => {
      if (String(target).endsWith(".ruk-backup")) throw new Error("injected backup cleanup failure");
      return fs.rm(target, options);
    },
  };

  const result = await installNativeLauncher({
    root,
    platform: "win32",
    arch: "x64",
    commandDestination: path.join(root, "node_modules", ".bin", "ruk.exe"),
    fileSystem,
  });

  assert.equal(result.cleanupPending, true);
  assert.deepEqual(await fs.readFile(destination), contents);
  const files = await fs.readdir(root, { recursive: true });
  assert.equal(files.filter((file) => file.endsWith(".ruk-backup")).length, 1);
  assert.deepEqual(files.filter((file) => file.endsWith(".ruk-pending")), []);
});

test("Windows committed replacement reports pending temporary cleanup failures", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-temp-cleanup-pending-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const destination = path.join(root, "bin", "ruk.exe");
  const fileSystem = {
    mkdir: fs.mkdir,
    copyFile: (...args: Parameters<typeof fs.copyFile>) => fs.copyFile(...args),
    writeFile: (...args: Parameters<typeof fs.writeFile>) => fs.writeFile(...args),
    chmod: (...args: Parameters<typeof fs.chmod>) => fs.chmod(...args),
    rename: (...args: Parameters<typeof fs.rename>) => fs.rename(...args),
    rm: async (target: Parameters<typeof fs.rm>[0], options: Parameters<typeof fs.rm>[1]) => {
      if (String(target).endsWith(".ruk-pending")) throw new Error("injected temporary cleanup failure");
      return fs.rm(target, options);
    },
  };

  const result = await replaceWindowsOutputs([{ contents: "new-binary", destination }], fileSystem);

  assert.equal(result.cleanupPending, true);
  assert.equal(await fs.readFile(destination, "utf8"), "new-binary");
});

test("Windows package updates defer native replacement to a detached handoff", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-deferred-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const packageRoot = path.join(root, "node_modules", "@xenoviz", "ruk-windows-x64");
  const native = path.join(packageRoot, "native", "ruk.exe");
  const contents = Buffer.from("deferred-windows-ruk-binary");
  await fs.mkdir(path.dirname(native), { recursive: true });
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
    ruk: { distribution: "package", target: "windows-x64", binary: "native/ruk.exe", sha256 },
  }));
  const commandDestination = path.join(root, "node_modules", ".bin", "ruk.exe");
  let spawnCall: [string, string[], Record<string, unknown>] | undefined;
  const result = await installNativeLauncher({
    root,
    platform: "win32",
    arch: "x64",
    commandDestination,
    environment: { RUK_UPDATE_PID: "123" },
    spawnReplacement: (command: string, args: string[], options: { detached: boolean; stdio: "ignore"; windowsHide: boolean }) => {
      spawnCall = [command, args, options];
      return { unref() {} };
    },
  });
  assert.equal(windowsUpdateProcessID({ RUK_UPDATE_PID: "123" }), 123);
  assert.equal(windowsUpdateProcessID({ RUK_UPDATE_PID: "0" }), undefined);
  assert.equal(windowsUpdateProcessID({ RUK_UPDATE_PID: "not-a-pid" }), undefined);
  assert.equal(result.destination.endsWith("bin/ruk.exe"), true);
  assert.equal(result.deferred, true);
  assert.equal(spawnCall?.[0], process.execPath);
  assert.equal(spawnCall?.[1]?.[0], "--input-type=module");
  assert.equal(spawnCall?.[1]?.[1], "-e");
  assert.equal(spawnCall?.[1]?.[3], "123");
  await assert.rejects(fs.access(result.destination));
  await assert.rejects(fs.access(commandDestination));

  if (spawnCall === undefined) throw new Error("deferred replacement helper was not captured");
  const invalidPIDArgs = [...spawnCall[1]];
  invalidPIDArgs[3] = "0";
  const helper = spawnSync(spawnCall[0], invalidPIDArgs, { stdio: "ignore" });
  assert.notEqual(helper.status, 0);
});

test("Windows deferred staging cleans up when detached handoff cannot start", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-launcher-spawn-failure-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const packageRoot = path.join(root, "node_modules", "@xenoviz", "ruk-windows-x64");
  const native = path.join(packageRoot, "native", "ruk.exe");
  const contents = Buffer.from("spawn-failure-windows-ruk-binary");
  await fs.mkdir(path.dirname(native), { recursive: true });
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
    ruk: { distribution: "package", target: "windows-x64", binary: "native/ruk.exe", sha256 },
  }));
  const commandDestination = path.join(root, "node_modules", ".bin", "ruk.exe");
  await assert.rejects(
    installNativeLauncher({
      root,
      platform: "win32",
      arch: "x64",
      commandDestination,
      environment: { RUK_UPDATE_PID: "123" },
      spawnReplacement: () => {
        throw new Error("injected detached spawn failure");
      },
    }),
    /injected detached spawn failure/,
  );
  const files = await fs.readdir(root, { recursive: true });
  assert.deepEqual(files.filter((file) => file.endsWith(".ruk-pending")), []);
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
    ruk: { distribution: "package", target: "linux-x64", binary: "native/ruk", sha256: "0".repeat(64) },
  }));
  await assert.rejects(
    installNativeLauncher({ root: value.root, platform: "linux", arch: "x64", libc: "glibc" }),
    /checksum mismatch/,
  );
  assert.equal(await fs.readFile(destination, "utf8"), "previous-native");

  await fs.writeFile(packageJSON, JSON.stringify({
    name: "@xenoviz/ruk-linux-x64",
    version,
    ruk: { distribution: "package", target: "linux-x64", binary: "../escape", sha256: "0".repeat(64) },
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

test("ensureNativeLauncher reuses a verified placement and installs when scripts were skipped", async (t) => {
  const value = await fixture();
  t.after(() => fs.rm(value.root, { recursive: true, force: true }));
  const destination = path.join(value.root, "bin", "ruk");
  await fs.writeFile(destination, "#!/usr/bin/env node\nexport {};\n");

  const installed = await ensureNativeLauncher({ root: value.root, platform: "linux", arch: "x64", libc: "glibc" });
  assert.equal(installed.reused, false);
  assert.deepEqual(await fs.readFile(destination), value.contents);

  const reused = await ensureNativeLauncher({ root: value.root, platform: "linux", arch: "x64", libc: "glibc" });
  assert.equal(reused.reused, true);
  assert.equal(reused.destination, destination);
  assert.deepEqual(await fs.readFile(destination), value.contents);
});

test("runPackageCommand installs when needed then executes the native binary", async (t) => {
  const value = await fixture();
  t.after(() => fs.rm(value.root, { recursive: true, force: true }));
  const destination = path.join(value.root, "bin", "ruk");
  await fs.writeFile(destination, "#!/usr/bin/env node\nexport {};\n");

  let exitCode: number | undefined;
  const firstSpawn: { command?: string; args?: readonly string[] } = {};
  const result = await runPackageCommand({
    root: value.root,
    platform: "linux",
    arch: "x64",
    libc: "glibc",
    args: ["--version"],
    exit: (code) => {
      exitCode = code;
    },
    spawnSync: (command, args) => {
      firstSpawn.command = command;
      firstSpawn.args = args;
      return { status: 0, signal: null };
    },
  });

  assert.equal(result.status, 0);
  assert.equal(exitCode, 0);
  assert.equal(firstSpawn.command, destination);
  assert.deepEqual(firstSpawn.args, ["--version"]);
  assert.deepEqual(await fs.readFile(destination), value.contents);
  assert.equal(result.reused, false);

  exitCode = undefined;
  const secondSpawn: { command?: string; args?: readonly string[] } = {};
  const reused = await runPackageCommand({
    root: value.root,
    platform: "linux",
    arch: "x64",
    libc: "glibc",
    args: ["--help"],
    exit: (code) => {
      exitCode = code;
    },
    spawnSync: (command, args) => {
      secondSpawn.command = command;
      secondSpawn.args = args;
      return { status: 7, signal: null };
    },
  });
  assert.equal(reused.status, 7);
  assert.equal(exitCode, 7);
  assert.equal(reused.reused, true);
  assert.equal(secondSpawn.command, destination);
  assert.deepEqual(secondSpawn.args, ["--help"]);
});

test("published bin/ruk delegates to runPackageCommand", async () => {
  const launcher = await fs.readFile(new URL("../../npm/ruk/bin/ruk", import.meta.url), "utf8");
  assert.match(launcher, /runPackageCommand/);
  assert.match(launcher, /scripts\/npm\/launcher\.mjs/);
});
