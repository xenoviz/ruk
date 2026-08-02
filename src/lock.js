import fs from "node:fs/promises";
import crypto from "node:crypto";
import os from "node:os";
import path from "node:path";

const delay = (milliseconds) => new Promise((resolve) => setTimeout(resolve, milliseconds));

async function readOwner(lockPath) {
  try {
    return JSON.parse(await fs.readFile(path.join(lockPath, "owner.json"), "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT" || error instanceof SyntaxError) return null;
    throw error;
  }
}

function processIsAlive(pid) {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return error?.code === "EPERM";
  }
}

async function staleLockCanBeRemoved(lockPath, staleMs) {
  const stat = await fs.stat(lockPath);
  if (Date.now() - stat.mtimeMs <= staleMs) return false;
  const owner = await readOwner(lockPath);
  return !(owner?.hostname === os.hostname() && processIsAlive(owner.pid));
}

export async function withDirectoryLock(lockPath, callback, options = {}) {
  const timeoutMs = options.timeoutMs ?? 10 * 60 * 1000;
  const staleMs = options.staleMs ?? 30 * 60 * 1000;
  const started = Date.now();
  const token = crypto.randomUUID();
  await fs.mkdir(path.dirname(lockPath), { recursive: true });

  while (true) {
    try {
      await fs.mkdir(lockPath);
      await fs.writeFile(
        path.join(lockPath, "owner.json"),
        JSON.stringify({
          pid: process.pid,
          hostname: os.hostname(),
          token,
          createdAt: new Date().toISOString(),
        }),
        { mode: 0o600 },
      );
      break;
    } catch (error) {
      if (error?.code !== "EEXIST") throw error;
      try {
        if (await staleLockCanBeRemoved(lockPath, staleMs)) {
          await fs.rm(lockPath, { recursive: true, force: true });
          continue;
        }
      } catch (statError) {
        if (statError?.code === "ENOENT") continue;
        throw statError;
      }
      if (Date.now() - started > timeoutMs) {
        throw new Error(`Timed out waiting for lock ${lockPath}`);
      }
      await delay(150 + Math.floor(Math.random() * 150));
    }
  }

  try {
    return await callback();
  } finally {
    const owner = await readOwner(lockPath);
    if (owner?.token === token) {
      await fs.rm(lockPath, { recursive: true, force: true });
    }
  }
}
