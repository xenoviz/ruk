import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { withDirectoryLock } from "../src/lock.js";

test("directory lock serializes concurrent callbacks and cleans up", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-lock-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const lock = path.join(root, "state.lock");
  const events: string[] = [];

  await Promise.all([
    withDirectoryLock(lock, async () => {
      events.push("first:start");
      await new Promise((resolve) => setTimeout(resolve, 40));
      events.push("first:end");
    }),
    withDirectoryLock(lock, async () => {
      events.push("second:start");
      events.push("second:end");
    }),
  ]);

  const firstEnd = events.indexOf("first:end");
  const secondStart = events.indexOf("second:start");
  assert.ok(firstEnd < secondStart || events.indexOf("second:end") < events.indexOf("first:start"));
  await assert.rejects(fs.access(lock));
});

test("directory lock removes abandoned stale locks", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-stale-lock-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const lock = path.join(root, "state.lock");
  await fs.mkdir(lock, { recursive: true });
  await fs.writeFile(
    path.join(lock, "owner.json"),
    JSON.stringify({ pid: 999_999_999, hostname: os.hostname(), token: "abandoned", createdAt: oldDate() }),
  );
  const old = new Date(Date.now() - 10_000);
  await fs.utimes(lock, old, old);

  let entered = false;
  await withDirectoryLock(lock, async () => {
    entered = true;
  }, { staleMs: 1, timeoutMs: 1_000 });
  assert.equal(entered, true);
});

test("directory lock recovers a stale lock created before owner metadata", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-malformed-lock-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const lock = path.join(root, "state.lock");
  await fs.mkdir(lock);
  const old = new Date(Date.now() - 10_000);
  await fs.utimes(lock, old, old);

  let entered = false;
  await withDirectoryLock(lock, () => { entered = true; }, { staleMs: 1, timeoutMs: 1_000 });
  assert.equal(entered, true);
});

test("concurrent stale-lock recovery preserves serialization", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-stale-lock-race-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const lock = path.join(root, "state.lock");
  await fs.mkdir(lock, { recursive: true });
  await fs.writeFile(
    path.join(lock, "owner.json"),
    JSON.stringify({ pid: 999_999_999, hostname: os.hostname(), token: "abandoned", createdAt: oldDate() }),
  );
  const old = new Date(Date.now() - 10_000);
  await fs.utimes(lock, old, old);

  let active = 0;
  let maximumActive = 0;
  await Promise.all(
    Array.from({ length: 8 }, () =>
      withDirectoryLock(lock, async () => {
        active += 1;
        maximumActive = Math.max(maximumActive, active);
        await new Promise((resolve) => setTimeout(resolve, 20));
        active -= 1;
      }, { staleMs: 1, timeoutMs: 2_000 }),
    ),
  );

  assert.equal(maximumActive, 1);
});

function oldDate(): string {
  return new Date(Date.now() - 10_000).toISOString();
}
