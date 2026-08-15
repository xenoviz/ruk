import assert from "node:assert/strict";
import test from "node:test";
import { canonicalJSON, normalizeText, parseJSON } from "../../scripts/conformance/normalize.js";
import { validateScenarios } from "../../scripts/conformance/scenarios.js";
import { compareStepOutput } from "../../scripts/conformance/harness.js";

test("conformance normalization removes repository-specific process values", () => {
  const context = { roots: ["C:\\temp\\typescript", "/tmp/go"] };
  assert.equal(
    normalizeText("C:\\temp\\typescript\\workspace\\550e8400-e29b-41d4-a716-446655440000 PID 42 at 2026-08-16T12:00:00.000Z\r\n", context),
    "<repo>/workspace/<uuid> process <pid> at <timestamp>\n",
  );
  assert.equal(canonicalJSON({ pid: 41, updatedAt: "2026-08-16T12:00:00.000Z", path: "/tmp/go" }, context), `{"path":"<repo>","pid":"<pid>","updatedAt":"<timestamp>"}`);
  assert.equal(canonicalJSON({ trees: { "hash-a": { path: "/tmp/go/a" } }, workspaces: { "hash-b": { path: "/tmp/go/a" } } }, context), `{"trees":[{"path":"<repo>/a"}],"workspaces":[{"path":"<repo>/a"}]}`);
  assert.equal(
    canonicalJSON({ assignmentId: "46bc4998-95b0-4d16-b017-69b06a13747b", ports: { http: 43127 } }, context),
    `{"assignmentId":"<uuid>","ports":{"http":"<port>"}}`,
  );
});

test("conformance JSON parsing distinguishes structured output from human output", () => {
  assert.deepEqual(parseJSON('{"status":"error"}\n'), { status: "error" });
  assert.equal(parseJSON("ruk: Unknown command\n"), null);
});

test("scenario format accepts core and future lifecycle/dependency/port domains", () => {
  const scenarios = validateScenarios([
    { name: "core", args: ["--help"], domains: ["core"] },
    { name: "future", args: ["acquire", "main"], domains: ["lifecycle", "dependencies", "ports"], metadata: { owner: "fixture" } },
  ]);
  assert.equal(scenarios.length, 2);
  assert.deepEqual(scenarios[1]?.domains, ["lifecycle", "dependencies", "ports"]);
});

test("scenario format accepts ordered steps with per-step state comparison", () => {
  const scenarios = validateScenarios([
    {
      name: "lifecycle",
      steps: [
        { name: "init", args: ["init", "--json"] },
        { name: "status", args: ["status", "--json"], compareState: false },
      ],
    },
  ]);
  assert.deepEqual(scenarios[0]?.steps, [
    { name: "init", args: ["init", "--json"] },
    { name: "status", args: ["status", "--json"], compareState: false },
  ]);
  assert.deepEqual(scenarios[0]?.args, undefined);
});

test("step comparison labels differences by order and name", () => {
  const differences = compareStepOutput(
    { name: "init", args: ["init", "--json"] },
    {
      exitCode: 0,
      stdout: "{\"status\":\"prepared\"}\n",
      stderr: "",
      stdoutJSON: { status: "prepared" },
      stderrJSON: null,
      state: null,
    },
    {
      exitCode: 1,
      stdout: "",
      stderr: "failed\n",
      stdoutJSON: null,
      stderrJSON: null,
      state: null,
    },
    ["/tmp/typescript", "/tmp/go"],
    2,
  );
  assert.deepEqual(differences, [
    "step 2 (init): exit code differs",
    "step 2 (init): stdout differs",
    "step 2 (init): stderr differs",
    "step 2 (init): stdout JSON differs",
  ]);
});

test("scenario validation rejects malformed command and fixture definitions", () => {
  assert.throws(() => validateScenarios([{ name: "missing args" }]), /must contain args or steps/);
  assert.throws(() => validateScenarios([{ name: "bad fixture", args: [], fixture: { files: { "file": 1 } } }]), /files are invalid/);
  assert.throws(() => validateScenarios([{ name: "empty steps", steps: [] }]), /steps must contain at least one step/);
  assert.throws(() => validateScenarios([{ name: "bad step", steps: [{ name: "init", args: ["init", 1] }] }]), /step 0 must contain a name and string args/);
});
