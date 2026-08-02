import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { dependencyFingerprint } from "../src/fingerprint.js";
import { run } from "../src/process.js";

async function repository() {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-fingerprint-"));
  await fs.mkdir(path.join(root, "packages", "api"), { recursive: true });
  await fs.writeFile(path.join(root, "package.json"), '{"name":"root","workspaces":["packages/*"]}\n');
  await fs.writeFile(path.join(root, "packages", "api", "package.json"), '{"name":"api","version":"1.0.0"}\n');
  await fs.writeFile(path.join(root, "bun.lock"), "lock-v1\n");
  await fs.writeFile(path.join(root, "source.js"), "export const value = 1;\n");
  await run("git", ["init", "-q"], { cwd: root });
  await run("git", ["add", "."], { cwd: root });
  return root;
}

const manager = { name: "test", command: [process.execPath], dependencyMode: "managed" };

test("fingerprint changes only for dependency inputs", async (t) => {
  const root = await repository();
  t.after(() => fs.rm(root, { recursive: true, force: true }));

  const first = await dependencyFingerprint({ root, manager });
  await fs.writeFile(path.join(root, "source.js"), "export const value = 2;\n");
  const sourceChanged = await dependencyFingerprint({ root, manager });
  assert.equal(sourceChanged.fingerprint, first.fingerprint);

  await fs.writeFile(path.join(root, "packages", "api", "package.json"), '{"name":"api","version":"2.0.0"}\n');
  const manifestChanged = await dependencyFingerprint({ root, manager });
  assert.notEqual(manifestChanged.fingerprint, first.fingerprint);

  await fs.writeFile(path.join(root, "bun.lock"), "lock-v2\n");
  const lockChanged = await dependencyFingerprint({ root, manager });
  assert.notEqual(lockChanged.fingerprint, manifestChanged.fingerprint);

  await fs.writeFile(path.join(root, "bun.lock"), "lock-v2\r\n");
  const lineEndingsChanged = await dependencyFingerprint({ root, manager });
  assert.equal(lineEndingsChanged.fingerprint, lockChanged.fingerprint);
});

test("fingerprint separates managed and shared dependency layouts", async (t) => {
  const root = await repository();
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const managed = await dependencyFingerprint({ root, manager });
  const shared = await dependencyFingerprint({
    root,
    manager: { ...manager, dependencyMode: "shared" },
  });
  assert.notEqual(shared.fingerprint, managed.fingerprint);
});
