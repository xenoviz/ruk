import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { dependencyFiles } from "./git.js";
import { packageManagerVersion } from "./config.js";
import type { FingerprintDetails, PackageManager } from "./types.js";
import { isErrnoException } from "./types.js";

export async function dependencyFingerprint({
  root,
  manager,
}: {
  root: string;
  manager: PackageManager;
}): Promise<FingerprintDetails> {
  const hash = crypto.createHash("sha256");
  const files = await dependencyFiles(root);
  const version = await packageManagerVersion(manager, root);

  hash.update("ruk-fingerprint-v2\0");
  hash.update(`${process.platform}\0${process.arch}\0`);
  const bunVersion = (process.versions as NodeJS.ProcessVersions & { bun?: string }).bun ?? "not-bun";
  hash.update(
    `${process.versions.node}\0${process.versions.modules ?? "unknown"}\0${bunVersion}\0`,
  );
  hash.update(
    `${manager.name}\0${version}\0${manager.dependencyMode ?? "managed"}\0${JSON.stringify(manager.command)}\0`,
  );

  for (const relative of files) {
    hash.update(relative.replaceAll("\\", "/"));
    hash.update("\0");
    try {
      const content = await fs.readFile(path.join(root, relative));
      const normalized = path.posix.basename(relative.replaceAll("\\", "/")) === "bun.lockb"
        ? content
        : Buffer.from(content.toString("utf8").replaceAll("\r\n", "\n"));
      hash.update(String(normalized.byteLength));
      hash.update("\0");
      hash.update(normalized);
    } catch (error) {
      if (!isErrnoException(error) || error.code !== "ENOENT") throw error;
      hash.update("missing");
    }
    hash.update("\0");
  }

  return {
    fingerprint: hash.digest("hex"),
    files,
    manager: {
      name: manager.name,
      version,
      command: manager.command,
      dependencyMode: manager.dependencyMode ?? "managed",
    },
  };
}
