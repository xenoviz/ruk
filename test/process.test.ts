import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { commandExists, killProcessTree, processIdentity, requireProcessIdentity, run } from "../src/process.js";

test("process runner captures output and preserves non-zero results when requested", async () => {
  const success = await run(process.execPath, ["-e", "process.stdout.write('ok')"]);
  assert.equal(success.stdout, "ok");
  const failure = await run(process.execPath, ["-e", "process.stderr.write('bad');process.exit(7)"], {
    allowFailure: true,
  });
  assert.equal(failure.code, 7);
  assert.equal(failure.stderr, "bad");
  await assert.rejects(
    run(process.execPath, ["-e", "process.stderr.write('bad');process.exit(7)"]),
    /exit code 7: bad/,
  );
});

test("command detection recognizes the active Node executable", async () => {
  assert.equal(await commandExists(process.execPath), true);
});

test("missing process identity fails closed", async () => {
  await assert.rejects(
    requireProcessIdentity(42, async () => null, async () => true),
    /cannot be released safely/,
  );
  assert.equal(await requireProcessIdentity(42, async () => null, async () => false), null);
});

test("process runner executes Windows command shims without shell injection", async (t) => {
  if (process.platform !== "win32") return t.skip("Windows command shims are Windows-specific");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-command-shim-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await fs.writeFile(
    path.join(root, "ruk-echo.cmd"),
    '@echo off\r\nnode -e "process.stdout.write(process.argv[1])" "%~1"\r\n',
  );
  const result = await run("ruk-echo", ["safe&echo injected"], {
    env: { ...process.env, PATH: `${root}${path.delimiter}${process.env["PATH"] ?? ""}` },
  });
  assert.equal(result.stdout, "safe&echo injected");
});

test("process runner reports and cleans up a tracked process group", async () => {
  let pid = 0;
  let identity: string | null = null;
  const running = run(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
    detached: true,
    allowFailure: true,
    onSpawn: async (childPid) => {
      pid = childPid;
      identity = await processIdentity(childPid);
    },
  });
  while (!identity) await new Promise((resolve) => setTimeout(resolve, 10));
  assert.equal(await killProcessTree(pid, true, identity), true);
  const result = await running;
  assert.notEqual(result.code, 0);
});

test("process runner terminates a child when tracking registration fails", async () => {
  await assert.rejects(
    run(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
      detached: true,
      onSpawn: () => { throw new Error("tracking failed"); },
    }),
    /tracking failed/,
  );
});
