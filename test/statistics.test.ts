import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { emptyMetrics, treeKey } from "../src/state.js";
import { diskStatistics, usageStatistics } from "../src/statistics.js";
import type { RukState, WorkspaceRecord } from "../src/types.js";

test("statistics aggregate counters and estimate repeated linked content", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-stats-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const workspace = path.join(root, "workspace");
  const target = path.join(root, "store", "package");
  await fs.mkdir(path.join(workspace, "node_modules"), { recursive: true });
  await fs.mkdir(target, { recursive: true });
  await fs.writeFile(path.join(target, "index.js"), "12345");
  const type = process.platform === "win32" ? "junction" : "dir";
  await fs.symlink(target, path.join(workspace, "node_modules", "one"), type);
  await fs.symlink(target, path.join(workspace, "node_modules", "two"), type);
  const now = new Date(0).toISOString();
  const record: WorkspaceRecord = {
    path: workspace,
    managed: true,
    branch: "(warm)",
    lifecycle: "available",
    operationId: null,
    assignment: null,
    processes: [],
    createdAt: now,
    updatedAt: now,
    availableAt: now,
    failure: null,
  };
  const metrics = { ...emptyMetrics(), acquisitions: 4, workspaceReuses: 3, preparations: 2, preparationSkips: 2, totalPreparationMs: 30 };
  const state: RukState = {
    version: 4,
    metrics,
    workspaces: { [treeKey(workspace)]: record },
    trees: {
      [treeKey(workspace)]: {
        path: workspace,
        fingerprint: "fingerprint",
        mode: "bun-global-store",
        projections: ["node_modules"],
        branch: "(detached)",
        updatedAt: now,
      },
    },
  };

  const usage = usageStatistics(state);
  assert.equal(usage.reuseRate, 0.75);
  assert.equal(usage.averagePreparationMs, 15);
  assert.equal(usage.availableWorkspaces, 1);
  state.workspaces[treeKey(workspace)]!.operationId = "00000000-0000-4000-8000-000000000001";
  assert.equal(usageStatistics(state).availableWorkspaces, 0);
  state.workspaces[treeKey(workspace)]!.operationId = null;
  state.workspaces[treeKey(workspace)]!.lifecycle = "returning";
  state.workspaces[treeKey(workspace)]!.assignment = {
    id: "00000000-0000-4000-8000-000000000000",
    owner: "agent",
    hostname: "host",
    assignedAt: now,
    renewedAt: now,
    expiresAt: new Date(1).toISOString(),
    leaseDurationMinutes: 1 / 60_000,
    lastActivityAt: now,
    leaseKeepers: [],
    ports: {},
  };
  assert.equal(usageStatistics(state).activeAssignments, 1);
  const disk = await diskStatistics(state);
  assert.equal(disk.linkedTargetBytes, 5);
  assert.equal(disk.estimatedBytesAvoided, 5);
});

test("disk statistics tolerate unreadable linked targets", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX permissions are required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-stats-unreadable-"));
  const workspace = path.join(root, "workspace");
  const target = path.join(root, "target");
  t.after(async () => {
    await fs.chmod(target, 0o700).catch(() => {});
    await fs.rm(root, { recursive: true, force: true });
  });
  await fs.mkdir(path.join(workspace, "node_modules"), { recursive: true });
  await fs.mkdir(target);
  await fs.writeFile(path.join(target, "index.js"), "content");
  await fs.symlink(target, path.join(workspace, "node_modules", "unreadable"), "dir");
  await fs.chmod(target, 0);
  const now = new Date(0).toISOString();
  const state: RukState = {
    version: 4,
    metrics: emptyMetrics(),
    workspaces: {
      [treeKey(workspace)]: {
        path: workspace,
        managed: true,
        branch: "(warm)",
        lifecycle: "available",
        operationId: null,
        assignment: null,
        processes: [],
        createdAt: now,
        updatedAt: now,
        availableAt: now,
        failure: null,
      },
    },
    trees: {
      [treeKey(workspace)]: {
        path: workspace,
        fingerprint: "fingerprint",
        mode: "managed-install",
        projections: ["node_modules"],
        branch: "(detached)",
        updatedAt: now,
      },
    },
  };
  assert.deepEqual(await diskStatistics(state), {
    projectionBytes: 0,
    linkedTargetBytes: 0,
    estimatedBytesAvoided: 0,
  });
});

test("disk statistics count nested linked targets once", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-stats-nested-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const workspace = path.join(root, "workspace");
  const parent = path.join(root, "store", "parent");
  const child = path.join(root, "store", "child");
  await fs.mkdir(path.join(workspace, "node_modules"), { recursive: true });
  await fs.mkdir(parent, { recursive: true });
  await fs.mkdir(child, { recursive: true });
  await fs.writeFile(path.join(parent, "parent.js"), "12345");
  await fs.writeFile(path.join(child, "child.js"), "1234567");
  const type = process.platform === "win32" ? "junction" : "dir";
  await fs.symlink(child, path.join(parent, "child"), type);
  await fs.symlink(parent, path.join(workspace, "node_modules", "parent"), type);
  await fs.symlink(child, path.join(workspace, "node_modules", "child"), type);
  const now = new Date(0).toISOString();
  const state: RukState = {
    version: 4,
    metrics: emptyMetrics(),
    workspaces: {
      [treeKey(workspace)]: {
        path: workspace,
        managed: true,
        branch: "(warm)",
        lifecycle: "available",
        operationId: null,
        assignment: null,
        processes: [],
        createdAt: now,
        updatedAt: now,
        availableAt: now,
        failure: null,
      },
    },
    trees: {
      [treeKey(workspace)]: {
        path: workspace,
        fingerprint: "fingerprint",
        mode: "managed-install",
        projections: ["node_modules"],
        branch: "(detached)",
        updatedAt: now,
      },
    },
  };
  assert.deepEqual(await diskStatistics(state), {
    projectionBytes: 0,
    linkedTargetBytes: 12,
    estimatedBytesAvoided: 0,
  });
});
