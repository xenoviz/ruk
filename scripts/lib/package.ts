import fs from "node:fs/promises";
import path from "node:path";
import { isRecord } from "./types.js";

export const VERSION_PATTERN = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/;

export async function readPackageJson(root: string): Promise<Record<string, unknown>> {
  const value: unknown = JSON.parse(await fs.readFile(path.join(root, "package.json"), "utf8"));
  if (!isRecord(value)) throw new Error("package.json must contain an object");
  return value;
}

export async function readPackageVersion(root: string): Promise<string> {
  const packageJSON = await readPackageJson(root);
  const version = packageJSON["version"];
  if (typeof version !== "string" || !VERSION_PATTERN.test(version)) {
    throw new Error(`Invalid package version ${String(version)}`);
  }
  return version;
}
