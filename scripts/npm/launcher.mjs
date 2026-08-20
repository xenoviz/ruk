import crypto from "node:crypto";
import { spawn, spawnSync } from "node:child_process";
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
    await fs.rename(temporary, destination);
  } finally {
    await fs.rm(temporary, { force: true });
  }
}

async function atomicWrite(filename, contents) {
  await fs.mkdir(path.dirname(filename), { recursive: true });
  const temporary = `${filename}.${process.pid}.${crypto.randomUUID()}.tmp`;
  try {
    await fs.writeFile(temporary, contents, { mode: 0o644 });
    await fs.rename(temporary, filename);
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

async function removeTemporaryOutputs(outputs, fileSystem) {
  const results = await Promise.all(outputs.map(async ({ temporary }) => {
    try {
      await fileSystem.rm(temporary, { force: true });
      return { error: undefined };
    } catch (error) {
      return { error };
    }
  }));
  return results.flatMap(({ error }) => error === undefined ? [] : [error]);
}

async function rollbackWindowsOutputs(outputs, fileSystem) {
  const errors = [];
  for (let index = outputs.length - 1; index >= 0; index -= 1) {
    const output = outputs[index];
    if (!output.installed && !output.backupMoved) continue;
    if (output.installed) {
      try {
        await fileSystem.rm(output.destination, { force: true });
      } catch (error) {
        errors.push(error);
      }
    }
    if (output.backupMoved) {
      try {
        await fileSystem.rename(output.backup, output.destination);
      } catch (error) {
        errors.push(error);
      }
    }
  }
  return errors;
}

// replaceWindowsOutputs stages every direct-install output before touching an
// existing destination, then commits all replacements as one rollback-capable
// transaction. Backups are retained until every destination is installed.
export async function replaceWindowsOutputs(outputs, fileSystem = fs) {
  if (!Array.isArray(outputs) || outputs.length === 0) {
    throw new Error("Windows replacement requires at least one output");
  }
  const destinations = new Set();
  for (const output of outputs) {
    if (!output || typeof output.destination !== "string" || output.destination === "") {
      throw new Error("Windows replacement output destination is invalid");
    }
    const hasSource = typeof output.source === "string";
    const hasContents = typeof output.contents === "string";
    if (hasSource === hasContents) {
      throw new Error(`Windows replacement output ${output.destination} must have exactly one source or contents`);
    }
    const normalizedDestination = path.resolve(path.normalize(output.destination)).toLowerCase();
    if (destinations.has(normalizedDestination)) {
      throw new Error(`Windows replacement outputs contain duplicate destination: ${output.destination}`);
    }
    destinations.add(normalizedDestination);
  }
  const staged = [];
  try {
    for (const output of outputs) {
      const temporary = `${output.destination}.${process.pid}.${crypto.randomUUID()}.ruk-pending`;
      const entry = {
        destination: output.destination,
        temporary,
        backup: `${output.destination}.${process.pid}.${crypto.randomUUID()}.ruk-backup`,
        backupMoved: false,
        installed: false,
      };
      staged.push(entry);
      await fileSystem.mkdir(path.dirname(output.destination), { recursive: true });
      if (typeof output.source === "string") {
        await fileSystem.copyFile(output.source, temporary);
      } else {
        await fileSystem.writeFile(temporary, output.contents, { mode: output.mode ?? 0o644 });
      }
      if (output.executable) await fileSystem.chmod(temporary, 0o755);
    }
  } catch (error) {
    const cleanupErrors = await removeTemporaryOutputs(staged, fileSystem);
    if (cleanupErrors.length > 0) {
      throw new AggregateError([error, ...cleanupErrors], "Windows replacement staging cleanup failed");
    }
    throw error;
  }

  let committed = false;
  try {
    for (const output of staged) {
      let hadDestination = true;
      try {
        await fileSystem.rename(output.destination, output.backup);
        output.backupMoved = true;
      } catch (error) {
        if (error?.code !== "ENOENT") throw error;
        hadDestination = false;
      }
      output.hadDestination = hadDestination;
      await fileSystem.rename(output.temporary, output.destination);
      output.installed = true;
    }
    committed = true;
    const cleanupErrors = [];
    for (const output of staged) {
      if (!output.hadDestination) continue;
      try {
        await fileSystem.rm(output.backup, { force: true });
      } catch (error) {
        cleanupErrors.push(error);
      }
    }
    const temporaryCleanupErrors = await removeTemporaryOutputs(staged, fileSystem);
    return { cleanupPending: cleanupErrors.length > 0 || temporaryCleanupErrors.length > 0 };
  } catch (error) {
    if (!committed) {
      const rollbackErrors = await rollbackWindowsOutputs(staged, fileSystem);
      const cleanupErrors = await removeTemporaryOutputs(staged, fileSystem);
      if (rollbackErrors.length > 0 || cleanupErrors.length > 0) {
        throw new AggregateError(
          [error, ...rollbackErrors, ...cleanupErrors],
          "Windows replacement rollback failed",
        );
      }
    }
    throw error;
  }
}

async function resolveNativePackage(options = {}) {
  const root = path.resolve(
    options.root instanceof URL
      ? fileURLToPath(options.root)
      : options.root ?? fileURLToPath(new URL("../..", import.meta.url)),
  );
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
  const sha256 = await digest(source);
  if (sha256 !== expectedDigest.toLowerCase()) {
    throw new Error(`Ruk native binary checksum mismatch for ${selected.packageName}`);
  }
  const commandDestination = options.commandDestination ?? (
    platform === "win32" ? windowsCommandDestination(root, options.environment) : undefined
  );
  return {
    root,
    platform,
    selected,
    source,
    sha256,
    destination,
    marker,
    markerContents,
    installer,
    commandDestination,
  };
}

async function reuseInstalledNative(resolved) {
  try {
    const installedDigest = await digest(resolved.destination);
    if (installedDigest !== resolved.sha256) return undefined;
    const marker = await readJSON(resolved.marker, "Ruk distribution marker");
    if (!isObject(marker) || marker.schemaVersion !== 1 || marker.distribution !== "package") return undefined;
    if (typeof marker.installer !== "string" || marker.installer === "") return undefined;
    return {
      packageName: resolved.selected.packageName,
      target: resolved.selected.target,
      destination: resolved.destination,
      sha256: installedDigest,
      installer: marker.installer,
      deferred: false,
      cleanupPending: false,
      reused: true,
    };
  } catch {
    return undefined;
  }
}

export async function installNativeLauncher(options = {}) {
  const resolved = await resolveNativePackage(options);
  const updatePID = resolved.platform === "win32" ? windowsUpdateProcessID(options.environment) : undefined;
  const deferredEntries = [];
  let cleanupPending = false;
  try {
    if (updatePID !== undefined) {
      deferredEntries.push([await stageWindowsCopy(resolved.source, resolved.destination), resolved.destination]);
      if (resolved.commandDestination !== undefined) {
        if (resolved.platform !== "win32" || path.extname(resolved.commandDestination).toLowerCase() !== ".exe") {
          throw new Error("Ruk native command destination must be a Windows .exe path");
        }
        const resolvedCommandDestination = path.resolve(resolved.commandDestination);
        deferredEntries.push([await stageWindowsCopy(resolved.source, resolvedCommandDestination), resolvedCommandDestination]);
        const commandMarker = `${resolvedCommandDestination}.ruk-distribution`;
        deferredEntries.push([await stageWindowsContents(resolved.markerContents, commandMarker), commandMarker]);
      }
      deferredEntries.push([await stageWindowsContents(resolved.markerContents, resolved.marker), resolved.marker]);
      await scheduleWindowsReplacement(deferredEntries, updatePID, options.spawnReplacement ?? spawn);
    } else {
      if (resolved.platform !== "win32" && resolved.commandDestination !== undefined) {
        throw new Error("Ruk native command destination must be a Windows .exe path");
      }
      if (resolved.platform === "win32") {
        const outputs = [{ source: resolved.source, destination: resolved.destination }];
        if (resolved.commandDestination !== undefined) {
          if (path.extname(resolved.commandDestination).toLowerCase() !== ".exe") {
            throw new Error("Ruk native command destination must be a Windows .exe path");
          }
          const resolvedCommandDestination = path.resolve(resolved.commandDestination);
          outputs.push(
            { source: resolved.source, destination: resolvedCommandDestination },
            { contents: resolved.markerContents, destination: `${resolvedCommandDestination}.ruk-distribution` },
          );
        }
        outputs.push({ contents: resolved.markerContents, destination: resolved.marker });
        const replacement = await replaceWindowsOutputs(outputs, options.fileSystem ?? fs);
        cleanupPending = replacement.cleanupPending;
      } else {
        await atomicCopy(resolved.source, resolved.destination, true);
        await atomicWrite(resolved.marker, resolved.markerContents);
      }
    }
  } catch (error) {
    if (updatePID !== undefined) await removeStagedWindowsEntries(deferredEntries);
    throw error;
  }
  return {
    packageName: resolved.selected.packageName,
    target: resolved.selected.target,
    destination: resolved.destination,
    sha256: resolved.sha256,
    installer: resolved.installer,
    deferred: updatePID !== undefined,
    cleanupPending,
    reused: false,
  };
}

// Ensures the verified native binary is on the package command path. Package
// managers may skip postinstall; the published bin/ruk entry uses this path so
// the first command invocation can finish installation and then exec the native
// binary without requiring lifecycle scripts.
export async function ensureNativeLauncher(options = {}) {
  const resolved = await resolveNativePackage(options);
  const reused = await reuseInstalledNative(resolved);
  if (reused !== undefined) return reused;
  return installNativeLauncher(options);
}

export async function runPackageCommand(options = {}) {
  const exit = options.exit ?? ((code) => {
    process.exit(code);
  });
  const writeError = options.writeError ?? ((message) => {
    process.stderr.write(message);
  });
  const run = options.spawnSync ?? spawnSync;
  try {
    const installed = await ensureNativeLauncher(options);
    if (installed.deferred) {
      writeError(`Scheduled native replacement ${installed.target} for ${installed.packageName}.\n`);
      exit(0);
      return { ...installed, status: 0 };
    }
    if (installed.cleanupPending) {
      writeError("Ruk native installation succeeded, but temporary backup cleanup is pending; manual cleanup may be required.\n");
    }
    const result = run(installed.destination, options.args ?? process.argv.slice(2), {
      stdio: "inherit",
      env: options.environment ?? process.env,
      windowsHide: false,
    });
    if (result.error) throw result.error;
    if (typeof result.signal === "string" && result.signal !== "") {
      try {
        process.kill(process.pid, result.signal);
      } catch {
        exit(1);
      }
      return { ...installed, status: 1, signal: result.signal };
    }
    const status = Number.isInteger(result.status) ? result.status : 1;
    exit(status);
    return { ...installed, status };
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    writeError(`Ruk native launcher failed: ${message}\n`);
    exit(1);
    return { status: 1, error: message };
  }
}
