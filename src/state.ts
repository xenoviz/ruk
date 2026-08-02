import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { withDirectoryLock } from "./lock.js";
import type { RukState, StorePaths, TreeRecord } from "./types.js";
import { isErrnoException, isRecord } from "./types.js";

export function storePaths(commonDir: string): StorePaths {
  const root = path.join(commonDir, "ruk");
  return {
    root,
    locks: path.join(root, "locks"),
    state: path.join(root, "state.json"),
    stateLock: path.join(root, "locks", "state.lock"),
  };
}

export function treeLockPath(paths: StorePaths, treePath: string): string {
  return path.join(paths.locks, `workspace-${treeKey(treePath)}.lock`);
}

export function treeKey(treePath: string): string {
  return crypto.createHash("sha256").update(path.resolve(treePath)).digest("hex").slice(0, 20);
}

function isTreeRecord(value: unknown): value is TreeRecord {
  return (
    isRecord(value) &&
    typeof value["path"] === "string" &&
    typeof value["fingerprint"] === "string" &&
    typeof value["mode"] === "string" &&
    Array.isArray(value["projections"]) &&
    value["projections"].every((entry) => typeof entry === "string") &&
    typeof value["branch"] === "string" &&
    typeof value["updatedAt"] === "string"
  );
}

function parseState(value: unknown, file: string): RukState {
  if (!isRecord(value) || value["version"] !== 1 || !isRecord(value["trees"])) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  const trees = value["trees"];
  if (!Object.values(trees).every(isTreeRecord)) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  return { version: 1, trees: trees as Record<string, TreeRecord> };
}

export async function readState(paths: StorePaths): Promise<RukState> {
  try {
    return parseState(JSON.parse(await fs.readFile(paths.state, "utf8")) as unknown, paths.state);
  } catch (error) {
    if (isErrnoException(error) && error.code === "ENOENT") return { version: 1, trees: {} };
    if (error instanceof SyntaxError) {
      throw new Error(`Cannot parse Ruk state in ${paths.state}: ${error.message}`);
    }
    throw error;
  }
}

export async function updateState<T>(
  paths: StorePaths,
  mutate: (state: RukState) => T | Promise<T>,
): Promise<T> {
  return withDirectoryLock(paths.stateLock, async () => {
    await fs.mkdir(paths.root, { recursive: true });
    const state = await readState(paths);
    const result = await mutate(state);
    const temporary = `${paths.state}.${process.pid}.tmp`;
    await fs.writeFile(temporary, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
    await fs.rename(temporary, paths.state);
    return result;
  });
}

export async function setTreeState(
  paths: StorePaths,
  treePath: string,
  value: Omit<TreeRecord, "path">,
): Promise<void> {
  return updateState(paths, (state) => {
    state.trees[treeKey(treePath)] = { path: path.resolve(treePath), ...value };
  });
}

export async function deleteTreeState(paths: StorePaths, treePath: string): Promise<void> {
  return updateState(paths, (state) => {
    delete state.trees[treeKey(treePath)];
  });
}
