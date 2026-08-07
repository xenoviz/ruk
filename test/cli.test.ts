import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { getRepository } from "../src/git.js";
import { beginWorkspaceReturn, finishWorkspaceReturn, markWorkspaceAssigned, recordPreparingWorkspace } from "../src/lifecycle.js";
import { withDirectoryLock } from "../src/lock.js";
import { run } from "../src/process.js";
import { readState, storePaths, treeKey, treeLockPath } from "../src/state.js";

const cli = fileURLToPath(new URL("../bin/ruk.js", import.meta.url));

test("CLI creates, leases, reuses, and collects worktrees", { timeout: 300_000 }, async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-cli-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const root = path.join(parent, "repo");
  const tree = path.join(parent, "agent-tree");
  const counter = path.join(parent, "count.txt");
  const installer = path.join(parent, "install.mjs");
  await fs.mkdir(root);
  await fs.writeFile(
    installer,
    `import fs from "node:fs/promises";
import path from "node:path";
const counter = process.argv[2];
let count = 0;
try { count = Number(await fs.readFile(counter, "utf8")); } catch {}
if (process.env.RUK_TEST_SLOW_INSTALL === "1") await new Promise((resolve) => setTimeout(resolve, 250));
await fs.writeFile(counter, String(count + 1));
await fs.mkdir(path.join(process.cwd(), "node_modules", "fixture"), { recursive: true });
await fs.writeFile(path.join(process.cwd(), "node_modules", "fixture", "ready"), "yes");
`,
  );
  await fs.writeFile(path.join(root, "package.json"), '{"name":"cli-fixture","dependencies":{"fixture":"1"}}\n');
  await fs.writeFile(path.join(root, "package-lock.json"), "{}\n");
  await fs.writeFile(path.join(root, ".gitignore"), "node_modules/\n");
  await fs.writeFile(path.join(root, "source.txt"), "clean\n");
  await fs.writeFile(
    path.join(root, ".rukrc.json"),
    `${JSON.stringify({
      dependencyMode: "managed",
      installCommand: [process.execPath, installer, counter],
    }, null, 2)}\n`,
  );
  await run("git", ["init", "-q"], { cwd: root });
  await run("git", ["config", "user.email", "test@example.com"], { cwd: root });
  await run("git", ["config", "user.name", "ruk test"], { cwd: root });
  await run("git", ["add", "."], { cwd: root });
  await run("git", ["commit", "-qm", "fixture"], { cwd: root });

  const unprepared = JSON.parse((await run(process.execPath, [cli, "status", "--json"], { cwd: root })).stdout);
  assert.equal(unprepared.reason, "not-prepared");
  assert.equal(unprepared.recovery, "ruk sync");
  await run(process.execPath, [cli, "init"], { cwd: root });
  await run(process.execPath, [cli, "create", "agent/cli", "--path", tree], { cwd: root });
  assert.equal(await fs.readFile(counter, "utf8"), "2");

  const status = await run(process.execPath, [cli, "status", "--json"], { cwd: tree });
  assert.equal(JSON.parse(status.stdout).status, "ready");

  const listed = await run(process.execPath, [cli, "list"], { cwd: root });
  assert.match(listed.stdout, /agent\/cli/);
  const canonicalTree = await fs.realpath(tree);
  assert.match(listed.stdout, new RegExp(canonicalTree.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")));

  await fs.writeFile(path.join(tree, "package.json"), '{"name":"cli-fixture","dependencies":{"fixture":"2"}}\n');
  const executed = await run(
    process.execPath,
    [cli, "exec", "--", process.execPath, "-e", "process.stdout.write('command-ran')"],
    { cwd: tree },
  );
  assert.match(executed.stdout, /command-ran/);
  assert.equal(await fs.readFile(counter, "utf8"), "3");

  await run(process.execPath, [cli, "remove", tree, "--force"], { cwd: root });
  await assert.rejects(fs.access(tree));

  const jsonTree = path.join(parent, "json-tree");
  const created = await run(
    process.execPath,
    [cli, "create", "agent/json", "--path", jsonTree, "--json"],
    { cwd: root },
  );
  const record = JSON.parse(created.stdout);
  assert.equal(await fs.realpath(record.path), await fs.realpath(jsonTree));
  assert.equal(record.status, "prepared");
  await run(process.execPath, [cli, "remove", jsonTree, "--force"], { cwd: root });

  const warmed = await Promise.all([1, 2].map(async () => JSON.parse((await run(
    process.execPath,
    [cli, "warm", "--count", "1", "--json"],
    { cwd: root, env: { ...process.env, RUK_TEST_SLOW_INSTALL: "1" } },
  )).stdout)));
  assert.equal(warmed.every((result) => result.status === "warmed"), true);
  assert.equal(warmed.reduce((total, result) => total + result.created.length, 0), 1);

  const invalidPort = await run(
    process.execPath,
    [cli, "acquire", "agent/invalid-port", "--port", "___", "--json"],
    { cwd: root, allowFailure: true },
  );
  assert.equal(JSON.parse(invalidPort.stderr).code, "INVALID_ARGUMENT");

  const acquired = JSON.parse((await run(
    process.execPath,
    [
      cli,
      "acquire",
      "agent/lease-one",
      "--owner",
      "test-agent",
      "--ttl",
      "10",
      "--port",
      "app",
      "--port",
      "debug-server",
      "--json",
    ],
    { cwd: root },
  )).stdout);
  assert.equal(acquired.status, "assigned");
  assert.equal(acquired.reused, true);
  assert.equal(acquired.owner, undefined);
  assert.ok(Number.isInteger(acquired.ports.app));
  assert.ok(Number.isInteger(acquired.ports["debug-server"]));
  assert.notEqual(acquired.ports.app, acquired.ports["debug-server"]);

  const portOutput = await run(
    process.execPath,
    [cli, "run", "--", process.execPath, "-e", "process.stdout.write(process.env.RUK_PORT_APP || '')"],
    { cwd: acquired.path },
  );
  assert.match(portOutput.stdout, new RegExp(String(acquired.ports.app)));

  const managedRemove = await run(process.execPath, [cli, "remove", acquired.path, "--force"], {
    cwd: root,
    allowFailure: true,
  });
  assert.equal(managedRemove.code, 1);
  assert.match(managedRemove.stderr, /use ruk release/);

  const renewed = JSON.parse((await run(
    process.execPath,
    [cli, "renew", acquired.assignmentId, "--ttl", "20", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(renewed.status, "renewed");
  assert.equal(renewed.assignmentId, acquired.assignmentId);

  const running = run(
    process.execPath,
    [cli, "run", "--", process.execPath, "-e", "setInterval(() => {}, 1000)"],
    { cwd: acquired.path, allowFailure: true },
  );
  const repository = await getRepository(root);
  const paths = storePaths(repository.commonDir);
  await waitFor(async () => {
    const workspace = (await readState(paths)).workspaces[treeKey(acquired.path)];
    return workspace?.processes.length === 1;
  }, 30_000);
  const released = JSON.parse((await run(
    process.execPath,
    [cli, "release", acquired.assignmentId, "--force", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(released.status, "available");
  assert.equal(released.cleanedProcesses, 1);
  await running;

  const installsBeforeReuse = await fs.readFile(counter, "utf8");
  const reused = JSON.parse((await run(
    process.execPath,
    [cli, "acquire", "agent/lease-two", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(reused.reused, true);
  assert.equal(await fs.realpath(reused.path), await fs.realpath(acquired.path));
  assert.notEqual(reused.assignmentId, acquired.assignmentId);
  assert.equal(await fs.readFile(counter, "utf8"), installsBeforeReuse);

  await fs.writeFile(path.join(reused.path, "node_modules", "fixture", "ready"), "tampered");
  await fs.writeFile(path.join(reused.path, "node_modules", "fixture", "poison"), "present");
  await fs.writeFile(path.join(reused.path, "source.txt"), "dirty\n");
  const corruptedStatus = JSON.parse((await run(
    process.execPath,
    [cli, "status", "--json"],
    { cwd: reused.path },
  )).stdout);
  assert.equal(corruptedStatus.status, "sync-required");
  assert.equal(corruptedStatus.reason, "projection-changed");
  const dirtyRelease = await run(process.execPath, [cli, "release", reused.assignmentId, "--json"], {
    cwd: root,
    allowFailure: true,
  });
  assert.equal(dirtyRelease.code, 1);
  assert.ok((await readState(paths)).trees[treeKey(reused.path)]);
  await run("git", ["restore", "source.txt"], { cwd: reused.path });
  assert.equal((await run("git", ["status", "--porcelain"], { cwd: reused.path })).stdout, "");
  const installsBeforeRunRepair = Number(await fs.readFile(counter, "utf8"));
  const repairedRun = await run(
    process.execPath,
    [cli, "run", "--", process.execPath, "-e", "const fs=require('fs');process.stdout.write((fs.existsSync('node_modules/fixture/poison')?'poison':'clean')+fs.readFileSync('node_modules/fixture/ready','utf8'))"],
    { cwd: reused.path },
  );
  assert.match(repairedRun.stdout, /cleanyes/);
  assert.equal(Number(await fs.readFile(counter, "utf8")), installsBeforeRunRepair + 1);
  assert.equal((await run("git", ["status", "--porcelain"], { cwd: reused.path })).stdout, "");

  const staleRelease = await run(process.execPath, [cli, "release", acquired.assignmentId, "--json"], {
    cwd: root,
    allowFailure: true,
  });
  assert.equal(staleRelease.code, 1);
  assert.match(staleRelease.stderr, /does not exist/);
  await fs.writeFile(path.join(reused.path, "node_modules", "fixture", "ready"), "tampered");
  const installsBeforeRepair = Number(await fs.readFile(counter, "utf8"));
  await run(process.execPath, [cli, "release", reused.assignmentId, "--json"], { cwd: root });

  const repaired = JSON.parse((await run(
    process.execPath,
    [cli, "acquire", "agent/repaired", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(Number(await fs.readFile(counter, "utf8")), installsBeforeRepair + 1);
  assert.equal(await fs.readFile(path.join(repaired.path, "node_modules", "fixture", "ready"), "utf8"), "yes");
  await run(process.execPath, [cli, "release", repaired.assignmentId, "--json"], { cwd: root });

  const raced = JSON.parse((await run(process.execPath, [cli, "acquire", "agent/race", "--json"], {
    cwd: root,
  })).stdout);
  let racedRun!: ReturnType<typeof run>;
  await withDirectoryLock(treeLockPath(paths, raced.path), async () => {
    racedRun = run(
      process.execPath,
      [cli, "run", "--", process.execPath, "-e", "require('fs').writeFileSync('race.txt','ran')"],
      { cwd: raced.path, allowFailure: true },
    );
    await new Promise((resolve) => setTimeout(resolve, 1_000));
    await beginWorkspaceReturn(paths, raced.assignmentId);
    await finishWorkspaceReturn(paths, raced.assignmentId);
  });
  assert.equal((await racedRun).code, 1);
  await assert.rejects(fs.access(path.join(raced.path, "race.txt")));

  await fs.writeFile(path.join(reused.path, "node_modules", "fixture", "ready"), "tampered");
  const rewarmed = JSON.parse((await run(
    process.execPath,
    [cli, "warm", "--count", "1", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(rewarmed.created.length, 1);
  assert.equal(rewarmed.available, 1);

  const short = await run(
    process.execPath,
    [cli, "exec", "agent/short", process.execPath, "-e", "process.stdout.write('assigned-command')"],
    { cwd: root },
  );
  assert.match(short.stdout, /assigned-command/);
  assert.match(short.stderr, /Released/);

  const failedShort = await run(
    process.execPath,
    [cli, "exec", "agent/missing-command", "ruk-command-that-does-not-exist"],
    { cwd: root, allowFailure: true },
  );
  assert.equal(failedShort.code, 1);
  assert.match(failedShort.stderr, /Released/);

  if (process.platform !== "win32") {
    const childPidFile = path.join(parent, "background-child.pid");
    const background = await run(
      process.execPath,
      [cli, "exec", "agent/background", "sh", "-c", 'sleep 60 & echo $! > "$1"; sleep 1', "sh", childPidFile],
      { cwd: root },
    );
    assert.match(background.stderr, /Released/);
    const childPid = Number(await fs.readFile(childPidFile, "utf8"));
    await waitFor(() => processStopped(childPid));
  }

  const shellResult = await run(process.execPath, [cli, "shell", "agent/shell"], {
    cwd: root,
    env: { ...process.env, RUK_SHELL: process.execPath },
  });
  assert.match(shellResult.stderr, /Released/);
  if (process.platform !== "win32") {
    const shellProbe = path.join(parent, "shell-probe.sh");
    const shellChildPid = path.join(parent, "shell-child.pid");
    await fs.writeFile(
      shellProbe,
      "#!/bin/sh\n[ \"$(ps -o pgid= -p $$ | tr -d ' ')\" = \"$(ps -o tpgid= -p $$ | tr -d ' ')\" ] || exit 23\nsleep 60 </dev/null >/dev/null 2>&1 &\necho $! > \"$RUK_TEST_CHILD_PID\"\nprintf RUK_PTY_OK\n",
    );
    await fs.chmod(shellProbe, 0o755);
    const scriptArgs = process.platform === "darwin"
      ? ["-q", "/dev/null", process.execPath, cli, "shell", "agent/pty"]
      : [
          "-qec",
          [process.execPath, cli, "shell", "agent/pty"]
            .map((value) => `'${value.replaceAll("'", "'\\''")}'`).join(" "),
          "/dev/null",
        ];
    const ptyShell = await run("/usr/bin/script", scriptArgs, {
      cwd: root,
      env: { ...process.env, RUK_SHELL: shellProbe, RUK_TEST_CHILD_PID: shellChildPid },
    });
    assert.match(ptyShell.stdout, /RUK_PTY_OK/);
    const childPid = Number(await fs.readFile(shellChildPid, "utf8"));
    await waitFor(() => processStopped(childPid));
  }

  const statistics = JSON.parse((await run(process.execPath, [cli, "stats", "--disk", "--json"], {
    cwd: root,
  })).stdout);
  assert.ok(statistics.acquisitions >= 4);
  assert.ok(statistics.workspaceReuses >= 3);
  assert.ok(statistics.preparationSkips >= 1);
  assert.ok(statistics.disk.projectionBytes >= 0);
  assert.ok(statistics.disk.estimatedBytesAvoided >= 0);

  const planned = JSON.parse((await run(
    process.execPath,
    [cli, "gc", "--max-age", "0", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(planned.status, "planned");
  assert.equal(planned.removed.length, 2);
  const collected = JSON.parse((await run(
    process.execPath,
    [cli, "gc", "--max-age", "0", "--apply", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(collected.status, "collected");
  assert.equal(collected.removed.length, 2);
  await assert.rejects(fs.access(reused.path));

  const expiring = JSON.parse((await run(
    process.execPath,
    [cli, "acquire", "agent/expired", "--ttl", "0.001", "--json"],
    { cwd: root },
  )).stdout);
  await new Promise((resolve) => setTimeout(resolve, 100));
  const expiredPlan = JSON.parse((await run(process.execPath, [cli, "gc", "--json"], { cwd: root })).stdout);
  assert.equal(expiredPlan.removed.length, 0);
  assert.equal(expiredPlan.expired[0].assignmentId, expiring.assignmentId);
  const expiredCollection = JSON.parse((await run(
    process.execPath,
    [cli, "gc", "--apply", "--force-expired", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(expiredCollection.removed[0].reason, "expired assignment (forced)");
  await assert.rejects(fs.access(expiring.path));
});

test("failed preparation invalidates a reused workspace projection", { timeout: 60_000 }, async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-cli-failed-preparation-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const root = path.join(parent, "repo");
  const installer = path.join(parent, "install.mjs");
  await fs.mkdir(root);
  await fs.writeFile(
    installer,
    `import fs from "node:fs/promises";
import path from "node:path";
const pkg = JSON.parse(await fs.readFile(path.join(process.cwd(), "package.json"), "utf8"));
const value = pkg.dependencies.fixture;
await fs.mkdir(path.join(process.cwd(), "node_modules", "fixture"), { recursive: true });
const failing = value === "2" && process.env.RUK_TEST_FAIL_INSTALL === "1";
await fs.writeFile(path.join(process.cwd(), "node_modules", "fixture", "ready"), failing ? "corrupt" : "ready");
if (failing) process.exit(1);
`,
  );
  await fs.writeFile(path.join(root, "package.json"), '{"dependencies":{"fixture":"1"}}\n');
  await fs.writeFile(path.join(root, "package-lock.json"), "{}\n");
  await fs.writeFile(path.join(root, ".gitignore"), "node_modules/\n");
  await fs.writeFile(
    path.join(root, ".rukrc.json"),
    `${JSON.stringify({ dependencyMode: "managed", installCommand: [process.execPath, installer] })}\n`,
  );
  await run("git", ["init", "-q", "-b", "main"], { cwd: root });
  await run("git", ["config", "user.email", "test@example.com"], { cwd: root });
  await run("git", ["config", "user.name", "ruk test"], { cwd: root });
  await run("git", ["add", "."], { cwd: root });
  await run("git", ["commit", "-qm", "fixture one"], { cwd: root });
  await run("git", ["branch", "agent/a"], { cwd: root });
  await run("git", ["switch", "-qc", "agent/b"], { cwd: root });
  await fs.writeFile(path.join(root, "package.json"), '{"dependencies":{"fixture":"2"}}\n');
  await run("git", ["commit", "-qam", "fixture two"], { cwd: root });
  await run("git", ["switch", "-q", "main"], { cwd: root });

  await run(process.execPath, [cli, "warm", "--count", "1", "--from", "agent/a", "--json"], { cwd: root });
  const warmedForB = JSON.parse((await run(
    process.execPath,
    [cli, "warm", "--count", "1", "--from", "agent/b", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(warmedForB.created.length, 1);
  const failed = await run(process.execPath, [cli, "acquire", "agent/b", "--json"], {
    cwd: root,
    allowFailure: true,
    env: { ...process.env, RUK_TEST_FAIL_INSTALL: "1" },
  });
  assert.equal(failed.code, 1);
  const repository = await getRepository(root);
  const failedState = await readState(storePaths(repository.commonDir));
  assert.equal(failedState.metrics.acquisitions, 0);
  assert.equal(failedState.metrics.workspaceReuses, 0);

  const recovered = JSON.parse((await run(process.execPath, [cli, "acquire", "agent/a", "--json"], {
    cwd: root,
  })).stdout);
  assert.equal(
    await fs.readFile(path.join(recovered.path, "node_modules", "fixture", "ready"), "utf8"),
    "ready",
  );
  const recoveredState = await readState(storePaths(repository.commonDir));
  assert.equal(recoveredState.metrics.acquisitions, 1);
  assert.equal(recoveredState.metrics.workspaceReuses, 1);
  await run(process.execPath, [cli, "release", recovered.assignmentId, "--json"], { cwd: root });
});

test("--fetch without --from starts from the fetched default branch", { timeout: 60_000 }, async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-cli-fetch-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const remote = path.join(parent, "remote.git");
  const seed = path.join(parent, "seed");
  const root = path.join(parent, "repo");
  const installer = path.join(parent, "install.mjs");
  await fs.writeFile(
    installer,
    `import fs from "node:fs/promises";
import path from "node:path";
await fs.mkdir(path.join(process.cwd(), "node_modules", "fixture"), { recursive: true });
await fs.writeFile(path.join(process.cwd(), "node_modules", "fixture", "ready"), "yes");
`,
  );
  await fs.mkdir(seed);
  await fs.writeFile(path.join(seed, "package.json"), '{"dependencies":{"fixture":"1"}}\n');
  await fs.writeFile(path.join(seed, "package-lock.json"), "{}\n");
  await fs.writeFile(path.join(seed, ".gitignore"), "node_modules/\n");
  await fs.writeFile(
    path.join(seed, ".rukrc.json"),
    `${JSON.stringify({ dependencyMode: "managed", installCommand: [process.execPath, installer] })}\n`,
  );
  await fs.writeFile(path.join(seed, "version.txt"), "old\n");
  await run("git", ["init", "-q", "-b", "trunk"], { cwd: seed });
  await run("git", ["config", "user.email", "test@example.com"], { cwd: seed });
  await run("git", ["config", "user.name", "ruk test"], { cwd: seed });
  await run("git", ["add", "."], { cwd: seed });
  await run("git", ["commit", "-qm", "initial"], { cwd: seed });
  await run("git", ["init", "--bare", "-q", "--initial-branch=trunk", remote], { cwd: parent });
  await run("git", ["remote", "add", "upstream", remote], { cwd: seed });
  await run("git", ["push", "-q", "-u", "upstream", "trunk"], { cwd: seed });
  await run("git", ["clone", "-q", "--branch", "trunk", remote, root], { cwd: parent });
  await run("git", ["remote", "rename", "origin", "upstream"], { cwd: root });
  await fs.writeFile(path.join(seed, "version.txt"), "new\n");
  await run("git", ["commit", "-qam", "advance trunk"], { cwd: seed });
  await run("git", ["push", "-q"], { cwd: seed });

  const acquired = JSON.parse((await run(
    process.execPath,
    [cli, "acquire", "agent/fetched", "--fetch", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal((await fs.readFile(path.join(acquired.path, "version.txt"), "utf8")).trim(), "new");
  await run(process.execPath, [cli, "release", acquired.assignmentId, "--json"], { cwd: root });

  const createdPath = path.join(parent, "created");
  const created = JSON.parse((await run(
    process.execPath,
    [cli, "create", "agent/created", "--path", createdPath, "--fetch", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal((await fs.readFile(path.join(created.path, "version.txt"), "utf8")).trim(), "new");
  await run(process.execPath, [cli, "remove", created.path, "--force"], { cwd: root });

  const failed = await run(
    process.execPath,
    [cli, "create", "agent/missing", "--from", "missing-ref", "--json"],
    { cwd: root, allowFailure: true },
  );
  assert.equal(failed.code, 1);
  assert.equal(JSON.parse(failed.stderr).status, "error");
});

test("gc recovers an interrupted acquire handoff", async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-cli-abandoned-warm-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const root = path.join(parent, "repo");
  const abandoned = path.join(parent, "abandoned");
  await fs.mkdir(root);
  await run("git", ["init", "-q", "-b", "main"], { cwd: root });
  await run("git", ["config", "user.email", "test@example.com"], { cwd: root });
  await run("git", ["config", "user.name", "ruk test"], { cwd: root });
  await fs.writeFile(path.join(root, "tracked.txt"), "fixture\n");
  await run("git", ["add", "."], { cwd: root });
  await run("git", ["commit", "-qm", "fixture"], { cwd: root });
  await run("git", ["worktree", "add", "--detach", abandoned, "HEAD"], { cwd: root });
  const repository = await getRepository(root);
  const paths = storePaths(repository.commonDir);
  const preparing = await recordPreparingWorkspace(paths, {
    path: abandoned,
    branch: "agent/interrupted",
    now: "2026-01-01T00:00:00.000Z",
  });
  await markWorkspaceAssigned(paths, abandoned, preparing.operationId!, {
    owner: "interrupted-agent",
    hostname: "host",
    expiresAt: "2030-01-01T00:00:00.000Z",
    now: "2026-01-01T00:00:00.000Z",
  });

  const collected = JSON.parse((await run(
    process.execPath,
    [cli, "gc", "--max-age", "0", "--apply", "--json"],
    { cwd: root },
  )).stdout);
  assert.equal(collected.removed.some((entry: { path: string }) => path.resolve(entry.path) === path.resolve(abandoned)), true);
  await assert.rejects(fs.access(abandoned));
});

test("CLI exposes stable help, version, JSON, and argument errors", async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-cli-contract-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const help = await run(process.execPath, [cli, "--help"], { cwd: parent });
  assert.match(help.stdout, /^Ruk —/);
  assert.match(help.stdout, /ruk create <branch> .*\[--fetch\]/);
  assert.match(help.stdout, /ruk update \[--check\] \[--json\]/);
  const version = await run(process.execPath, [cli, "--version"], { cwd: parent });
  assert.match(version.stdout, /^0\.1\.0\n$/);

  const invalid = await run(process.execPath, [cli, "create", "one", "two"], {
    cwd: parent,
    allowFailure: true,
  });
  assert.equal(invalid.code, 1);
  assert.match(invalid.stderr, /exactly one branch name/);

  const removedViaOption = await run(process.execPath, [cli, "update", "--via", "pnpm"], {
    cwd: parent,
    allowFailure: true,
  });
  assert.equal(removedViaOption.code, 1);
  assert.match(removedViaOption.stderr, /Unknown option --via/);

  const jsonError = await run(process.execPath, [cli, "status", "--unknown", "--json"], {
    cwd: parent,
    allowFailure: true,
  });
  const failure = JSON.parse(jsonError.stderr);
  assert.equal(failure.status, "error");
  assert.equal(failure.code, "INVALID_ARGUMENT");
  assert.equal(failure.retryable, false);

  for (const [name, files] of [
    ["missing-manager", { "package.json": '{"packageManager":"ruk-missing-manager@1.0.0"}\n' }],
    [
      "missing-installer",
      {
        "package.json": '{"name":"missing-installer"}\n',
        ".rukrc.json": '{"dependencyMode":"managed","installCommand":["ruk-missing-installer"]}\n',
      },
    ],
  ] as const) {
    const root = path.join(parent, name);
    await fs.mkdir(root);
    for (const [file, content] of Object.entries(files)) await fs.writeFile(path.join(root, file), content);
    await run("git", ["init", "-q"], { cwd: root });
    const result = await run(process.execPath, [cli, "sync", "--json"], { cwd: root, allowFailure: true });
    assert.equal(JSON.parse(result.stderr).code, "DEPENDENCY_PREPARATION_FAILED");
  }
});

async function waitFor(check: () => Promise<boolean>, timeoutMs = 10_000): Promise<void> {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await check()) return;
    await new Promise((resolve) => setTimeout(resolve, 50));
  }
  throw new Error("Timed out waiting for lifecycle state");
}

async function processStopped(pid: number): Promise<boolean> {
  try {
    process.kill(pid, 0);
  } catch (error) {
    return error instanceof Error && "code" in error && error.code === "ESRCH";
  }
  const status = await run("ps", ["-o", "stat=", "-p", String(pid)], { allowFailure: true });
  return status.code !== 0 || status.stdout.trim().startsWith("Z");
}
