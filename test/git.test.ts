import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { addWorktree, assignWorktree, currentBranch, returnWorktree } from "../src/git.js";
import { run } from "../src/process.js";

test("pooled worktree assignment and return preserve branch safety", async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-git-pool-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const root = path.join(parent, "repo");
  const workspace = path.join(parent, "workspace");
  await fs.mkdir(root);
  await run("git", ["init", "-q"], { cwd: root });
  await run("git", ["config", "user.email", "test@example.com"], { cwd: root });
  await run("git", ["config", "user.name", "ruk test"], { cwd: root });
  await fs.writeFile(path.join(root, "tracked.txt"), "clean\n");
  await fs.writeFile(path.join(root, ".gitignore"), "ignored-secret.txt\n");
  await run("git", ["add", "."], { cwd: root });
  await run("git", ["commit", "-qm", "fixture"], { cwd: root });

  await addWorktree({ cwd: root, destination: workspace, branch: "unused", detach: true, stdio: "ignore" });
  await assignWorktree({ repository: root, workspace, branch: "agent/pool" });
  assert.equal(await currentBranch(workspace), "agent/pool");

  await fs.writeFile(path.join(workspace, "tracked.txt"), "dirty\n");
  await fs.writeFile(path.join(workspace, "ignored-secret.txt"), "secret\n");
  await assert.rejects(returnWorktree(workspace), /uncommitted changes/);
  await returnWorktree(workspace, true);
  assert.equal(await currentBranch(workspace), "(detached)");
  assert.equal((await fs.readFile(path.join(workspace, "tracked.txt"), "utf8")).trim(), "clean");
  await assert.rejects(fs.access(path.join(workspace, "ignored-secret.txt")));
});
