import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const PACKAGE_SCOPE = "@xenoviz";
const ROOT_PACKAGE = "@xenoviz/ruk";

export const NATIVE_TARGETS = Object.freeze({
  "bun-linux-x64-baseline": Object.freeze({ packageName: "@xenoviz/ruk-linux-x64", platform: "linux", arch: "x64", libc: "glibc" }),
  "bun-linux-arm64": Object.freeze({ packageName: "@xenoviz/ruk-linux-arm64", platform: "linux", arch: "arm64", libc: "glibc" }),
  "bun-linux-x64-musl-baseline": Object.freeze({ packageName: "@xenoviz/ruk-linux-x64-musl", platform: "linux", arch: "x64", libc: "musl" }),
  "bun-darwin-x64": Object.freeze({ packageName: "@xenoviz/ruk-darwin-x64", platform: "darwin", arch: "x64" }),
  "bun-darwin-arm64": Object.freeze({ packageName: "@xenoviz/ruk-darwin-arm64", platform: "darwin", arch: "arm64" }),
  "bun-windows-x64-baseline": Object.freeze({ packageName: "@xenoviz/ruk-windows-x64", platform: "win32", arch: "x64" }),
  "bun-windows-arm64": Object.freeze({ packageName: "@xenoviz/ruk-windows-arm64", platform: "win32", arch: "arm64" }),
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
    return { packageName: "@xenoviz/ruk-linux-x64", target: "bun-linux-x64-baseline" };
  }
  if (platform === "linux" && arch === "x64" && libc === "musl") {
    return { packageName: "@xenoviz/ruk-linux-x64-musl", target: "bun-linux-x64-musl-baseline" };
  }
  if (platform === "linux" && arch === "arm64" && libc === "glibc") {
    return { packageName: "@xenoviz/ruk-linux-arm64", target: "bun-linux-arm64" };
  }
  if (platform === "darwin" && arch === "x64") {
    return { packageName: "@xenoviz/ruk-darwin-x64", target: "bun-darwin-x64" };
  }
  if (platform === "darwin" && arch === "arm64") {
    return { packageName: "@xenoviz/ruk-darwin-arm64", target: "bun-darwin-arm64" };
  }
  if (platform === "win32" && arch === "x64") {
    return { packageName: "@xenoviz/ruk-windows-x64", target: "bun-windows-x64-baseline" };
  }
  if (platform === "win32" && arch === "arm64") {
    return { packageName: "@xenoviz/ruk-windows-arm64", target: "bun-windows-arm64" };
  }
  const libcSuffix = platform === "linux" ? `/${libc ?? "unknown"}` : "";
  throw new Error(`Ruk npm package is not available for ${platform}/${arch}${libcSuffix}; reinstall with a supported platform package`);
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

function packageDirectory(root, packageName) {
  const [scope, name] = packageName.split("/");
  if (scope !== PACKAGE_SCOPE || !name) throw new Error(`Invalid native package name ${packageName}`);
  return path.join(root, "node_modules", scope, name);
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
  const marker = path.join(path.dirname(destination), ".ruk-distribution");
  const nativeRoot = packageDirectory(root, selected.packageName);
  const nativeManifestPath = path.join(nativeRoot, "package.json");
  try {
    await fs.access(nativeManifestPath);
  } catch (error) {
    throw new Error(`Ruk optional native package ${selected.packageName} is missing; reinstall @xenoviz/ruk`, { cause: error });
  }
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
  await atomicCopy(source, destination, platform !== "win32");
  const commandDestination = options.commandDestination ?? (
    platform === "win32" ? windowsCommandDestination(root, options.environment) : undefined
  );
  if (commandDestination !== undefined) {
    if (platform !== "win32" || path.extname(commandDestination).toLowerCase() !== ".exe") {
      throw new Error("Ruk native command destination must be a Windows .exe path");
    }
    await atomicCopy(source, path.resolve(commandDestination), false);
  }
  await atomicWrite(marker, "package\n");
  return { packageName: selected.packageName, target: selected.target, destination, sha256: actualDigest };
}
