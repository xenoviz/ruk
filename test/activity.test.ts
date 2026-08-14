import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  activityHeartbeatInterval,
  withAssignmentActivity,
} from "../src/activity.js";
import {
  finishAssignmentActivity,
  markWorkspaceAssigned,
  recordPreparingWorkspace,
  refreshAssignmentActivity,
  renewAssignment,
} from "../src/lifecycle.js";
import { withDirectoryLock } from "../src/lock.js";
import {
  containsProcessIdentityUnavailableError,
  ProcessIdentityUnavailableError,
} from "../src/process.js";
import { readState, storePaths } from "../src/state.js";

async function fixture(t: test.TestContext, leaseDurationMinutes = 480) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-activity-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const paths = storePaths(root);
  const prepared = await recordPreparingWorkspace(paths, {
    path: path.join(root, "workspace"),
    branch: "agent/activity",
    now: "2026-01-01T00:00:00.000Z",
  });
  const assignedAt = "2026-01-01T00:00:00.000Z";
  const workspace = await markWorkspaceAssigned(paths, prepared.path, prepared.operationId!, {
    owner: "agent",
    hostname: "host",
    expiresAt: new Date(Date.parse(assignedAt) + leaseDurationMinutes * 60_000).toISOString(),
    now: assignedAt,
  });
  return { paths, assignmentId: workspace.assignment!.id };
}

test("activity heartbeat interval is one third of the lease capped at five minutes", () => {
  assert.equal(activityHeartbeatInterval(6), 120_000);
  assert.equal(activityHeartbeatInterval(60), 300_000);
  assert.equal(activityHeartbeatInterval(0.001), 20);
});

test("withAssignmentActivity registers and removes its fenced keeper", async (t) => {
  const { paths, assignmentId } = await fixture(t);
  const keeperId = "6748518b-7f5e-4cce-8625-91b0c81a93b3";

  const value = await withAssignmentActivity(
    paths,
    assignmentId,
    async () => {
      const workspace = Object.values((await readState(paths)).workspaces)[0]!;
      assert.deepEqual(workspace.assignment!.leaseKeepers.map(({ id }) => id), [keeperId]);
      return 42;
    },
    { keeperId },
  );

  assert.equal(value, 42);
  const workspace = Object.values((await readState(paths)).workspaces)[0]!;
  assert.deepEqual(workspace.assignment!.leaseKeepers, []);
});

test("withAssignmentActivity timestamps its initial keeper after acquiring the state lock", async (t) => {
  const { paths, assignmentId } = await fixture(t, 0.001);
  let releaseLock!: () => void;
  let lockAcquired!: () => void;
  const acquired = new Promise<void>((resolve) => { lockAcquired = resolve; });
  const hold = new Promise<void>((resolve) => { releaseLock = resolve; });
  const lock = withDirectoryLock(paths.stateLock, async () => {
    lockAcquired();
    await hold;
  });
  await acquired;

  const active = withAssignmentActivity(
    paths,
    assignmentId,
    async () => {
      const workspace = Object.values((await readState(paths)).workspaces)[0]!;
      const keeper = workspace.assignment!.leaseKeepers[0]!;
      assert.ok(Date.parse(keeper.validUntil) > Date.now());
    },
    { heartbeatIntervalMs: 20 },
  );
  await new Promise((resolve) => setTimeout(resolve, 60));
  releaseLock();
  await lock;
  await active;
});

test("withAssignmentActivity refreshes the lease while work continues", async (t) => {
  const { paths, assignmentId } = await fixture(t);
  const startedAt = Date.now();

  await withAssignmentActivity(
    paths,
    assignmentId,
    async () => new Promise<void>((resolve) => setTimeout(resolve, 45)),
    { heartbeatIntervalMs: 10 },
  );

  const workspace = Object.values((await readState(paths)).workspaces)[0]!;
  assert.ok(Date.parse(workspace.assignment!.lastActivityAt) >= startedAt);
  assert.deepEqual(workspace.assignment!.leaseKeepers, []);
});

test("withAssignmentActivity reports a lost keeper and invokes failure cleanup", async (t) => {
  const { paths, assignmentId } = await fixture(t);
  const keeperId = "583b29a4-a6dc-4856-b4c4-e784b04f45c7";
  let cleaned = false;

  await assert.rejects(
    withAssignmentActivity(
      paths,
      assignmentId,
      async (signal) => {
        await finishAssignmentActivity(paths, assignmentId, keeperId);
        await new Promise<void>((resolve) => signal.addEventListener("abort", () => {
          cleaned = true;
          resolve();
        }, { once: true }));
      },
      {
        heartbeatIntervalMs: 10,
        keeperId,
      },
    ),
    /is not active/,
  );

  assert.equal(cleaned, true);
});

test("withAssignmentActivity preserves a failed abort cleanup", async (t) => {
  const { paths, assignmentId } = await fixture(t);

  await assert.rejects(
    withAssignmentActivity(
      paths,
      assignmentId,
      async (signal) => {
        await new Promise<void>((resolve) => signal.addEventListener("abort", () => resolve(), { once: true }));
        throw new ProcessIdentityUnavailableError(42);
      },
      {
        heartbeatIntervalMs: 10,
        retryAttempts: 0,
        refresh: async () => { throw new Error("heartbeat write failed"); },
      },
    ),
    (error: unknown) => {
      assert.ok(error instanceof AggregateError);
      assert.equal(containsProcessIdentityUnavailableError(error), true);
      assert.match(error.message, /operation cleanup failed/);
      return true;
    },
  );
});

test("withAssignmentActivity retries transient heartbeat writes before failing work", async (t) => {
  const { paths, assignmentId } = await fixture(t);
  let attempts = 0;
  let finishWork!: () => void;

  await withAssignmentActivity(
    paths,
    assignmentId,
    async () => new Promise<void>((resolve) => { finishWork = resolve; }),
    {
      heartbeatIntervalMs: 10,
      retryAttempts: 2,
      retryDelayMs: 1,
      refresh: async (activityPaths, currentAssignmentId, input) => {
        attempts += 1;
        if (attempts < 3) throw new Error("EPERM: transient state replacement failure");
        const result = await refreshAssignmentActivity(activityPaths, currentAssignmentId, input);
        finishWork();
        return result;
      },
    },
  );

  assert.equal(attempts, 3);
});

test("withAssignmentActivity adopts a shorter duration after explicit renewal", async (t) => {
  const { paths, assignmentId } = await fixture(t, 0.003);
  const refreshTimes: number[] = [];
  let finishWork!: () => void;

  await withAssignmentActivity(
    paths,
    assignmentId,
    async () => {
      const renewedAt = new Date().toISOString();
      await renewAssignment(
        paths,
        assignmentId,
        new Date(Date.parse(renewedAt) + 0.0003 * 60_000).toISOString(),
        renewedAt,
      );
      await new Promise<void>((resolve) => { finishWork = resolve; });
    },
    {
      refresh: async (activityPaths, currentAssignmentId, input) => {
        const result = await refreshAssignmentActivity(activityPaths, currentAssignmentId, input);
        refreshTimes.push(Date.now());
        if (refreshTimes.length === 2) finishWork();
        return result;
      },
    },
  );

  assert.equal(refreshTimes.length, 2);
  assert.ok(refreshTimes[1]! - refreshTimes[0]! < 40);
});
