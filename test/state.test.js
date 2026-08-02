import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { readState, setTreeState, storePaths, treeKey } from "../src/state.js";

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
      }),
    ),
  );

  const state = await readState(paths);
  assert.equal(Object.keys(state.trees).length, workspaces.length);
  assert.equal(state.trees[treeKey(workspaces[4])].fingerprint, "fingerprint-4");
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
