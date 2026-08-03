import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { spawn } from "node:child_process";
import type { StdioOptions } from "node:child_process";
import { run } from "./process.js";
import { isRecord } from "./types.js";
import { VERSION } from "./version.js";

const RELEASE_API = "https://api.github.com/repos/xenoviz/ruk/releases/latest";
const PACKAGE_NAME = "@xenoviz/ruk";
const MAX_BINARY_BYTES = 250 * 1024 * 1024;

type Fetch = typeof fetch;
type Run = typeof run;
export type Distribution = "package" | "standalone";
export type UpdateInstaller = "bun" | "npm" | "pnpm" | "yarn";

interface ReleaseAsset {
  name: string;
  url: string;
}

interface LatestRelease {
  version: string;
  assets: ReleaseAsset[];
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

interface ParsedVersion {
  core: readonly [number, number, number];
  prerelease: string[];
}

function versionParts(version: string): ParsedVersion {
  const match = /^(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?$/.exec(version);
  if (!match) throw new Error(`Unsupported version ${version}`);
  const core = [Number(match[1]), Number(match[2]), Number(match[3])] as const;
  if (!core.every(Number.isSafeInteger)) throw new Error(`Unsupported version ${version}`);
  return { core, prerelease: match[4]?.split(".") ?? [] };
}

function comparePrerelease(left: readonly string[], right: readonly string[]): number {
  if (left.length === 0 || right.length === 0) return left.length === right.length ? 0 : left.length === 0 ? 1 : -1;
  const length = Math.max(left.length, right.length);
  for (let index = 0; index < length; index += 1) {
    const leftPart = left[index];
    const rightPart = right[index];
    if (leftPart === undefined || rightPart === undefined) return leftPart === undefined ? -1 : 1;
    if (leftPart === rightPart) continue;
    const leftNumeric = /^\d+$/.test(leftPart);
    const rightNumeric = /^\d+$/.test(rightPart);
    if (leftNumeric && rightNumeric) {
      const leftNumber = BigInt(leftPart);
      const rightNumber = BigInt(rightPart);
      if (leftNumber === rightNumber) continue;
      return leftNumber < rightNumber ? -1 : 1;
    }
    if (leftNumeric !== rightNumeric) return leftNumeric ? -1 : 1;
    return leftPart < rightPart ? -1 : 1;
  }
  return 0;
}

export function compareVersions(left: string, right: string): number {
  const leftParts = versionParts(left);
  const rightParts = versionParts(right);
  for (let index = 0; index < leftParts.core.length; index += 1) {
    const difference = leftParts.core[index]! - rightParts.core[index]!;
    if (difference !== 0) return Math.sign(difference);
  }
  return comparePrerelease(leftParts.prerelease, rightParts.prerelease);
}

function parseRelease(value: unknown): LatestRelease {
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

async function latestRelease(fetchImpl: Fetch): Promise<LatestRelease> {
  const response = await request(fetchImpl, RELEASE_API, "application/vnd.github+json");
  return parseRelease(await response.json() as unknown);
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

function releaseAsset(release: LatestRelease, name: string): ReleaseAsset {
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

export function checksumFromFile(content: string, assetName: string): string {
  for (const line of content.split(/\r?\n/)) {
    const match = /^([a-fA-F0-9]{64})\s+\*?(.+)$/.exec(line.trim());
    const filename = match?.[2]?.split(/[\\/]/).pop();
    if (match && filename === assetName) return match[1]!.toLowerCase();
  }
  throw new Error(`Checksum file does not contain ${assetName}`);
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

export function installerCommand(
  version: string,
  installer: UpdateInstaller,
): { command: string; args: string[] } {
  const specification = `${PACKAGE_NAME}@${version}`;
  if (installer === "bun") return { command: "bun", args: ["add", "--global", specification] };
  if (installer === "pnpm") return { command: "pnpm", args: ["add", "--global", specification] };
  if (installer === "yarn") return { command: "yarn", args: ["global", "add", specification] };
  return { command: "npm", args: ["install", "--global", specification] };
}

function quoteBatch(value: string): string {
  if (value.includes('"') || /[\r\n]/.test(value)) throw new Error("Executable path cannot be safely updated");
  return `"${value}"`;
}

async function scheduleWindowsReplacement(
  executable: string,
  candidate: string,
  expectedVersion: string,
): Promise<void> {
  const helper = `${candidate}.cmd`;
  const backup = `${executable}.ruk-backup-${process.pid}`;
  const executableQuoted = quoteBatch(executable);
  const candidateQuoted = quoteBatch(candidate);
  const backupQuoted = quoteBatch(backup);
  const helperQuoted = quoteBatch(helper);
  const script = `@echo off\r\n` +
    `:wait\r\n` +
    `tasklist /FI "PID eq ${process.pid}" 2>NUL | find "${process.pid}" >NUL\r\n` +
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
  const checksumAsset = releaseAsset(release, `${assetName}.sha256`);
  const [binary, checksumBytes] = await Promise.all([
    download(fetchImpl, asset),
    download(fetchImpl, checksumAsset),
  ]);
  const expected = checksumFromFile(new TextDecoder().decode(checksumBytes), assetName);
  const actual = crypto.createHash("sha256").update(binary).digest("hex");
  if (!crypto.timingSafeEqual(Buffer.from(actual, "hex"), Buffer.from(expected, "hex"))) {
    throw new Error(`Checksum verification failed for ${assetName}`);
  }

  const executable = path.resolve(options.executable ?? process.execPath);
  const candidate = path.join(path.dirname(executable), `.${path.basename(executable)}.ruk-${release.version}-${process.pid}.new`);
  await fs.writeFile(candidate, binary, { flag: "wx", mode: 0o755 });
  try {
    if (platform === "win32") {
      await scheduleWindowsReplacement(executable, candidate, release.version);
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
  const release = await latestRelease(fetchImpl);
  const platform = options.platform ?? process.platform;
  const musl = options.musl ?? (platform === "linux" ? await detectMusl() : false);
  const installer = options.installer ?? installerFromPath(options.entrypoint ?? process.argv[1] ?? "");
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
