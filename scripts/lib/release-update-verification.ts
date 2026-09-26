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

// Updaters before this version cannot upgrade, and published releases cannot
// be fixed, so they are not used as upgrade sources. Before 0.4.1 the Windows
// replacement helper paused with `timeout`, which exits immediately without
// console input. Through 0.5.0 release discovery rejected GitHub's pagination
// links, which name the repository by numeric ID, so `ruk update` failed once
// the eleventh release made GitHub paginate.
export const FIRST_WORKING_WINDOWS_UPDATER = "0.5.1";

function isEligibleUpgradeSource(
  release: unknown,
  candidate: PreviousWindowsRelease,
  currentVersion: string,
): boolean {
  try {
    if (compareVersions(candidate.version, currentVersion) >= 0) return false;
    if (compareVersions(candidate.version, FIRST_WORKING_WINDOWS_UPDATER) < 0) return false;
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
    message: `No prior ready Windows release at or after ${FIRST_WORKING_WINDOWS_UPDATER} exists to exercise as an upgrade source.\n`,
  };
}
