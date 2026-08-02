import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { withDirectoryLock } from "./lock.js";

export function storePaths(commonDir) {
  const root = path.join(commonDir, "ruk");
  return {
    root,
    locks: path.join(root, "locks"),
    state: path.join(root, "state.json"),
    stateLock: path.join(root, "locks", "state.lock"),
  };
}

export function treeLockPath(paths, treePath) {
  return path.join(paths.locks, `workspace-${treeKey(treePath)}.lock`);
}

export function treeKey(treePath) {
  return crypto.createHash("sha256").update(path.resolve(treePath)).digest("hex").slice(0, 20);
}

export async function readState(paths) {
  try {
    const parsed = JSON.parse(await fs.readFile(paths.state, "utf8"));
    if (parsed?.version !== 1 || typeof parsed.trees !== "object" || Array.isArray(parsed.trees)) {
      throw new Error(`Unsupported or invalid Ruk state in ${paths.state}`);
    }
    return parsed;
  } catch (error) {
    if (error?.code === "ENOENT") return { version: 1, trees: {} };
    if (error instanceof SyntaxError) {
      throw new Error(`Cannot parse Ruk state in ${paths.state}: ${error.message}`);
    }
    throw error;
  }
}

export async function updateState(paths, mutate) {
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

export async function setTreeState(paths, treePath, value) {
  return updateState(paths, (state) => {
    state.trees[treeKey(treePath)] = { path: path.resolve(treePath), ...value };
  });
}

export async function deleteTreeState(paths, treePath) {
  return updateState(paths, (state) => {
    delete state.trees[treeKey(treePath)];
  });
}
