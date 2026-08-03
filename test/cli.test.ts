import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";
import { run } from "../src/process.js";

const cli = fileURLToPath(new URL("../bin/ruk.js", import.meta.url));

test("CLI creates, lists, syncs, executes in, and removes a worktree", async (t) => {
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
await fs.writeFile(counter, String(count + 1));
await fs.mkdir(path.join(process.cwd(), "node_modules", "fixture"), { recursive: true });
await fs.writeFile(path.join(process.cwd(), "node_modules", "fixture", "ready"), "yes");
`,
  );
  await fs.writeFile(path.join(root, "package.json"), '{"name":"cli-fixture","dependencies":{"fixture":"1"}}\n');
  await fs.writeFile(path.join(root, "package-lock.json"), "{}\n");
  await fs.writeFile(path.join(root, ".gitignore"), "node_modules/\n");
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
});

test("CLI exposes stable help, version, JSON, and argument errors", async (t) => {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-cli-contract-"));
  t.after(() => fs.rm(parent, { recursive: true, force: true }));
  const help = await run(process.execPath, [cli, "--help"], { cwd: parent });
  assert.match(help.stdout, /^Ruk —/);
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
});
