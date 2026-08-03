import fs from "node:fs/promises";
import crypto from "node:crypto";
import os from "node:os";
import path from "node:path";
import { isErrnoException, isRecord } from "./types.js";

interface LockOwner {
  pid: number;
  hostname: string;
  token: string;
  createdAt: string;
}

export interface LockOptions {
  timeoutMs?: number;
  staleMs?: number;
}

const delay = (milliseconds: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, milliseconds));

function isLockOwner(value: unknown): value is LockOwner {
  return (
    isRecord(value) &&
    typeof value["pid"] === "number" &&
    typeof value["hostname"] === "string" &&
    typeof value["token"] === "string" &&
    typeof value["createdAt"] === "string"
  );
}

async function readOwner(lockPath: string): Promise<LockOwner | null> {
  try {
    const owner: unknown = JSON.parse(await fs.readFile(path.join(lockPath, "owner.json"), "utf8"));
    return isLockOwner(owner) ? owner : null;
  } catch (error) {
    if ((isErrnoException(error) && error.code === "ENOENT") || error instanceof SyntaxError) return null;
    throw error;
  }
}

function processIsAlive(pid: number): boolean {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  try {
    process.kill(pid, 0);
    return true;
  } catch (error) {
    return isErrnoException(error) && error.code === "EPERM";
  }
}

async function staleLockOwner(lockPath: string, staleMs: number): Promise<LockOwner | null> {
  const stat = await fs.stat(lockPath);
  if (Date.now() - stat.mtimeMs <= staleMs) return null;
  const owner = await readOwner(lockPath);
  if (!owner || (owner.hostname === os.hostname() && processIsAlive(owner.pid))) return null;
  return owner;
}

export async function withDirectoryLock<T>(
  lockPath: string,
  callback: () => T | Promise<T>,
  options: LockOptions = {},
): Promise<T> {
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
      if (!isErrnoException(error) || error.code !== "EEXIST") throw error;
      let abandoned = false;
      try {
        const owner = await staleLockOwner(lockPath, staleMs);
        if (owner) {
          const identity = crypto.createHash("sha256").update(owner.token).digest("hex").slice(0, 20);
          // ponytail: tombstones prevent delayed contenders from deleting a new lock; add GC if crash churn matters.
          await fs.rename(lockPath, `${lockPath}.abandoned-${identity}`);
          abandoned = true;
        }
      } catch (statError) {
        if (
          isErrnoException(statError) &&
          (statError.code === "ENOENT" || statError.code === "EEXIST" || statError.code === "ENOTEMPTY" || statError.code === "EPERM")
        ) continue;
        throw statError;
      }
      if (abandoned) continue;
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
