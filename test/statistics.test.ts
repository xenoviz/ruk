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
    version: 3,
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
  const disk = await diskStatistics(state);
  assert.equal(disk.linkedTargetBytes, 5);
  assert.equal(disk.estimatedBytesAvoided, 5);
});
