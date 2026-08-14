import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  addAssignmentProcess,
  allocateAssignmentPorts,
  assignmentIsAutoRenewing,
  beginAssignmentActivity,
  beginWorkspaceCollection,
  beginWorkspaceReturn,
  cancelWorkspaceCollection,
  cancelWorkspaceReturn,
  deleteWorkspaceRecord,
  findAssignments,
  finishWorkspaceReturn,
  finishAssignmentActivity,
  identifyGcCandidates,
  markWorkspaceAssigned,
  markWorkspaceAvailable,
  markWorkspaceFailed,
  recordPreparingWorkspace,
  recordSuccessfulAcquisition,
  refreshAssignmentActivity,
  removeAssignmentProcess,
  renewAssignment,
  reserveAvailableWorkspace,
} from "../src/lifecycle.js";
import { withDirectoryLock } from "../src/lock.js";
import { primaryCheckoutLockPath, readState, storePaths, treeKey } from "../src/state.js";
import type { StorePaths, WorkspaceRecord } from "../src/types.js";

const T0 = "2026-01-01T00:00:00.000Z";
const T1 = "2026-01-01T01:00:00.000Z";
const T2 = "2026-01-01T02:00:00.000Z";
const T3 = "2026-01-01T03:00:00.000Z";
const T4 = "2026-01-01T04:00:00.000Z";
const T5 = "2026-01-01T05:00:00.000Z";

async function fixture(t: test.TestContext): Promise<{ root: string; paths: StorePaths }> {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-lifecycle-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  return { root, paths: storePaths(root) };
}

async function prepare(
  paths: StorePaths,
  workspacePath: string,
  now = T0,
): Promise<WorkspaceRecord> {
  return recordPreparingWorkspace(paths, { path: workspacePath, branch: "agent/test", now });
}

async function assign(
  paths: StorePaths,
  workspacePath: string,
  now = T0,
  expiresAt = T2,
): Promise<WorkspaceRecord> {
  const preparing = await prepare(paths, workspacePath, now);
  const assigned = await markWorkspaceAssigned(paths, workspacePath, preparing.operationId!, {
    owner: "agent",
    hostname: "host",
    expiresAt,
    now,
  });
  return recordSuccessfulAcquisition(paths, assigned.assignment!.id, assigned.operationId!, false, now);
}

async function makeAvailable(
  paths: StorePaths,
  workspacePath: string,
  availableAt = T1,
): Promise<WorkspaceRecord> {
  const assigned = await assign(paths, workspacePath);
  await beginWorkspaceReturn(paths, assigned.assignment!.id, availableAt);
  return finishWorkspaceReturn(paths, assigned.assignment!.id, availableAt);
}

test("preparation and assignment finalizers are fenced by immutable IDs", async (t) => {
  const { root, paths } = await fixture(t);
  const workspacePath = path.join(root, "workspace");
  const preparing = await prepare(paths, workspacePath);
  await assert.rejects(
    markWorkspaceAssigned(paths, workspacePath, crypto.randomUUID(), {
      owner: "agent",
      hostname: "host",
      expiresAt: T2,
      now: T0,
    }),
    /operation does not match/,
  );
  const first = await markWorkspaceAssigned(paths, workspacePath, preparing.operationId!, {
    owner: "agent",
    hostname: "host",
    expiresAt: T2,
    now: T0,
  });
  assert.match(first.assignment!.id, /^[0-9a-f-]{36}$/i);
  await assert.rejects(beginWorkspaceReturn(paths, first.assignment!.id, T1), /acquisition is still in progress/);
  await assert.rejects(
    beginWorkspaceReturn(paths, first.assignment!.id, T1, undefined, crypto.randomUUID()),
    /acquisition is still in progress/,
  );
  await assert.rejects(
    beginWorkspaceReturn(paths, first.assignment!.id, T1, undefined, first.operationId!, T1),
    /changed before collection/,
  );
  await beginWorkspaceReturn(paths, first.assignment!.id, T1, undefined, first.operationId!, T0);
  await finishWorkspaceReturn(paths, first.assignment!.id, T1);
  const second = await reserveAvailableWorkspace(paths, {
    owner: "other",
    hostname: "host",
    expiresAt: T3,
    branch: "agent/reused",
    now: T2,
  });
  assert.notEqual(second!.assignment!.id, first.assignment!.id);
  assert.equal(second!.branch, "agent/reused");
  await assert.rejects(beginWorkspaceReturn(paths, first.assignment!.id, T3), /does not exist/);
});

test("renewal requires the exact active assignment", async (t) => {
  const { root, paths } = await fixture(t);
  const workspace = await assign(paths, path.join(root, "workspace"));
  const renewed = await renewAssignment(paths, workspace.assignment!.id, T3, T1);
  assert.equal(renewed.assignment!.assignedAt, T0);
  assert.equal(renewed.assignment!.renewedAt, T1);
  assert.equal(renewed.assignment!.expiresAt, T3);
  const preserved = await renewAssignment(paths, workspace.assignment!.id, T2, T0, T0);
  assert.equal(preserved.assignment!.renewedAt, T1);
  assert.equal(preserved.assignment!.expiresAt, T3);
  await assert.rejects(renewAssignment(paths, crypto.randomUUID(), T3, T1), /does not exist/);
  await assert.rejects(
    beginWorkspaceReturn(paths, workspace.assignment!.id, T2, undefined, crypto.randomUUID()),
    /acquisition operation does not match/,
  );
  await assert.rejects(beginWorkspaceReturn(paths, workspace.assignment!.id, T2, T2), /renewed before collection/);
  assert.equal((await readState(paths)).workspaces[treeKey(workspace.path)]!.lifecycle, "assigned");
});

test("assignment activity renews the lease and fences concurrent keepers", async (t) => {
  const { root, paths } = await fixture(t);
  const workspace = await assign(paths, path.join(root, "workspace"));
  const assignmentId = workspace.assignment!.id;
  const firstKeeper = "00000000-0000-4000-8000-000000000001";
  const secondKeeper = "00000000-0000-4000-8000-000000000002";

  const first = await beginAssignmentActivity(paths, assignmentId, {
    keeperId: firstKeeper,
    validUntil: T3,
    now: T1,
  });
  assert.equal(first.assignment!.lastActivityAt, T1);
  assert.equal(first.assignment!.expiresAt, T3);
  assert.equal(first.assignment!.leaseKeepers.length, 1);
  assert.equal(assignmentIsAutoRenewing(first.assignment!, T1), true);

  const second = await beginAssignmentActivity(paths, assignmentId, {
    keeperId: secondKeeper,
    validUntil: T4,
    now: T2,
  });
  assert.equal(second.assignment!.expiresAt, T4);
  assert.equal(second.assignment!.leaseKeepers.length, 2);

  const refreshed = await refreshAssignmentActivity(paths, assignmentId, {
    keeperId: firstKeeper,
    validUntil: T5,
    now: T3,
  });
  assert.equal(refreshed.assignment!.expiresAt, T5);
  assert.equal(refreshed.assignment!.leaseKeepers.find(({ id }) => id === firstKeeper)?.heartbeatAt, T3);

  const oneRemaining = await finishAssignmentActivity(paths, assignmentId, firstKeeper, T3);
  assert.deepEqual(oneRemaining.assignment!.leaseKeepers.map(({ id }) => id), [secondKeeper]);
  assert.equal(assignmentIsAutoRenewing(oneRemaining.assignment!, T3), true);
  assert.equal(assignmentIsAutoRenewing(oneRemaining.assignment!, T4), false);

  const finished = await finishAssignmentActivity(paths, assignmentId, secondKeeper, T3);
  assert.deepEqual(finished.assignment!.leaseKeepers, []);
  assert.equal(finished.assignment!.expiresAt, T5);
  await assert.rejects(
    finishAssignmentActivity(paths, crypto.randomUUID(), secondKeeper, T3),
    /does not exist/,
  );
});

test("a delayed activity refresh cannot shorten a newer explicit renewal", async (t) => {
  const { root, paths } = await fixture(t);
  const workspace = await assign(paths, path.join(root, "workspace"));
  const assignmentId = workspace.assignment!.id;
  const keeperId = "00000000-0000-4000-8000-000000000006";

  await beginAssignmentActivity(paths, assignmentId, {
    keeperId,
    validUntil: T3,
    now: T1,
  });
  await renewAssignment(paths, assignmentId, T5, T4);
  const refreshed = await refreshAssignmentActivity(paths, assignmentId, {
    keeperId,
    validUntil: T3,
    now: T2,
  });

  assert.equal(refreshed.assignment!.renewedAt, T4);
  assert.equal(refreshed.assignment!.lastActivityAt, T4);
  assert.equal(refreshed.assignment!.expiresAt, T5);
  assert.deepEqual(
    refreshed.assignment!.leaseKeepers.find(({ id }) => id === keeperId),
    { id: keeperId, heartbeatAt: T4, validUntil: T5 },
  );
});

test("forced GC does not select an expired assignment with a current keeper", async (t) => {
  const { root, paths } = await fixture(t);
  const workspace = await assign(paths, path.join(root, "workspace"), T0, T1);
  await beginAssignmentActivity(paths, workspace.assignment!.id, {
    keeperId: "00000000-0000-4000-8000-000000000003",
    validUntil: T3,
    now: T0,
  });

  assert.equal((await identifyGcCandidates(paths, T0, T2, true)).length, 0);
  assert.deepEqual(
    (await identifyGcCandidates(paths, T0, T4, true)).map(({ reason }) => reason),
    ["expired-assignment"],
  );
});

test("return transitions retain ownership until tracked processes are removed", async (t) => {
  const { root, paths } = await fixture(t);
  const assigned = await assign(paths, path.join(root, "workspace"));
  const assignmentId = assigned.assignment!.id;
  await addAssignmentProcess(
    paths,
    assignmentId,
    { pid: 42, terminalId: "ttys001", command: ["node", "server.js"], startedAt: "process-identity" },
    T1,
  );
  const returning = await beginWorkspaceReturn(paths, assignmentId, T1);
  assert.equal((await beginWorkspaceReturn(paths, assignmentId, T1)).lifecycle, "returning");
  assert.equal(returning.assignment!.id, assignmentId);
  assert.equal(returning.processes.length, 1);
  await assert.rejects(finishWorkspaceReturn(paths, assignmentId, T2), /tracked processes/);
  const cancelled = await cancelWorkspaceReturn(paths, assignmentId, "kill failed", T2);
  assert.equal(cancelled.lifecycle, "assigned");
  assert.equal(cancelled.failure, "kill failed");
  await beginWorkspaceReturn(paths, assignmentId, T2);
  await assert.rejects(
    removeAssignmentProcess(paths, assignmentId, 42, "reused-pid", T2),
    /is not tracked/,
  );
  await removeAssignmentProcess(paths, assignmentId, 42, "process-identity", T2);
  const available = await finishWorkspaceReturn(paths, assignmentId, T3);
  assert.equal(available.lifecycle, "available");
  assert.equal(available.assignment, null);
});

test("failed return restores an interrupted acquisition marker", async (t) => {
  const { root, paths } = await fixture(t);
  const workspacePath = path.join(root, "workspace");
  const preparing = await prepare(paths, workspacePath);
  const assigned = await markWorkspaceAssigned(paths, workspacePath, preparing.operationId!, {
    owner: "agent",
    hostname: "host",
    expiresAt: T2,
    now: T0,
  });

  await assert.rejects(
    beginWorkspaceReturn(paths, assigned.assignment!.id, T1),
    /acquisition is still in progress/,
  );
  const returning = await beginWorkspaceReturn(
    paths,
    assigned.assignment!.id,
    T1,
    undefined,
    assigned.operationId!,
  );
  assert.equal(returning.operationId, assigned.operationId);
  const cancelled = await cancelWorkspaceReturn(paths, assigned.assignment!.id, "cleanup failed", T2);
  assert.equal(cancelled.operationId, assigned.operationId);
  assert.equal(
    (await identifyGcCandidates(paths, T2, T2, true)).some(({ reason }) => reason === "abandoned-acquisition"),
    true,
  );

  await beginWorkspaceReturn(paths, assigned.assignment!.id, T2, undefined, assigned.operationId!);
  const available = await finishWorkspaceReturn(paths, assigned.assignment!.id, T3);
  assert.equal(available.operationId, null);
});

test("only one concurrent caller reserves an available workspace", async (t) => {
  const { root, paths } = await fixture(t);
  await makeAvailable(paths, path.join(root, "workspace"));
  let blockedReservation!: ReturnType<typeof reserveAvailableWorkspace>;
  await withDirectoryLock(path.join(paths.root, "warm.lock"), async () => {
    blockedReservation = reserveAvailableWorkspace(paths, {
      owner: "blocked",
      hostname: "host",
      expiresAt: T3,
      now: T2,
    });
    await new Promise((resolve) => setTimeout(resolve, 300));
    assert.equal(Object.values((await readState(paths)).workspaces)[0]!.lifecycle, "available");
  });
  const reserved = (await blockedReservation)!;
  await beginWorkspaceReturn(paths, reserved.assignment!.id, T2, undefined, reserved.operationId!);
  await finishWorkspaceReturn(paths, (await blockedReservation)!.assignment!.id, T2);
  const results = await Promise.all(
    ["one", "two"].map((owner) =>
      reserveAvailableWorkspace(paths, { owner, hostname: "host", expiresAt: T3, now: T2 }),
    ),
  );
  assert.equal(results.filter(Boolean).length, 1);
  assert.equal((await findAssignments(paths, { now: T2 })).length, 1);
});

test("assignment publication waits for the primary-checkout task fence", async (t) => {
  const { root, paths } = await fixture(t);
  const available = await makeAvailable(paths, path.join(root, "available"));
  const preparing = await prepare(paths, path.join(root, "preparing"));
  let releaseFence!: () => void;
  let fenceReady!: () => void;
  const held = new Promise<void>((resolve) => { releaseFence = resolve; });
  const ready = new Promise<void>((resolve) => { fenceReady = resolve; });
  const fence = withDirectoryLock(primaryCheckoutLockPath(paths), async () => {
    fenceReady();
    await held;
  });
  await ready;

  let reservationSettled = false;
  let assignmentSettled = false;
  const reservation = reserveAvailableWorkspace(paths, {
    owner: "reserved",
    hostname: "host",
    expiresAt: T3,
    now: T2,
  }, available.path).finally(() => { reservationSettled = true; });
  const assignment = markWorkspaceAssigned(paths, preparing.path, preparing.operationId!, {
    owner: "prepared",
    hostname: "host",
    expiresAt: T3,
    now: T2,
  }).finally(() => { assignmentSettled = true; });

  await new Promise((resolve) => setTimeout(resolve, 25));
  assert.equal(reservationSettled, false);
  assert.equal(assignmentSettled, false);
  releaseFence();
  await fence;
  assert.equal((await reservation)?.lifecycle, "assigned");
  assert.equal((await assignment).lifecycle, "assigned");
});

test("warm workspaces become available and named ports remain unique", async (t) => {
  const { root, paths } = await fixture(t);
  const warm = await prepare(paths, path.join(root, "warm"));
  const available = await markWorkspaceAvailable(paths, warm.path, warm.operationId!, T1);
  assert.equal(available.lifecycle, "available");

  const first = await reserveAvailableWorkspace(paths, {
    owner: "one",
    hostname: "host",
    expiresAt: T3,
    now: T2,
  });
  const second = await assign(paths, path.join(root, "second"), T1, T3);
  const allocator = async (excluded: ReadonlySet<number>) => excluded.has(4100) ? 4101 : 4100;
  const [firstPorts, secondPorts] = await Promise.all([
    allocateAssignmentPorts(paths, first!.assignment!.id, ["app"], allocator),
    allocateAssignmentPorts(paths, second.assignment!.id, ["debug"], allocator),
  ]);
  assert.notEqual(firstPorts.assignment!.ports["app"], secondPorts.assignment!.ports["debug"]);
  await assert.rejects(
    allocateAssignmentPorts(paths, first!.assignment!.id, ["web-app", "web_app"], allocator),
    /unique after normalization/,
  );
  await recordSuccessfulAcquisition(paths, first!.assignment!.id, first!.operationId!, true);

  const state = await readState(paths);
  assert.equal(state.metrics.acquisitions, 2);
  assert.equal(state.metrics.workspaceReuses, 1);
});

test("named port reservations preserve prototype-like names", async (t) => {
  const { root, paths } = await fixture(t);
  const workspace = await assign(paths, path.join(root, "workspace"));
  const allocated = await allocateAssignmentPorts(
    paths,
    workspace.assignment!.id,
    ["__proto__"],
    async () => 4100,
  );

  assert.equal(Object.hasOwn(allocated.assignment!.ports, "__proto__"), true);
  assert.equal(allocated.assignment!.ports["__proto__"], 4100);
  const persisted = Object.values((await readState(paths)).workspaces)[0]!;
  assert.equal(persisted.assignment!.ports["__proto__"], 4100);
});

test("corrupt assignment state cannot release a host port reservation", async (t) => {
  const { root, paths } = await fixture(t);
  const assigned = await assign(paths, path.join(root, "workspace"));
  await allocateAssignmentPorts(paths, assigned.assignment!.id, ["app"], async () => 4199);
  const validState = await fs.readFile(paths.state, "utf8");
  await fs.writeFile(paths.state, "{");
  await assert.rejects(
    allocateAssignmentPorts(paths, assigned.assignment!.id, ["debug"], async () => 4200),
    SyntaxError,
  );
  await fs.writeFile(paths.state, validState);
  await beginWorkspaceReturn(paths, assigned.assignment!.id);
  await finishWorkspaceReturn(paths, assigned.assignment!.id);
});

test("named ports remain unique across repositories", async (t) => {
  const { root, paths } = await fixture(t);
  const otherPaths = storePaths(path.join(root, "other-repository"));
  const first = await assign(paths, path.join(root, "first"));
  const second = await assign(otherPaths, path.join(root, "second"));
  const allocator = async (excluded: ReadonlySet<number>) => excluded.has(4100) ? 4101 : 4100;

  const [firstPorts, secondPorts] = await Promise.all([
    allocateAssignmentPorts(paths, first.assignment!.id, ["app"], allocator),
    allocateAssignmentPorts(otherPaths, second.assignment!.id, ["app"], allocator),
  ]);

  assert.notEqual(firstPorts.assignment!.ports["app"], secondPorts.assignment!.ports["app"]);
});

test("GC selects stale safe records and reports expired assignments without reclaiming them", async (t) => {
  const { root, paths } = await fixture(t);
  const available = await makeAvailable(paths, path.join(root, "available"), T1);
  const failedPrep = await prepare(paths, path.join(root, "failed"), T0);
  await markWorkspaceFailed(paths, failedPrep.path, failedPrep.operationId!, "install failed", T1);
  const expired = await assign(paths, path.join(root, "expired"), T0, T1);
  await assign(paths, path.join(root, "active"), T1, T3);
  await prepare(paths, path.join(root, "preparing"), T0);
  const acquiringPreparation = await prepare(paths, path.join(root, "acquiring"), T0);
  await markWorkspaceAssigned(paths, acquiringPreparation.path, acquiringPreparation.operationId!, {
    owner: "agent",
    hostname: "host",
    expiresAt: T3,
    now: T0,
  });
  const interruptedCollection = await makeAvailable(paths, path.join(root, "collecting"), T0);
  await beginWorkspaceCollection(paths, interruptedCollection.path, interruptedCollection.updatedAt, T1);

  const candidates = await identifyGcCandidates(paths, T1, T2, true);
  assert.deepEqual(
    candidates.map(({ workspace, reason, requiresForce }) => ({
      name: path.basename(workspace.path),
      reason,
      requiresForce,
    })),
    [
      { name: "acquiring", reason: "abandoned-acquisition", requiresForce: false },
      { name: "available", reason: "available", requiresForce: false },
      { name: "collecting", reason: "interrupted-collection", requiresForce: false },
      { name: "expired", reason: "expired-assignment", requiresForce: true },
      { name: "failed", reason: "failed", requiresForce: false },
      { name: "preparing", reason: "abandoned-preparation", requiresForce: false },
    ],
  );
  assert.equal((await findAssignments(paths, { id: expired.assignment!.id, now: T2 }))[0]?.expired, true);
  assert.equal(
    Object.values((await readState(paths)).workspaces).find((entry) => entry.path === expired.path)
      ?.lifecycle,
    "assigned",
  );

  const collecting = await beginWorkspaceCollection(paths, available.path, available.updatedAt, T2);
  assert.equal(await reserveAvailableWorkspace(paths, {
    owner: "late",
    hostname: "host",
    expiresAt: T3,
    now: T2,
  }), null);
  await cancelWorkspaceCollection(paths, collecting.path, collecting.operationId!, T2);
  const retry = (await identifyGcCandidates(paths, T2, T2)).find(
    (candidate) => candidate.workspace.path === available.path,
  );
  assert.ok(retry);
  const recollecting = await beginWorkspaceCollection(paths, retry.workspace.path, retry.workspace.updatedAt, T3);
  await assert.rejects(
    deleteWorkspaceRecord(paths, recollecting.path, crypto.randomUUID()),
    /operation does not match/,
  );
  await deleteWorkspaceRecord(paths, recollecting.path, recollecting.operationId!);
});
