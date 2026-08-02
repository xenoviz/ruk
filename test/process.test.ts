import assert from "node:assert/strict";
import test from "node:test";
import { commandExists, run } from "../src/process.js";

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
