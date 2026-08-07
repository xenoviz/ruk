import assert from "node:assert/strict";
import test from "node:test";
import { availablePort, portEnvironment, portEnvironmentName } from "../src/ports.js";

test("named ports normalize predictably and use available host ports", async () => {
  assert.equal(portEnvironmentName("debug-server"), "RUK_PORT_DEBUG_SERVER");
  assert.deepEqual(portEnvironment({ app: 3000 }), { RUK_PORT_APP: "3000" });
  assert.throws(() => portEnvironmentName("---"), /letter or number/);
  const port = await availablePort(new Set());
  assert.ok(port >= 1 && port <= 65_535);
});
