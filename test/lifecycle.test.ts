import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  addAssignmentProcess,
  allocateAssignmentPorts,
  beginWorkspaceCollection,
  beginWorkspaceReturn,
  cancelWorkspaceCollection,
  cancelWorkspaceReturn,
  deleteWorkspaceRecord,
  findAssignments,
  finishWorkspaceReturn,
  identifyGcCandidates,
  markWorkspaceAssigned,
  markWorkspaceAvailable,
  markWorkspaceFailed,
  recordPreparingWorkspace,
  recordSuccessfulAcquisition,
  removeAssignmentProcess,
  renewAssignment,
  reserveAvailableWorkspace,
} from "../src/lifecycle.js";
import { readState, storePaths } from "../src/state.js";
import type { StorePaths, WorkspaceRecord } from "../src/types.js";

const T0 = "2026-01-01T00:00:00.000Z";
const T1 = "2026-01-01T01:00:00.000Z";
const T2 = "2026-01-01T02:00:00.000Z";
const T3 = "2026-01-01T03:00:00.000Z";

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
  return markWorkspaceAssigned(paths, workspacePath, preparing.operationId!, {
    owner: "agent",
    hostname: "host",
    expiresAt,
    now,
  });
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
  await beginWorkspaceReturn(paths, first.assignment!.id, T1);
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
  await assert.rejects(renewAssignment(paths, crypto.randomUUID(), T3, T1), /does not exist/);
});

test("return transitions retain ownership until tracked processes are removed", async (t) => {
  const { root, paths } = await fixture(t);
  const assigned = await assign(paths, path.join(root, "workspace"));
  const assignmentId = assigned.assignment!.id;
  await addAssignmentProcess(
    paths,
    assignmentId,
    { pid: 42, groupId: 42, command: ["node", "server.js"], startedAt: "process-identity" },
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

test("only one concurrent caller reserves an available workspace", async (t) => {
  const { root, paths } = await fixture(t);
  await makeAvailable(paths, path.join(root, "workspace"));
  const results = await Promise.all(
    ["one", "two"].map((owner) =>
      reserveAvailableWorkspace(paths, { owner, hostname: "host", expiresAt: T3, now: T2 }),
    ),
  );
  assert.equal(results.filter(Boolean).length, 1);
  assert.equal((await findAssignments(paths, { now: T2 })).length, 1);
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
  await recordSuccessfulAcquisition(paths, first!.assignment!.id, true);
  await recordSuccessfulAcquisition(paths, second.assignment!.id, false);

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

  const candidates = await identifyGcCandidates(paths, T1, T2);
  assert.deepEqual(
    candidates.map(({ workspace, reason, requiresForce }) => ({
      name: path.basename(workspace.path),
      reason,
      requiresForce,
    })),
    [
      { name: "available", reason: "available", requiresForce: false },
      { name: "expired", reason: "expired-assignment", requiresForce: true },
      { name: "failed", reason: "failed", requiresForce: false },
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
