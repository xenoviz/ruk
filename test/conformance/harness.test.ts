import assert from "node:assert/strict";
import test from "node:test";
import { canonicalJSON, normalizeText, parseJSON } from "../../scripts/conformance/normalize.js";
import { validateScenarios } from "../../scripts/conformance/scenarios.js";
import { compareOutput, compareStepOutput, resolveStepArguments } from "../../scripts/conformance/harness.js";

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
  assert.equal(
    canonicalJSON({ airport: 43127, port: 43128, portNumber: 43129 }, context),
    `{"airport":43127,"port":"<port>","portNumber":"<port>"}`,
  );
  assert.equal(
    canonicalJSON({ totalPreparationMs: 21, lastPreparationMs: 7, averagePreparationMs: 10 }, context),
    `{"averagePreparationMs":"<duration>","lastPreparationMs":"<duration>","totalPreparationMs":"<duration>"}`,
  );
  assert.equal(
    canonicalJSON({ fingerprint: "ts", preparedFingerprint: "go", projectionFingerprint: "other" }, context),
    `{"fingerprint":"<fingerprint>","preparedFingerprint":"<fingerprint>","projectionFingerprint":"<fingerprint>"}`,
  );
  assert.equal(
    canonicalJSON(
      {
        repository: "/tmp/typescript",
        typescriptWorkspace: "/tmp/typescript-ruk-a1b2c3d4",
        goWorkspace: "/tmp/go-ruk-agent-lifecycle-e5f6a7b8",
      },
      context,
    ),
    `{"goWorkspace":"<workspace>","repository":"<repo>","typescriptWorkspace":"<workspace>"}`,
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
      compareFinalState: false,
    },
  ]);
  assert.deepEqual(scenarios[0]?.steps, [
    { name: "init", args: ["init", "--json"] },
    { name: "status", args: ["status", "--json"], compareState: false },
  ]);
  assert.deepEqual(scenarios[0]?.args, undefined);
  assert.equal(scenarios[0]?.compareFinalState, false);
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
    "step 3 (init): exit code differs",
    "step 3 (init): stdout JSON differs",
    "step 3 (init): stderr differs",
  ]);
});

test("JSON output is compared semantically instead of as raw text", () => {
  const differences = compareStepOutput(
    { name: "status", args: ["status", "--json"] },
    {
      exitCode: 0,
      stdout: '{"updatedAt":"2026-08-16T12:00:00.000Z","totalPreparationMs":1}\n',
      stderr: "",
      stdoutJSON: { updatedAt: "2026-08-16T12:00:00.000Z", totalPreparationMs: 1 },
      stderrJSON: null,
      state: null,
    },
    {
      exitCode: 0,
      stdout: '{"totalPreparationMs":99,"updatedAt":"2026-08-16T12:00:01.000Z"}\n',
      stderr: "",
      stdoutJSON: { totalPreparationMs: 99, updatedAt: "2026-08-16T12:00:01.000Z" },
      stderrJSON: null,
      state: null,
    },
    [],
  );
  assert.deepEqual(differences, []);
});

test("step and scenario state opt-outs have independent final-state semantics", () => {
  const result = (state: unknown) => ({
    exitCode: 0,
    stdout: "",
    stderr: "",
    stdoutJSON: null,
    stderrJSON: null,
    state,
  });
  const scenario = {
    name: "sequence",
    steps: [
      { name: "first", args: ["status"], compareState: false },
      { name: "second", args: ["status"] },
    ],
  };
  const differences = compareOutput(
    scenario,
    { ...result({ value: "ts-step" }), steps: [result({ value: "ts-step" }), result({ value: "same" })], finalState: { value: "ts-final" } },
    { ...result({ value: "go-step" }), steps: [result({ value: "go-step" }), result({ value: "same" })], finalState: { value: "go-final" } },
    [],
  );
  assert.deepEqual(differences, ["final state differs"]);

  const finalOptOut = compareOutput(
    { ...scenario, compareFinalState: false },
    { ...result(null), steps: [result({ value: "same" }), result({ value: "same" })], finalState: { value: "ts-final" } },
    { ...result(null), steps: [result({ value: "same" }), result({ value: "same" })], finalState: { value: "go-final" } },
    [],
  );
  assert.deepEqual(finalOptOut, []);

  const scenarioOptOut = compareOutput(
    { ...scenario, compareState: false },
    { ...result(null), steps: [result({ value: "ts" }), result({ value: "ts" })], finalState: { value: "ts-final" } },
    { ...result(null), steps: [result({ value: "go" }), result({ value: "go" })], finalState: { value: "go-final" } },
    [],
  );
  assert.deepEqual(scenarioOptOut, []);
});

test("step interpolation diagnostics preserve the source failure output", () => {
  const source = {
    exitCode: 1,
    stdout: "partial output\n",
    stderr: "root cause\n",
    stdoutJSON: null,
    stderrJSON: null,
    state: null,
  };
  assert.throws(
    () => resolveStepArguments(["release", "${acquire.assignmentId}"], new Map([["acquire", source]]), undefined),
    /step acquire.*exited with code 1.*partial output.*root cause/,
  );
  assert.throws(
    () => resolveStepArguments(["release", "${acquire.assignmentId}"], new Map([["acquire", { ...source, exitCode: 0 }]]), undefined),
    /step acquire.*stdout=.*partial output.*stderr=.*root cause/,
  );
});

test("scenario validation rejects malformed command and fixture definitions", () => {
  assert.throws(() => validateScenarios([{ name: "missing args" }]), /must contain args or steps/);
  assert.throws(() => validateScenarios([{ name: "bad fixture", args: [], fixture: { files: { "file": 1 } } }]), /files are invalid/);
  assert.throws(() => validateScenarios([{ name: "empty steps", steps: [] }]), /steps must contain at least one step/);
  assert.throws(() => validateScenarios([{ name: "bad step", steps: [{ name: "init", args: ["init", 1] }] }]), /step 0 must contain a name and string args/);
  assert.throws(() => validateScenarios([{ name: "bad final-state option", args: ["status"], compareFinalState: "no" }]), /invalid compareFinalState/);
});
