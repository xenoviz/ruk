import { compareVersions, versionIsPrerelease, versionPrereleaseChannel } from "./release.js";
import { isRecord } from "./types.js";

export interface PreviousWindowsRelease {
  tagName: string;
  version: string;
}

export type WindowsUpdateVerificationPlan =
  | { kind: "skip"; message: string }
  | { kind: "verify"; previous: PreviousWindowsRelease };

const WINDOWS_UPDATE_ASSETS = ["ruk-release.json", "ruk-windows-x64.exe"] as const;

function hasNamedAsset(value: unknown, name: string): boolean {
  return Array.isArray(value) && value.some(
    (asset) => isRecord(asset) && asset["name"] === name,
  );
}

function parseReadyWindowsRelease(release: unknown, currentTag: string): PreviousWindowsRelease | undefined {
  if (
    !isRecord(release) ||
    release["draft"] !== false ||
    typeof release["prerelease"] !== "boolean" ||
    typeof release["tag_name"] !== "string" ||
    release["tag_name"] === currentTag
  ) {
    return undefined;
  }
  if (WINDOWS_UPDATE_ASSETS.some((name) => !hasNamedAsset(release["assets"], name))) {
    return undefined;
  }
  const version = release["tag_name"].replace(/^v/, "");
  try {
    versionIsPrerelease(version);
  } catch {
    return undefined;
  }
  return { tagName: release["tag_name"], version };
}

function isEligibleUpgradeSource(
  release: unknown,
  candidate: PreviousWindowsRelease,
  currentVersion: string,
): boolean {
  try {
    if (compareVersions(candidate.version, currentVersion) >= 0) return false;
    if (versionIsPrerelease(currentVersion)) {
      return versionPrereleaseChannel(candidate.version) === versionPrereleaseChannel(currentVersion);
    }
    if (!isRecord(release) || release["prerelease"] !== false || versionIsPrerelease(candidate.version)) {
      return false;
    }
    return true;
  } catch {
    return false;
  }
}

export function planWindowsUpdateVerification(
  releases: unknown,
  currentTag: string,
  currentVersion: string,
): WindowsUpdateVerificationPlan {
  if (!Array.isArray(releases)) throw new Error("GitHub returned invalid release metadata");
  let selected: PreviousWindowsRelease | undefined;
  for (const release of releases) {
    const candidate = parseReadyWindowsRelease(release, currentTag);
    if (candidate === undefined || !isEligibleUpgradeSource(release, candidate, currentVersion)) {
      continue;
    }
    if (selected === undefined || compareVersions(candidate.version, selected.version) > 0) {
      selected = candidate;
    }
  }
  if (selected !== undefined) {
    return { kind: "verify", previous: selected };
  }
  if (versionIsPrerelease(currentVersion)) {
    return {
      kind: "skip",
      message: "No prior ready Windows release exists on this prerelease channel; stable installs ignore prereleases and are not an upgrade source.\n",
    };
  }
  return {
    kind: "skip",
    message: "No prior ready Windows release exists; the first release has no upgrade source.\n",
  };
}
