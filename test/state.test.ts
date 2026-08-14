import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { emptyMetrics, readState, setTreeState, storePaths, treeKey } from "../src/state.js";

test("state updates are atomic across concurrent workspaces", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-state-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const paths = storePaths(root);
  const workspaces = Array.from({ length: 12 }, (_, index) => path.join(root, `workspace-${index}`));

  await Promise.all(
    workspaces.map((workspace, index) =>
      setTreeState(paths, workspace, {
        fingerprint: `fingerprint-${index}`,
        mode: "managed-install",
        projections: ["node_modules"],
        branch: `agent/${index}`,
        updatedAt: new Date(0).toISOString(),
      }),
    ),
  );

  const state = await readState(paths);
  assert.equal(Object.keys(state.trees).length, workspaces.length);
  const selected = workspaces[4];
  assert.ok(selected);
  assert.equal(state.trees[treeKey(selected)]?.fingerprint, "fingerprint-4");
});

test("state rejects corrupted and unsupported data", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-state-invalid-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const paths = storePaths(root);
  await fs.mkdir(paths.root, { recursive: true });
  await fs.writeFile(paths.state, "not-json");
  await assert.rejects(readState(paths), /Cannot parse Ruk state/);
  await fs.writeFile(paths.state, '{"version":99,"trees":{}}');
  await assert.rejects(readState(paths), /Unsupported or invalid Ruk state/);
});

test("state safely migrates v1 preparation records to v4", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-state-migrate-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const paths = storePaths(root);
  const workspace = path.join(root, "workspace");
  const key = treeKey(workspace);
  const tree = {
    path: workspace,
    fingerprint: "fingerprint",
    mode: "managed-install",
    projections: ["node_modules"],
    branch: "agent/test",
    updatedAt: new Date(0).toISOString(),
  };
  await fs.mkdir(paths.root, { recursive: true });
  await fs.writeFile(paths.state, JSON.stringify({ version: 1, trees: { [key]: tree } }));

  const migrated = await readState(paths);
  assert.equal(migrated.version, 4);
  assert.deepEqual(migrated.trees[key], tree);
  assert.deepEqual(migrated.workspaces, {});
  assert.equal(migrated.metrics.acquisitions, 0);

  await setTreeState(paths, workspace, { ...tree, fingerprint: "updated" });
  const persisted = JSON.parse(await fs.readFile(paths.state, "utf8")) as { version: number };
  assert.equal(persisted.version, 4);
});

test("state migrates v3 assignments to v4 activity records", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-state-v3-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const paths = storePaths(root);
  const workspacePath = path.join(root, "workspace");
  const key = treeKey(workspacePath);
  await fs.mkdir(paths.root, { recursive: true });
  await fs.writeFile(paths.state, JSON.stringify({
    version: 3,
    trees: {},
    workspaces: {
      [key]: {
        path: workspacePath,
        managed: true,
        branch: "agent/test",
        lifecycle: "assigned",
        operationId: null,
        assignment: {
          id: "46bc4998-95b0-4d16-b017-69b06a13747b",
          owner: "agent",
          hostname: "host",
          assignedAt: "2026-01-01T00:00:00.000Z",
          renewedAt: "2026-01-01T01:00:00.000Z",
          expiresAt: "2026-01-01T03:00:00.000Z",
          ports: {},
        },
        processes: [],
        createdAt: "2026-01-01T00:00:00.000Z",
        updatedAt: "2026-01-01T01:00:00.000Z",
        availableAt: null,
        failure: null,
      },
    },
    metrics: emptyMetrics(),
  }));

  const migrated = await readState(paths);
  const assignment = migrated.workspaces[key]!.assignment as typeof migrated.workspaces[typeof key]["assignment"] & {
    leaseDurationMinutes: number;
    lastActivityAt: string;
    leaseKeepers: unknown[];
  };
  assert.equal(migrated.version, 4);
  assert.equal(assignment!.leaseDurationMinutes, 120);
  assert.equal(assignment!.lastActivityAt, "2026-01-01T01:00:00.000Z");
  assert.deepEqual(assignment!.leaseKeepers, []);
});
