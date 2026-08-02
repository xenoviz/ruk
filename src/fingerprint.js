import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { dependencyFiles } from "./git.js";
import { packageManagerVersion } from "./config.js";

export async function dependencyFingerprint({ root, manager }) {
  const hash = crypto.createHash("sha256");
  const files = await dependencyFiles(root);
  const version = await packageManagerVersion(manager, root);

  hash.update("ruk-fingerprint-v2\0");
  hash.update(`${process.platform}\0${process.arch}\0`);
  hash.update(`${process.versions.node}\0${process.versions.modules ?? "unknown"}\0`);
  hash.update(
    `${manager.name}\0${version}\0${manager.dependencyMode ?? "managed"}\0${JSON.stringify(manager.command)}\0`,
  );

  for (const relative of files) {
    hash.update(relative.replaceAll("\\", "/"));
    hash.update("\0");
    try {
      const content = await fs.readFile(path.join(root, relative));
      hash.update(String(content.byteLength));
      hash.update("\0");
      hash.update(content);
    } catch (error) {
      if (error?.code !== "ENOENT") throw error;
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
