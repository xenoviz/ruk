import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { assertSharedBackendSupported, ensureDependencies } from "../src/dependencies.js";
import { getRepository } from "../src/git.js";
import { run } from "../src/process.js";

async function createFixture() {
  const parent = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-deps-"));
  const root = path.join(parent, "repo");
  await fs.mkdir(root);
  await fs.writeFile(path.join(root, "package.json"), '{"name":"fixture","dependencies":{"demo":"1"}}\n');
  await fs.writeFile(path.join(root, "bun.lock"), "fixture-lock\n");
  await run("git", ["init", "-q"], { cwd: root });
  await run("git", ["config", "user.email", "test@example.com"], { cwd: root });
  await run("git", ["config", "user.name", "ruk test"], { cwd: root });
  await run("git", ["add", "."], { cwd: root });
  await run("git", ["commit", "-qm", "fixture"], { cwd: root });

  const counter = path.join(parent, "install-count.txt");
  const installer = path.join(parent, "installer.mjs");
  await fs.writeFile(
    installer,
    `import fs from "node:fs/promises";
import path from "node:path";
const counter = process.argv[2];
let count = 0;
try { count = Number(await fs.readFile(counter, "utf8")); } catch {}
await fs.writeFile(counter, String(count + 1));
const pkg = JSON.parse(await fs.readFile(path.join(process.cwd(), "package.json"), "utf8"));
await fs.mkdir(path.join(process.cwd(), "node_modules", "demo"), { recursive: true });
await fs.writeFile(path.join(process.cwd(), "node_modules", "demo", "marker.txt"), pkg.dependencies.demo);
await fs.writeFile(path.join(process.cwd(), "node_modules", "demo", "global-store.txt"), process.env.BUN_INSTALL_GLOBAL_STORE ?? "");
await fs.writeFile(path.join(process.cwd(), "node_modules", "demo", "arguments.txt"), process.argv.slice(3).join(" "));
`,
  );

  return {
    parent,
    root,
    counter,
    manager: {
      name: "bun",
      command: [process.execPath, installer, counter],
      dependencyMode: "shared",
    },
  };
}

test("each worktree gets a local projection while unchanged trees skip reinstall", async (t) => {
  const fixture = await createFixture();
  t.after(() => fs.rm(fixture.parent, { recursive: true, force: true }));
  const mainRepository = await getRepository(fixture.root);
  const initial = await ensureDependencies({ repository: mainRepository, manager: fixture.manager });
  assert.equal(initial.reused, false);
  assert.equal(initial.mode, "bun-global-store");
  assert.equal(await fs.readFile(fixture.counter, "utf8"), "1");
  assert.equal(await fs.readFile(path.join(fixture.root, "node_modules", "demo", "global-store.txt"), "utf8"), "1");
  assert.equal(await fs.readFile(path.join(fixture.root, "node_modules", "demo", "arguments.txt"), "utf8"), "--linker isolated");

  const agentTree = path.join(fixture.parent, "agent");
  await run("git", ["worktree", "add", "-qb", "agent/test", agentTree], { cwd: fixture.root });
  const agentRepository = await getRepository(agentTree);
  const reused = await ensureDependencies({ repository: agentRepository, manager: fixture.manager });
  assert.equal(reused.reused, false);
  assert.equal(reused.fingerprint, initial.fingerprint);
  assert.equal(await fs.readFile(fixture.counter, "utf8"), "2");

  await fs.writeFile(path.join(agentTree, "package.json"), '{"name":"fixture","dependencies":{"demo":"2"}}\n');
  const diverged = await ensureDependencies({ repository: agentRepository, manager: fixture.manager });
  assert.equal(diverged.reused, false);
  assert.notEqual(diverged.fingerprint, initial.fingerprint);
  assert.equal(await fs.readFile(fixture.counter, "utf8"), "3");
  assert.equal(await fs.readFile(path.join(agentTree, "node_modules", "demo", "marker.txt"), "utf8"), "2");
  assert.equal(await fs.readFile(path.join(fixture.root, "node_modules", "demo", "marker.txt"), "utf8"), "1");

  const noOp = await ensureDependencies({ repository: agentRepository, manager: fixture.manager });
  assert.equal(noOp.alreadyAttached, true);
  assert.equal(await fs.readFile(fixture.counter, "utf8"), "3");
});

test("pnpm uses its global virtual store for each local projection", async (t) => {
  const fixture = await createFixture();
  t.after(() => fs.rm(fixture.parent, { recursive: true, force: true }));
  fixture.manager.name = "pnpm";

  const repository = await getRepository(fixture.root);
  const result = await ensureDependencies({ repository, manager: fixture.manager });

  assert.equal(result.mode, "pnpm-global-store");
  assert.equal(
    await fs.readFile(path.join(fixture.root, "node_modules", "demo", "arguments.txt"), "utf8"),
    "--config.enable-global-virtual-store=true",
  );
  assert.equal(
    await fs.readFile(path.join(fixture.root, "node_modules", "demo", "global-store.txt"), "utf8"),
    "",
  );
});

test("managed mode does not enable a package manager global store", async (t) => {
  const fixture = await createFixture();
  t.after(() => fs.rm(fixture.parent, { recursive: true, force: true }));
  fixture.manager.dependencyMode = "managed";

  const repository = await getRepository(fixture.root);
  const result = await ensureDependencies({ repository, manager: fixture.manager });

  assert.equal(result.mode, "managed-install");
  assert.equal(
    await fs.readFile(path.join(fixture.root, "node_modules", "demo", "arguments.txt"), "utf8"),
    "",
  );
  assert.equal(
    await fs.readFile(path.join(fixture.root, "node_modules", "demo", "global-store.txt"), "utf8"),
    "",
  );
});

test("concurrent preparation of one workspace performs one install", async (t) => {
  const fixture = await createFixture();
  t.after(() => fs.rm(fixture.parent, { recursive: true, force: true }));
  fixture.manager.dependencyMode = "managed";
  const repository = await getRepository(fixture.root);

  const [first, second] = await Promise.all([
    ensureDependencies({ repository, manager: fixture.manager }),
    ensureDependencies({ repository, manager: fixture.manager }),
  ]);

  assert.equal(await fs.readFile(fixture.counter, "utf8"), "1");
  assert.equal([first, second].filter((result) => result.alreadyAttached).length, 1);
});

test("pnpm rejects an explicitly disabled global virtual store", async (t) => {
  const fixture = await createFixture();
  t.after(() => fs.rm(fixture.parent, { recursive: true, force: true }));
  fixture.manager = {
    ...fixture.manager,
    name: "pnpm",
    command: [...fixture.manager.command, "--config.enable-global-virtual-store=false"],
  };

  const repository = await getRepository(fixture.root);
  await assert.rejects(
    ensureDependencies({ repository, manager: fixture.manager }),
    /requires the global virtual store/,
  );
});

test("shared backends reject package-manager versions without the feature", () => {
  assert.throws(
    () => assertSharedBackendSupported({ name: "bun", version: "1.3.13" }),
    /bun 1\.3\.14 or newer/,
  );
  assert.throws(
    () => assertSharedBackendSupported({ name: "pnpm", version: "10.12.0" }),
    /pnpm 10\.12\.1 or newer/,
  );
  assert.doesNotThrow(() => assertSharedBackendSupported({ name: "bun", version: "1.3.14" }));
  assert.doesNotThrow(() => assertSharedBackendSupported({ name: "pnpm", version: "11.7.0" }));
  assert.throws(
    () => assertSharedBackendSupported({ name: "npm", version: "11.9.0" }),
    /does not support npm/,
  );
});
