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
} from "../src/lifecycle.js";
import { readState, storePaths } from "../src/state.js";

async function fixture(t: test.TestContext) {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-activity-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const paths = storePaths(root);
  const prepared = await recordPreparingWorkspace(paths, {
    path: path.join(root, "workspace"),
    branch: "agent/activity",
    now: "2026-01-01T00:00:00.000Z",
  });
  const workspace = await markWorkspaceAssigned(paths, prepared.path, prepared.operationId!, {
    owner: "agent",
    hostname: "host",
    expiresAt: "2026-01-01T08:00:00.000Z",
    now: "2026-01-01T00:00:00.000Z",
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
  let finishWork!: () => void;

  await assert.rejects(
    withAssignmentActivity(
      paths,
      assignmentId,
      async () => {
        await finishAssignmentActivity(paths, assignmentId, keeperId);
        await new Promise<void>((resolve) => { finishWork = resolve; });
      },
      {
        heartbeatIntervalMs: 10,
        keeperId,
        onFailure: () => {
          cleaned = true;
          finishWork();
        },
      },
    ),
    /is not active/,
  );

  assert.equal(cleaned, true);
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
