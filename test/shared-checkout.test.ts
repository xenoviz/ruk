import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { getRepository } from "../src/git.js";
import { markWorkspaceAssigned, recordPreparingWorkspace } from "../src/lifecycle.js";
import { run } from "../src/process.js";
import { storePaths } from "../src/state.js";

const cli = fileURLToPath(new URL("../bin/ruk.js", import.meta.url));

test("primary checkout task commands require an explicit sharing policy", { timeout: 60_000 }, async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-shared-checkout-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const root = path.join(parent, "repo");
  const installer = path.join(parent, "install.mjs");
  await fs.mkdir(root);
  await fs.writeFile(
    installer,
    'import fs from "node:fs/promises"; await fs.mkdir("node_modules", { recursive: true });\n',
  );
  await fs.writeFile(path.join(root, "package.json"), '{"name":"shared-checkout-fixture"}\n');
  await fs.writeFile(
    path.join(root, ".rukrc.json"),
    `${JSON.stringify({ dependencyMode: "managed", installCommand: [process.execPath, installer] })}\n`,
  );
  await run("git", ["init", "-q"], { cwd: root });
  await run("git", ["config", "user.email", "test@example.com"], { cwd: root });
  await run("git", ["config", "user.name", "ruk test"], { cwd: root });
  await run("git", ["add", "."], { cwd: root });
  await run("git", ["commit", "-qm", "fixture"], { cwd: root });
  await run(process.execPath, [cli, "init"], { cwd: root });

  const repository = await getRepository(root);
  const paths = storePaths(repository.commonDir);
  const prepared = await recordPreparingWorkspace(paths, {
    path: path.join(parent, "active-workspace"),
    branch: "agent/active",
  });
  await markWorkspaceAssigned(paths, prepared.path, prepared.operationId!, {
    owner: "agent",
    hostname: "host",
    expiresAt: new Date(Date.now() + 60 * 60_000).toISOString(),
  });

  const deniedRun = await run(
    process.execPath,
    [cli, "run", "--", process.execPath, "-e", "process.stdout.write('unsafe')"],
    { cwd: root, allowFailure: true },
  );
  assert.equal(deniedRun.code, 1);
  assert.match(deniedRun.stderr, /Primary checkout has 1 active Ruk assignment/);

  const deniedSync = await run(process.execPath, [cli, "sync", "--json"], {
    cwd: root,
    allowFailure: true,
  });
  assert.deepEqual(JSON.parse(deniedSync.stderr), {
    status: "error",
    code: "RESOURCE_BUSY",
    message: "Primary checkout has 1 active Ruk assignment; acquire a dedicated workspace or pass --allow-shared-checkout",
    retryable: true,
    activeAssignments: 1,
    recovery: "ruk acquire <branch>",
  });

  const allowed = await run(
    process.execPath,
    [cli, "run", "--allow-shared-checkout", "--", process.execPath, "-e", "process.stdout.write('allowed')"],
    { cwd: root },
  );
  assert.match(allowed.stdout, /allowed$/);

  const status = JSON.parse((await run(process.execPath, [cli, "status", "--json"], { cwd: root })).stdout);
  assert.equal(status.primaryCheckout, true);
  assert.equal(status.managed, false);
  assert.equal(status.activeAssignments, 1);
  assert.equal(status.lastActivityAt, null);
  assert.equal(status.autoRenewing, false);

  const listed = JSON.parse((await run(process.execPath, [cli, "list", "--json"], { cwd: root })).stdout);
  assert.equal(listed[0].primaryCheckout, true);
  assert.equal(listed[0].managed, false);
  assert.equal(listed[0].activeAssignments, 1);
});
