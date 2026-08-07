import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { addWorktree, assignWorktree, currentBranch, fetchRemote, returnWorktree } from "../src/git.js";
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
  await fs.writeFile(path.join(root, ".gitignore"), "ignored-secret.txt\nnode_modules/\n");
  await run("git", ["add", "."], { cwd: root });
  await run("git", ["commit", "-qm", "fixture"], { cwd: root });

  await addWorktree({ cwd: root, destination: workspace, branch: "unused", detach: true, stdio: "ignore" });
  await assignWorktree({ repository: root, workspace, branch: "agent/pool" });
  assert.equal(await currentBranch(workspace), "agent/pool");

  await fs.writeFile(path.join(workspace, "tracked.txt"), "dirty\n");
  await fs.writeFile(path.join(workspace, "ignored-secret.txt"), "secret\n");
  await fs.mkdir(path.join(workspace, "node_modules", "fixture"), { recursive: true });
  await fs.writeFile(path.join(workspace, "node_modules", "fixture", "ready"), "yes\n");
  await assert.rejects(returnWorktree(workspace), /uncommitted changes/);
  await returnWorktree(workspace, true, ["node_modules"]);
  assert.equal(await currentBranch(workspace), "(detached)");
  assert.equal((await fs.readFile(path.join(workspace, "tracked.txt"), "utf8")).trim(), "clean");
  await assert.rejects(fs.access(path.join(workspace, "ignored-secret.txt")));
  assert.equal(await fs.readFile(path.join(workspace, "node_modules", "fixture", "ready"), "utf8"), "yes\n");
});

test("fetch is explicit and updates the selected remote", async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-git-fetch-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const remote = path.join(parent, "remote.git");
  const source = path.join(parent, "source");
  const clone = path.join(parent, "clone");
  await run("git", ["init", "--bare", "-q", remote]);
  await run("git", ["init", "-q", "-b", "main", source]);
  await run("git", ["config", "user.email", "test@example.com"], { cwd: source });
  await run("git", ["config", "user.name", "ruk test"], { cwd: source });
  await fs.writeFile(path.join(source, "tracked.txt"), "one\n");
  await run("git", ["add", "."], { cwd: source });
  await run("git", ["commit", "-qm", "one"], { cwd: source });
  await run("git", ["remote", "add", "origin", remote], { cwd: source });
  await run("git", ["push", "-qu", "origin", "main"], { cwd: source });
  await run("git", ["clone", "-q", "-b", "main", remote, clone]);
  await fs.writeFile(path.join(source, "tracked.txt"), "two\n");
  await run("git", ["commit", "-qam", "two"], { cwd: source });
  await run("git", ["push", "-q"], { cwd: source });

  const before = (await run("git", ["rev-parse", "origin/main"], { cwd: clone })).stdout;
  await fetchRemote(clone, "origin/main");
  const after = (await run("git", ["rev-parse", "origin/main"], { cwd: clone })).stdout;
  assert.notEqual(after, before);
});

test("fetch recognizes fully qualified remote-tracking refs", async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-git-qualified-fetch-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const root = path.join(parent, "repo");
  const origin = path.join(parent, "origin.git");
  const upstream = path.join(parent, "upstream.git");
  await run("git", ["init", "--bare", "-q", origin]);
  await run("git", ["init", "--bare", "-q", upstream]);
  await run("git", ["init", "-q", root]);
  await run("git", ["remote", "add", "origin", origin], { cwd: root });
  await run("git", ["remote", "add", "upstream", upstream], { cwd: root });

  assert.equal(await fetchRemote(root, "refs/remotes/upstream/main"), "upstream");
});
