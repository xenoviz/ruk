export const RUK_PACKAGE_NAME = "@xenoviz/ruk";
export const RELEASE_ASSET_NAMES = [
  "ruk-linux-x64",
  "ruk-linux-arm64",
  "ruk-linux-x64-musl",
  "ruk-macos-x64",
  "ruk-macos-arm64",
  "ruk-windows-x64.exe",
  "ruk-windows-arm64.exe",
] as const;

export interface ReleaseManifestAsset {
  sha256: string;
  size: number;
}

export interface ReleaseManifest {
  schemaVersion: 1;
  repository: "xenoviz/ruk";
  version: string;
  package: { name: "@xenoviz/ruk"; version: string };
  assets: Record<string, ReleaseManifestAsset>;
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

export function versionIsPrerelease(version: string): boolean {
  return versionParts(version).prerelease.length > 0;
}

export function versionPrereleaseChannel(version: string): string | undefined {
  return versionParts(version).prerelease[0];
}
