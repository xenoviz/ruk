import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test, { type TestContext } from "node:test";
import { detectPackageManager, loadConfig } from "../src/config.js";
import type { RukConfig } from "../src/types.js";

async function directory(t: TestContext): Promise<string> {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-config-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  return root;
}

test("configuration is safe and managed by default", async (t) => {
  const root = await directory(t);
  assert.deepEqual(await loadConfig(root), {
    installCommand: null,
    dependencyMode: "managed",
  });
});

test("configuration validates modes, commands, and unknown keys", async (t) => {
  const root = await directory(t);
  await fs.writeFile(path.join(root, ".rukrc.json"), '{"dependencyMode":"unsafe"}\n');
  await assert.rejects(loadConfig(root), /managed.*shared/);

  await fs.writeFile(path.join(root, ".rukrc.json"), '{"installCommand":"npm install"}\n');
  await assert.rejects(loadConfig(root), /non-empty string array/);

  await fs.writeFile(path.join(root, ".rukrc.json"), '{"mystery":true}\n');
  await assert.rejects(loadConfig(root), /Unknown .rukrc.json option/);

  await fs.writeFile(path.join(root, ".rukrc.json"), '[]\n');
  await assert.rejects(loadConfig(root), /must contain a JSON object/);
});

test("package manager detection chooses deterministic install commands", async (t) => {
  const root = await directory(t);
  await fs.writeFile(path.join(root, "package.json"), '{"name":"fixture"}\n');
  await fs.writeFile(path.join(root, "package-lock.json"), '{}\n');
  const manager = await detectPackageManager(root, {
    dependencyMode: "managed",
    installCommand: [process.execPath, "fixture.mjs"],
  });
  assert.equal(manager.name, path.basename(process.execPath).replace(/\.exe$/i, ""));
  assert.equal(manager.dependencyMode, "managed");
});

test("environment configuration is parsed and validated", async (t) => {
  const root = await directory(t);
  const previousCommand = process.env["RUK_INSTALL_COMMAND"];
  const previousMode = process.env["RUK_DEPENDENCY_MODE"];
  t.after(() => {
    if (previousCommand === undefined) delete process.env["RUK_INSTALL_COMMAND"];
    else process.env["RUK_INSTALL_COMMAND"] = previousCommand;
    if (previousMode === undefined) delete process.env["RUK_DEPENDENCY_MODE"];
    else process.env["RUK_DEPENDENCY_MODE"] = previousMode;
  });

  process.env["RUK_INSTALL_COMMAND"] = JSON.stringify([process.execPath, "install.mjs"]);
  process.env["RUK_DEPENDENCY_MODE"] = "shared";
  assert.deepEqual(await loadConfig(root), {
    installCommand: [process.execPath, "install.mjs"],
    dependencyMode: "shared",
  });

  process.env["RUK_INSTALL_COMMAND"] = "not-json";
  await assert.rejects(loadConfig(root), /JSON string array/);
  process.env["RUK_INSTALL_COMMAND"] = "[]";
  await assert.rejects(loadConfig(root), /non-empty string array/);
});

test("npm auto-detection uses ci with a lockfile and install without one", async (t) => {
  const root = await directory(t);
  await fs.writeFile(path.join(root, "package.json"), '{"name":"fixture"}\n');
  const config: RukConfig = { dependencyMode: "managed", installCommand: null };
  assert.deepEqual((await detectPackageManager(root, config)).command, ["npm", "install"]);
  await fs.writeFile(path.join(root, "package-lock.json"), '{}\n');
  assert.deepEqual((await detectPackageManager(root, config)).command, ["npm", "ci"]);
});

test("auto-detection reports a declared package manager missing from PATH", async (t) => {
  const root = await directory(t);
  await fs.writeFile(
    path.join(root, "package.json"),
    '{"name":"fixture","packageManager":"not-a-real-manager@1.0.0"}\n',
  );
  await assert.rejects(
    detectPackageManager(root, { dependencyMode: "managed", installCommand: null }),
    /not-a-real-manager is required/,
  );
});
