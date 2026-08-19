import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { RELEASE_ASSET_NAMES } from "./lib/release.js";
import type { ReleaseManifest } from "./lib/release.js";

export function checksumFromFile(content: string, assetName: string): string {
  for (const line of content.split(/\r?\n/)) {
    const match = /^([a-fA-F0-9]{64})\s+\*?(.+)$/.exec(line.trim());
    const filename = match?.[2]?.split(/[\\/]/).pop();
    if (match && filename === assetName) return match[1]!.toLowerCase();
  }
  throw new Error(`Checksum file does not contain ${assetName}`);
}

export async function createReleaseManifest(
  artifacts: string,
  version: string,
): Promise<ReleaseManifest> {
  const expectedFiles = new Set(
    RELEASE_ASSET_NAMES.flatMap((name) => [name, `${name}.sha256`]),
  );
  const files = await fs.readdir(artifacts);
  for (const file of files) {
    if (!expectedFiles.has(file)) throw new Error(`Unexpected staged release file ${file}`);
  }
  for (const file of expectedFiles) {
    if (!files.includes(file)) throw new Error(`Missing staged release file ${file}`);
  }

  const manifestAssets: Record<string, { sha256: string; size: number }> = {};
  for (const name of RELEASE_ASSET_NAMES) {
    const binary = await fs.readFile(path.join(artifacts, name));
    const checksum = checksumFromFile(
      await fs.readFile(path.join(artifacts, `${name}.sha256`), "utf8"),
      name,
    );
    const actual = crypto.createHash("sha256").update(binary).digest("hex");
    if (actual !== checksum) throw new Error(`Staged checksum does not match ${name}`);
    manifestAssets[name] = { sha256: checksum, size: binary.byteLength };
  }

  return {
    schemaVersion: 1,
    repository: "xenoviz/ruk",
    version,
    package: { name: "@xenoviz/ruk", version },
    assets: manifestAssets,
  };
}
