import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { availablePort, portEnvironment, portEnvironmentName, withHostPortRegistry } from "../src/ports.js";

test("named ports normalize predictably and use available host ports", async () => {
  assert.equal(portEnvironmentName("debug-server"), "RUK_PORT_DEBUG_SERVER");
  assert.deepEqual(portEnvironment({ app: 3000 }), { RUK_PORT_APP: "3000" });
  assert.throws(() => portEnvironmentName("---"), /letter or number/);
  const port = await availablePort(new Set());
  assert.ok(port >= 1 && port <= 65_535);
});

test("host port registry rejects an insecure pre-created root", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX ownership and mode checks are required");
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-ports-security-"));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const root = path.join(temporary, `ruk-host-${process.getuid!()}`);
  await fs.mkdir(root);
  await fs.chmod(root, 0o777);
  await assert.rejects(withHostPortRegistry(() => {}, root), /Unsafe Ruk host port directory/);
});
