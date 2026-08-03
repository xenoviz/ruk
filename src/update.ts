import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { spawn } from "node:child_process";
import type { StdioOptions } from "node:child_process";
import { run } from "./process.js";
import {
  compareVersions,
  RELEASE_ASSET_NAMES,
  RUK_PACKAGE_NAME,
} from "./release.js";
import type { ReleaseManifest, ReleaseManifestAsset } from "./release.js";
import { isRecord } from "./types.js";
import { VERSION } from "./version.js";

const RELEASES_API = "https://api.github.com/repos/xenoviz/ruk/releases?per_page=10";
const MAX_BINARY_BYTES = 250 * 1024 * 1024;

type Fetch = typeof fetch;
type Run = typeof run;
type ScheduleWindows = (executable: string, candidate: string, expectedVersion: string) => Promise<void>;
export type Distribution = "package" | "standalone";
export type UpdateInstaller = "bun" | "npm" | "pnpm" | "yarn";

interface ReleaseAsset {
  name: string;
  url: string;
}

interface ReleaseCandidate {
  version: string;
  assets: ReleaseAsset[];
}

interface LatestRelease extends ReleaseCandidate {
  manifest: ReleaseManifest;
}

export interface UpdateReporter {
  stdio: StdioOptions;
}

export interface UpdateOptions {
  distribution: Distribution;
  checkOnly: boolean;
  reporter: UpdateReporter;
  fetchImpl?: Fetch;
  runImpl?: Run;
  platform?: NodeJS.Platform;
  architecture?: string;
  musl?: boolean;
  executable?: string;
  entrypoint?: string;
  installer?: UpdateInstaller;
  scheduleWindowsImpl?: ScheduleWindows;
}

export interface UpdateResult {
  status: "up-to-date" | "update-available" | "updated" | "scheduled";
  currentVersion: string;
  latestVersion: string;
  method: Distribution | UpdateInstaller;
  asset: string | null;
}

function stableVersion(tag: string): string {
  const match = /^v?(\d+\.\d+\.\d+)$/.exec(tag);
  if (!match) throw new Error(`Latest release has unsupported version tag ${tag}`);
  return match[1]!;
}

function parseRelease(value: unknown): ReleaseCandidate {
  if (!isRecord(value) || typeof value["tag_name"] !== "string" || !Array.isArray(value["assets"])) {
    throw new Error("GitHub returned invalid release metadata");
  }
  const assets = value["assets"].map((asset) => {
    if (
      !isRecord(asset) ||
      typeof asset["name"] !== "string" ||
      typeof asset["browser_download_url"] !== "string"
    ) {
      throw new Error("GitHub returned invalid release asset metadata");
    }
    return { name: asset["name"], url: asset["browser_download_url"] };
  });
  return { version: stableVersion(value["tag_name"]), assets };
}

async function request(fetchImpl: Fetch, url: string, accept: string): Promise<Response> {
  const response = await fetchImpl(url, {
    headers: { Accept: accept, "User-Agent": `ruk/${VERSION}` },
    redirect: "follow",
    signal: AbortSignal.timeout(30_000),
  });
  if (!response.ok) {
    throw new Error(`Update request failed (${response.status} ${response.statusText})`);
  }
  return response;
}

function parseManifest(value: unknown, version: string): ReleaseManifest {
  if (
    !isRecord(value) ||
    value["schemaVersion"] !== 1 ||
    value["repository"] !== "xenoviz/ruk" ||
    value["version"] !== version ||
    !isRecord(value["package"]) ||
    value["package"]["name"] !== RUK_PACKAGE_NAME ||
    value["package"]["version"] !== version ||
    !isRecord(value["assets"])
  ) {
    throw new Error(`Release ${version} has an invalid readiness manifest`);
  }
  const assets: Record<string, ReleaseManifestAsset> = {};
  const names = Object.keys(value["assets"]);
  if (
    names.length !== RELEASE_ASSET_NAMES.length ||
    !RELEASE_ASSET_NAMES.every((name) => names.includes(name))
  ) {
    throw new Error(`Release ${version} readiness manifest has an invalid asset set`);
  }
  for (const name of RELEASE_ASSET_NAMES) {
    const asset = value["assets"][name];
    if (
      !isRecord(asset) ||
      typeof asset["sha256"] !== "string" ||
      !/^[a-f0-9]{64}$/.test(asset["sha256"]) ||
      typeof asset["size"] !== "number" ||
      !Number.isSafeInteger(asset["size"]) ||
      asset["size"] <= 0 ||
      asset["size"] > MAX_BINARY_BYTES
    ) {
      throw new Error(`Release ${version} readiness manifest has invalid metadata for ${name}`);
    }
    assets[name] = { sha256: asset["sha256"], size: asset["size"] };
  }
  return {
    schemaVersion: 1,
    repository: "xenoviz/ruk",
    version,
    package: { name: RUK_PACKAGE_NAME, version },
    assets,
  };
}

async function latestReadyRelease(fetchImpl: Fetch): Promise<LatestRelease> {
  const response = await request(fetchImpl, RELEASES_API, "application/vnd.github+json");
  const value: unknown = await response.json();
  if (!Array.isArray(value)) throw new Error("GitHub returned an invalid release list");
  for (const candidate of value) {
    if (!isRecord(candidate) || candidate["draft"] !== false || candidate["prerelease"] !== false) {
      continue;
    }
    let release: ReleaseCandidate;
    try {
      release = parseRelease(candidate);
    } catch {
      continue;
    }
    if (!release.assets.some((asset) => asset.name === "ruk-release.json")) continue;
    const manifestAsset = releaseAsset(release, "ruk-release.json");
    const manifestBytes = await download(fetchImpl, manifestAsset);
    const manifest = parseManifest(
      JSON.parse(new TextDecoder().decode(manifestBytes)) as unknown,
      release.version,
    );
    for (const name of RELEASE_ASSET_NAMES) {
      releaseAsset(release, name);
      releaseAsset(release, `${name}.sha256`);
    }
    return { ...release, manifest };
  }
  throw new Error("No completed Ruk release is available yet");
}

async function detectMusl(): Promise<boolean> {
  const report = process.report?.getReport();
  if (isRecord(report) && isRecord(report["header"])) {
    const glibc = report["header"]["glibcVersionRuntime"];
    if (typeof glibc === "string" && glibc.length > 0) return false;
  }
  try {
    await fs.access("/etc/alpine-release");
    return true;
  } catch {
    return false;
  }
}

export function executableAsset(
  platform: NodeJS.Platform,
  architecture: string,
  musl = false,
): string {
  if (platform === "darwin" && architecture === "x64") return "ruk-macos-x64";
  if (platform === "darwin" && architecture === "arm64") return "ruk-macos-arm64";
  if (platform === "win32" && architecture === "x64") return "ruk-windows-x64.exe";
  if (platform === "win32" && architecture === "arm64") return "ruk-windows-arm64.exe";
  if (platform === "linux" && architecture === "arm64" && !musl) return "ruk-linux-arm64";
  if (platform === "linux" && architecture === "x64") {
    return musl ? "ruk-linux-x64-musl" : "ruk-linux-x64";
  }
  throw new Error(`Standalone updates are not available for ${platform}/${architecture}${musl ? "/musl" : ""}`);
}

function releaseAsset(release: ReleaseCandidate, name: string): ReleaseAsset {
  const asset = release.assets.find((candidate) => candidate.name === name);
  if (!asset) throw new Error(`Release ${release.version} does not contain ${name}`);
  const url = new URL(asset.url);
  const segments = url.pathname.split("/").filter(Boolean).map((segment) => decodeURIComponent(segment));
  const expectedPrefix = ["xenoviz", "ruk", "releases", "download"];
  const prefixMatches = expectedPrefix.every((segment, index) => segments[index] === segment);
  const tag = segments[4];
  const filename = segments[5];
  if (
    url.protocol !== "https:" ||
    url.hostname !== "github.com" ||
    !prefixMatches ||
    segments.length !== 6 ||
    !tag ||
    stableVersion(tag) !== release.version ||
    filename !== name
  ) {
    throw new Error(`Release ${release.version} contains an untrusted URL for ${name}`);
  }
  return asset;
}

async function download(fetchImpl: Fetch, asset: ReleaseAsset): Promise<Uint8Array> {
  const response = await request(fetchImpl, asset.url, "application/octet-stream");
  const declaredLength = Number(response.headers.get("content-length") ?? "0");
  if (Number.isFinite(declaredLength) && declaredLength > MAX_BINARY_BYTES) {
    throw new Error(`${asset.name} exceeds the update size limit`);
  }
  const bytes = new Uint8Array(await response.arrayBuffer());
  if (bytes.byteLength === 0 || bytes.byteLength > MAX_BINARY_BYTES) {
    throw new Error(`${asset.name} has an invalid download size`);
  }
  return bytes;
}

export function installerFromPath(entrypoint: string): UpdateInstaller {
  const normalized = entrypoint.replaceAll("\\", "/").toLowerCase();
  if (normalized.includes("/.bun/install/global/")) return "bun";
  if (normalized.includes("/pnpm/global/") || normalized.includes("/.pnpm/")) return "pnpm";
  if (normalized.includes("/yarn/global/")) return "yarn";
  return "npm";
}

export function parseUpdateInstaller(value: string): UpdateInstaller {
  if (value === "bun" || value === "npm" || value === "pnpm" || value === "yarn") return value;
  throw new Error(`Unsupported update installer ${value}; expected bun, npm, pnpm, or yarn`);
}

export function installerCommand(
  version: string,
  installer: UpdateInstaller,
): { command: string; args: string[] } {
  const specification = `${RUK_PACKAGE_NAME}@${version}`;
  if (installer === "bun") return { command: "bun", args: ["add", "--global", specification] };
  if (installer === "pnpm") return { command: "pnpm", args: ["add", "--global", specification] };
  if (installer === "yarn") return { command: "yarn", args: ["global", "add", specification] };
  return { command: "npm", args: ["install", "--global", specification] };
}

function quoteBatch(value: string): string {
  if (value.includes('"') || /[\r\n]/.test(value)) throw new Error("Executable path cannot be safely updated");
  return `"${value}"`;
}

export function windowsReplacementPlan(
  executable: string,
  candidate: string,
  expectedVersion: string,
  pid = process.pid,
): { helper: string; script: string } {
  const helper = `${candidate}.cmd`;
  const backup = `${executable}.ruk-backup-${pid}`;
  const executableQuoted = quoteBatch(executable);
  const candidateQuoted = quoteBatch(candidate);
  const backupQuoted = quoteBatch(backup);
  const helperQuoted = quoteBatch(helper);
  const script = `@echo off\r\n` +
    `:wait\r\n` +
    `tasklist /FI "PID eq ${pid}" 2>NUL | find "${pid}" >NUL\r\n` +
    `if not errorlevel 1 (ping 127.0.0.1 -n 2 >NUL & goto wait)\r\n` +
    `copy /Y ${executableQuoted} ${backupQuoted} >NUL || goto fail\r\n` +
    `move /Y ${candidateQuoted} ${executableQuoted} >NUL || goto rollback\r\n` +
    `${executableQuoted} --version | findstr /X "${expectedVersion}" >NUL || goto rollback\r\n` +
    `del /Q ${backupQuoted} >NUL 2>NUL\r\n` +
    `del /Q ${helperQuoted} >NUL 2>NUL\r\n` +
    `exit /B 0\r\n` +
    `:rollback\r\n` +
    `move /Y ${backupQuoted} ${executableQuoted} >NUL\r\n` +
    `:fail\r\n` +
    `del /Q ${candidateQuoted} >NUL 2>NUL\r\n` +
    `del /Q ${helperQuoted} >NUL 2>NUL\r\n` +
    `exit /B 1\r\n`;
  return { helper, script };
}

async function scheduleWindowsReplacement(
  executable: string,
  candidate: string,
  expectedVersion: string,
): Promise<void> {
  const { helper, script } = windowsReplacementPlan(executable, candidate, expectedVersion);
  await fs.writeFile(helper, script, { mode: 0o700 });
  const child = spawn("cmd.exe", ["/d", "/s", "/c", helper], {
    detached: true,
    stdio: "ignore",
    windowsHide: true,
  });
  await new Promise<void>((resolve, reject) => {
    child.once("spawn", resolve);
    child.once("error", reject);
  });
  child.unref();
}

async function replacePosixExecutable(
  executable: string,
  candidate: string,
  expectedVersion: string,
  runImpl: Run,
): Promise<void> {
  const metadata = await fs.stat(executable);
  const backup = `${executable}.ruk-backup-${process.pid}`;
  await fs.chmod(candidate, metadata.mode & 0o777);
  await fs.copyFile(executable, backup);
  try {
    await fs.rename(candidate, executable);
    const verified = await runImpl(executable, ["--version"], { allowFailure: true });
    if (verified.code !== 0 || verified.stdout.trim() !== expectedVersion) {
      throw new Error("The replacement executable failed its version check");
    }
  } catch (error) {
    await fs.rename(backup, executable);
    throw error;
  }
  await fs.rm(backup, { force: true });
}

async function updateStandalone(
  release: LatestRelease,
  options: UpdateOptions,
  fetchImpl: Fetch,
  runImpl: Run,
  platform: NodeJS.Platform,
): Promise<UpdateResult> {
  const architecture = options.architecture ?? process.arch;
  const musl = options.musl ?? (platform === "linux" ? await detectMusl() : false);
  const assetName = executableAsset(platform, architecture, musl);
  const asset = releaseAsset(release, assetName);
  const binary = await download(fetchImpl, asset);
  const manifestAsset = release.manifest.assets[assetName];
  if (!manifestAsset || binary.byteLength !== manifestAsset.size) {
    throw new Error(`Release manifest size does not match ${assetName}`);
  }
  const expected = manifestAsset.sha256;
  const actual = crypto.createHash("sha256").update(binary).digest("hex");
  if (!crypto.timingSafeEqual(Buffer.from(actual, "hex"), Buffer.from(expected, "hex"))) {
    throw new Error(`Checksum verification failed for ${assetName}`);
  }

  const executable = path.resolve(options.executable ?? process.execPath);
  const candidate = path.join(path.dirname(executable), `.${path.basename(executable)}.ruk-${release.version}-${process.pid}.new`);
  await fs.writeFile(candidate, binary, { flag: "wx", mode: 0o755 });
  try {
    if (platform === "win32") {
      const schedule = options.scheduleWindowsImpl ?? scheduleWindowsReplacement;
      await schedule(executable, candidate, release.version);
      return {
        status: "scheduled",
        currentVersion: VERSION,
        latestVersion: release.version,
        method: "standalone",
        asset: assetName,
      };
    }
    await replacePosixExecutable(executable, candidate, release.version, runImpl);
  } catch (error) {
    await fs.rm(candidate, { force: true });
    throw error;
  }
  return {
    status: "updated",
    currentVersion: VERSION,
    latestVersion: release.version,
    method: "standalone",
    asset: assetName,
  };
}

export async function updateRuk(options: UpdateOptions): Promise<UpdateResult> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const runImpl = options.runImpl ?? run;
  const environmentInstaller = options.distribution === "package"
    ? process.env["RUK_UPDATE_INSTALLER"]
    : undefined;
  const installer = options.installer ??
    (environmentInstaller ? parseUpdateInstaller(environmentInstaller) : null) ??
    installerFromPath(options.entrypoint ?? process.argv[1] ?? "");
  const platform = options.platform ?? process.platform;
  const musl = options.distribution === "standalone"
    ? options.musl ?? (platform === "linux" ? await detectMusl() : false)
    : false;
  const release = await latestReadyRelease(fetchImpl);
  const method: Distribution | UpdateInstaller = options.distribution === "standalone" ? "standalone" : installer;
  const asset = options.distribution === "standalone"
    ? executableAsset(platform, options.architecture ?? process.arch, musl)
    : null;
  const available = compareVersions(release.version, VERSION) > 0;
  if (!available || options.checkOnly) {
    return {
      status: available ? "update-available" : "up-to-date",
      currentVersion: VERSION,
      latestVersion: release.version,
      method,
      asset,
    };
  }

  if (options.distribution === "standalone") {
    return updateStandalone(release, { ...options, musl }, fetchImpl, runImpl, platform);
  }
  const installation = installerCommand(release.version, installer);
  await runImpl(installation.command, installation.args, { stdio: options.reporter.stdio });
  return {
    status: "updated",
    currentVersion: VERSION,
    latestVersion: release.version,
    method: installer,
    asset: null,
  };
}

export function formatUpdate(result: UpdateResult): string {
  if (result.status === "up-to-date") return `Ruk ${result.currentVersion} is up to date.\n`;
  if (result.status === "update-available") {
    return `Ruk ${result.latestVersion} is available (current ${result.currentVersion}).\n`;
  }
  if (result.status === "scheduled") {
    return `Ruk ${result.latestVersion} is verified and will replace the current executable after this process exits.\n`;
  }
  return `Updated Ruk from ${result.currentVersion} to ${result.latestVersion} using ${result.method}.\n`;
}
