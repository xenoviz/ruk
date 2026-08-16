import crypto from "node:crypto";
import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import { createRequire } from "node:module";
import path from "node:path";
import { fileURLToPath } from "node:url";

const PACKAGE_SCOPE = "@xenoviz";
const ROOT_PACKAGE = "@xenoviz/ruk";

export const NATIVE_TARGETS = Object.freeze({
  "linux-x64": Object.freeze({ packageName: "@xenoviz/ruk-linux-x64", platform: "linux", arch: "x64", libc: "glibc" }),
  "linux-arm64": Object.freeze({ packageName: "@xenoviz/ruk-linux-arm64", platform: "linux", arch: "arm64", libc: "glibc" }),
  "linux-x64-musl": Object.freeze({ packageName: "@xenoviz/ruk-linux-x64-musl", platform: "linux", arch: "x64", libc: "musl" }),
  "darwin-x64": Object.freeze({ packageName: "@xenoviz/ruk-darwin-x64", platform: "darwin", arch: "x64" }),
  "darwin-arm64": Object.freeze({ packageName: "@xenoviz/ruk-darwin-arm64", platform: "darwin", arch: "arm64" }),
  "windows-x64": Object.freeze({ packageName: "@xenoviz/ruk-windows-x64", platform: "win32", arch: "x64" }),
  "windows-arm64": Object.freeze({ packageName: "@xenoviz/ruk-windows-arm64", platform: "win32", arch: "arm64" }),
});

export function detectLibc(platform = process.platform, report = process.report) {
  if (platform !== "linux") return undefined;
  try {
    const header = report?.getReport?.().header;
    if (typeof header?.glibcVersionRuntime === "string" && header.glibcVersionRuntime !== "") return "glibc";
    return "musl";
  } catch {
    return "unknown";
  }
}

export function platformTarget(platform = process.platform, arch = process.arch, libc = detectLibc(platform)) {
  if (platform === "linux" && arch === "x64" && libc === "glibc") {
    return { packageName: "@xenoviz/ruk-linux-x64", target: "linux-x64" };
  }
  if (platform === "linux" && arch === "x64" && libc === "musl") {
    return { packageName: "@xenoviz/ruk-linux-x64-musl", target: "linux-x64-musl" };
  }
  if (platform === "linux" && arch === "arm64" && libc === "glibc") {
    return { packageName: "@xenoviz/ruk-linux-arm64", target: "linux-arm64" };
  }
  if (platform === "darwin" && arch === "x64") {
    return { packageName: "@xenoviz/ruk-darwin-x64", target: "darwin-x64" };
  }
  if (platform === "darwin" && arch === "arm64") {
    return { packageName: "@xenoviz/ruk-darwin-arm64", target: "darwin-arm64" };
  }
  if (platform === "win32" && arch === "x64") {
    return { packageName: "@xenoviz/ruk-windows-x64", target: "windows-x64" };
  }
  if (platform === "win32" && arch === "arm64") {
    return { packageName: "@xenoviz/ruk-windows-arm64", target: "windows-arm64" };
  }
  const libcSuffix = platform === "linux" ? `/${libc ?? "unknown"}` : "";
  throw new Error(`Ruk npm package is not available for ${platform}/${arch}${libcSuffix}; reinstall with a supported platform package`);
}

export function installerFromEnvironment(environment = process.env) {
  const executable = String(environment.npm_execpath ?? environment.npm_command ?? "").toLowerCase().replaceAll("\\", "/");
  if (executable.includes("bun")) return "bun";
  if (executable.includes("pnpm")) return "pnpm";
  if (executable.includes("yarn")) return "yarn";
  return "npm";
}

function isObject(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

async function readJSON(filename, description) {
  let text;
  try {
    text = await fs.readFile(filename, "utf8");
  } catch (error) {
    throw new Error(`Cannot read ${description} at ${filename}`, { cause: error });
  }
  try {
    return JSON.parse(text);
  } catch (error) {
    throw new Error(`Cannot parse ${description} at ${filename}`, { cause: error });
  }
}

function relativePath(base, value, description) {
  if (typeof value !== "string" || value.trim() === "" || path.isAbsolute(value)) {
    throw new Error(`${description} must be a relative path inside its package`);
  }
  const parts = value.split(/[\\/]+/);
  if (parts.includes("..")) throw new Error(`${description} must stay inside its package`);
  const resolved = path.resolve(base, value);
  const relative = path.relative(base, resolved);
  if (relative === "" || relative.startsWith(`..${path.sep}`) || path.isAbsolute(relative)) {
    throw new Error(`${description} must stay inside its package`);
  }
  return resolved;
}

function packageManifest(root, packageName) {
  const [scope, name] = packageName.split("/");
  if (scope !== PACKAGE_SCOPE || !name) throw new Error(`Invalid native package name ${packageName}`);
  return createRequire(path.join(root, "package.json")).resolve(`${packageName}/package.json`);
}

async function digest(filename) {
  return crypto.createHash("sha256").update(await fs.readFile(filename)).digest("hex");
}

async function atomicCopy(source, destination, executable) {
  await fs.mkdir(path.dirname(destination), { recursive: true });
  const temporary = `${destination}.${process.pid}.${crypto.randomUUID()}.tmp`;
  try {
    await fs.copyFile(source, temporary);
    if (executable) await fs.chmod(temporary, 0o755);
    try {
      await fs.rename(temporary, destination);
    } catch (error) {
      if (process.platform !== "win32") throw error;
      await fs.rm(destination, { force: true });
      await fs.rename(temporary, destination);
    }
  } finally {
    await fs.rm(temporary, { force: true });
  }
}

async function atomicWrite(filename, contents) {
  await fs.mkdir(path.dirname(filename), { recursive: true });
  const temporary = `${filename}.${process.pid}.${crypto.randomUUID()}.tmp`;
  try {
    await fs.writeFile(temporary, contents, { mode: 0o644 });
    try {
      await fs.rename(temporary, filename);
    } catch (error) {
      if (process.platform !== "win32") throw error;
      await fs.rm(filename, { force: true });
      await fs.rename(temporary, filename);
    }
  } finally {
    await fs.rm(temporary, { force: true });
  }
}

export function windowsCommandDestination(root, environment = process.env) {
  const globalInstall = environment.npm_config_global === "true";
  if (globalInstall) {
    const prefix = environment.npm_config_prefix;
    if (typeof prefix === "string" && prefix !== "") return path.resolve(prefix, "ruk.exe");
    return path.resolve(root, "..", "..", "..", "ruk.exe");
  }
  const localPrefix = environment.npm_config_local_prefix ?? environment.INIT_CWD;
  if (typeof localPrefix === "string" && localPrefix !== "") {
    return path.resolve(localPrefix, "node_modules", ".bin", "ruk.exe");
  }
  return path.resolve(root, "..", "..", ".bin", "ruk.exe");
}

// Package updates are normally installed by a child package-manager process.
// When that child was launched by a running native Ruk executable, Windows
// keeps the old ruk.exe locked until the parent exits. The update command
// passes that parent PID through this narrow environment marker so the
// postinstall can hand off replacement without guessing which process owns a
// file or attempting to delete a live executable.
export function windowsUpdateProcessID(environment = process.env) {
  const value = String(environment.RUK_UPDATE_PID ?? "").trim();
  if (!/^\d+$/.test(value)) return undefined;
  const pid = Number(value);
  return Number.isSafeInteger(pid) && pid > 0 ? pid : undefined;
}

const WINDOWS_REPLACEMENT_SCRIPT = String.raw`import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";

const [pidText, ...values] = process.argv.slice(1);
const pid = Number(pidText);
const entries = [];
for (let index = 0; index + 1 < values.length; index += 2) entries.push([values[index], values[index + 1]]);
if (!Number.isSafeInteger(pid) || pid <= 0 || entries.length === 0) process.exit(2);
const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));
const alive = () => {
  try { process.kill(pid, 0); return true; }
  catch (error) { return error?.code === "EPERM"; }
};
const failurePath = (destination) => destination + ".ruk-update-failed";
const reportStatus = async (status, error) => {
  const message = JSON.stringify({ schemaVersion: 1, status, error: String(error?.message ?? error), pid, at: new Date().toISOString() }) + "\n";
  for (const [, destination] of entries) await fs.writeFile(failurePath(destination), message, { mode: 0o600 }).catch(() => {});
};
const reportFailure = (error) => reportStatus("failed", error);
const reportCleanupPending = (error) => reportStatus("committed-cleanup-pending", error);
const deadline = Date.now() + 10 * 60 * 1000;
while (alive() && Date.now() < deadline) await delay(100);
if (alive()) {
  await reportFailure(new Error("the previous Ruk process did not exit before the update handoff deadline"));
  process.exit(1);
}
for (let attempt = 0; attempt < 120; attempt += 1) {
  const backups = [];
  try {
    for (const [source, destination] of entries) {
      await fs.mkdir(path.dirname(destination), { recursive: true });
      const backup = destination + "." + process.pid + "." + crypto.randomUUID() + ".ruk-backup";
      let hadDestination = true;
      try { await fs.rename(destination, backup); }
      catch (error) {
        if (error?.code !== "ENOENT") throw error;
        hadDestination = false;
      }
      backups.push([source, destination, backup, hadDestination, false]);
      await fs.rename(source, destination);
      backups[backups.length - 1][4] = true;
    }
    // Every staged source has been installed. The transaction is committed;
    // cleanup failures must never trigger a rollback that could remove the
    // newly installed binaries or rely on an already-deleted backup.
    const cleanupErrors = [];
    for (const [, , backup, hadDestination] of backups) {
      if (!hadDestination) continue;
      try { await fs.rm(backup, { force: true }); }
      catch (cleanupError) { cleanupErrors.push(cleanupError); }
    }
    if (cleanupErrors.length > 0) {
      await reportCleanupPending(new Error("Windows update committed with backup cleanup pending: " + cleanupErrors.map((cleanupError) => cleanupError?.message ?? cleanupError).join("; ")));
    } else {
      for (const [, destination] of entries) await fs.rm(failurePath(destination), { force: true }).catch(() => {});
    }
    process.exit(0);
  } catch (error) {
    const rollbackErrors = [];
    for (let index = backups.length - 1; index >= 0; index -= 1) {
      const [source, destination, backup, hadDestination, installed] = backups[index];
      try {
        if (installed) await fs.rename(destination, source);
        if (hadDestination) await fs.rename(backup, destination);
      } catch (restoreError) {
        rollbackErrors.push(restoreError);
      }
    }
    if (rollbackErrors.length > 0) {
      await reportFailure(new Error("Windows update rollback failed: " + rollbackErrors.map((restoreError) => restoreError?.message ?? restoreError).join("; ")));
      process.exit(1);
    }
    await reportFailure(error);
    await delay(250);
  }
}
await reportFailure(new Error("the Windows update handoff could not replace all destinations"));
process.exit(1);`;

async function scheduleWindowsReplacement(entries, pid, spawner = spawn) {
  const child = spawner(process.execPath, ["--input-type=module", "-e", WINDOWS_REPLACEMENT_SCRIPT, String(pid), ...entries.flat()], {
    detached: true,
    stdio: "ignore",
    windowsHide: true,
  });
  child.unref();
}

async function removeStagedWindowsEntries(entries) {
  await Promise.all(entries.map(([source]) => fs.rm(source, { force: true }).catch(() => {})));
}

async function stageWindowsCopy(source, destination) {
  await fs.mkdir(path.dirname(destination), { recursive: true });
  const staged = `${destination}.${process.pid}.${crypto.randomUUID()}.ruk-pending`;
  try {
    await fs.copyFile(source, staged);
    return staged;
  } catch (error) {
    await fs.rm(staged, { force: true }).catch(() => {});
    throw error;
  }
}

async function stageWindowsContents(contents, destination) {
  await fs.mkdir(path.dirname(destination), { recursive: true });
  const staged = `${destination}.${process.pid}.${crypto.randomUUID()}.ruk-pending`;
  try {
    await fs.writeFile(staged, contents, { mode: 0o600 });
    return staged;
  } catch (error) {
    await fs.rm(staged, { force: true }).catch(() => {});
    throw error;
  }
}

export async function installNativeLauncher(options = {}) {
  const root = path.resolve(options.root ?? fileURLToPath(new URL("../..", import.meta.url)));
  const platform = options.platform ?? process.platform;
  const arch = options.arch ?? process.arch;
  const libc = options.libc ?? detectLibc(platform);
  const selected = platformTarget(platform, arch, libc);
  const rootManifest = await readJSON(path.join(root, "package.json"), "Ruk package manifest");
  if (!isObject(rootManifest) || rootManifest.name !== ROOT_PACKAGE) {
    throw new Error(`Ruk npm installer expected package ${ROOT_PACKAGE}`);
  }
  if (typeof rootManifest.version !== "string" || rootManifest.version === "") {
    throw new Error("Ruk npm installer requires a package version");
  }
  if (!isObject(rootManifest.ruk) || rootManifest.ruk.distribution !== "package") {
    throw new Error("Ruk npm installer requires the package distribution marker");
  }
  const configuredDestination = relativePath(root, rootManifest.ruk.binaryPath ?? "bin/ruk", "Ruk native destination");
  const destination = platform === "win32" ? `${configuredDestination}.exe` : configuredDestination;
  const installer = installerFromEnvironment(options.environment);
  const markerContents = `${JSON.stringify({ schemaVersion: 1, distribution: "package", installer })}\n`;
  const marker = `${destination}.ruk-distribution`;
  let nativeManifestPath;
  try {
    nativeManifestPath = packageManifest(root, selected.packageName);
    await fs.access(nativeManifestPath);
  } catch (error) {
    throw new Error(`Ruk optional native package ${selected.packageName} is missing; reinstall @xenoviz/ruk`, { cause: error });
  }
  const nativeRoot = path.dirname(nativeManifestPath);
  const nativeManifest = await readJSON(nativeManifestPath, selected.packageName);
  if (!isObject(nativeManifest) || nativeManifest.name !== selected.packageName) {
    throw new Error(`Ruk optional native package metadata is not ${selected.packageName}`);
  }
  if (nativeManifest.version !== rootManifest.version) {
    throw new Error(`Ruk optional native package ${selected.packageName} has version ${String(nativeManifest.version)}; expected ${rootManifest.version}`);
  }
  if (!isObject(nativeManifest.ruk) || nativeManifest.ruk.distribution !== "package") {
    throw new Error(`Ruk optional native package ${selected.packageName} is missing the package distribution marker`);
  }
  if (nativeManifest.ruk.target !== selected.target) {
    throw new Error(`Ruk optional native package ${selected.packageName} targets ${String(nativeManifest.ruk.target)}; expected ${selected.target}`);
  }
  const source = relativePath(nativeRoot, nativeManifest.ruk.binary, "Ruk native binary path");
  const expectedDigest = nativeManifest.ruk.sha256;
  if (typeof expectedDigest !== "string" || !/^[a-f0-9]{64}$/i.test(expectedDigest)) {
    throw new Error(`Ruk optional native package ${selected.packageName} has an invalid SHA-256 digest`);
  }
  let stat;
  try {
    stat = await fs.stat(source);
  } catch (error) {
    throw new Error(`Ruk native binary is missing at ${source}`, { cause: error });
  }
  if (!stat.isFile() || stat.size <= 0) throw new Error(`Ruk native binary is empty or not a file at ${source}`);
  if (platform !== "win32" && (stat.mode & 0o111) === 0) throw new Error(`Ruk native binary is not executable at ${source}`);
  const actualDigest = await digest(source);
  if (actualDigest !== expectedDigest.toLowerCase()) {
    throw new Error(`Ruk native binary checksum mismatch for ${selected.packageName}`);
  }
  const updatePID = platform === "win32" ? windowsUpdateProcessID(options.environment) : undefined;
  const deferredEntries = [];
  const commandDestination = options.commandDestination ?? (
    platform === "win32" ? windowsCommandDestination(root, options.environment) : undefined
  );
  try {
    if (updatePID !== undefined) {
      deferredEntries.push([await stageWindowsCopy(source, destination), destination]);
      if (commandDestination !== undefined) {
        if (platform !== "win32" || path.extname(commandDestination).toLowerCase() !== ".exe") {
          throw new Error("Ruk native command destination must be a Windows .exe path");
        }
        const resolvedCommandDestination = path.resolve(commandDestination);
        deferredEntries.push([await stageWindowsCopy(source, resolvedCommandDestination), resolvedCommandDestination]);
        const commandMarker = `${resolvedCommandDestination}.ruk-distribution`;
        deferredEntries.push([await stageWindowsContents(markerContents, commandMarker), commandMarker]);
      }
      deferredEntries.push([await stageWindowsContents(markerContents, marker), marker]);
      await scheduleWindowsReplacement(deferredEntries, updatePID, options.spawnReplacement ?? spawn);
    } else {
      await atomicCopy(source, destination, platform !== "win32");
      if (commandDestination !== undefined) {
        if (platform !== "win32" || path.extname(commandDestination).toLowerCase() !== ".exe") {
          throw new Error("Ruk native command destination must be a Windows .exe path");
        }
        const resolvedCommandDestination = path.resolve(commandDestination);
        await atomicCopy(source, resolvedCommandDestination, false);
        await atomicWrite(`${resolvedCommandDestination}.ruk-distribution`, markerContents);
      }
      await atomicWrite(marker, markerContents);
    }
  } catch (error) {
    if (updatePID !== undefined) await removeStagedWindowsEntries(deferredEntries);
    throw error;
  }
  return { packageName: selected.packageName, target: selected.target, destination, sha256: actualDigest, installer, deferred: updatePID !== undefined };
}
