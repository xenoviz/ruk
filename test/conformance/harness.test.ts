import assert from "node:assert/strict";
import test from "node:test";
import { canonicalJSON, normalizeText, parseJSON } from "../../scripts/conformance/normalize.js";
import { validateScenarios } from "../../scripts/conformance/scenarios.js";

test("conformance normalization removes repository-specific process values", () => {
  const context = { roots: ["C:\\temp\\typescript", "/tmp/go"] };
  assert.equal(
    normalizeText("C:\\temp\\typescript\\workspace\\550e8400-e29b-41d4-a716-446655440000 PID 42 at 2026-08-16T12:00:00.000Z\r\n", context),
    "<repo>/workspace/<uuid> process <pid> at <timestamp>\n",
  );
  assert.equal(canonicalJSON({ pid: 41, updatedAt: "2026-08-16T12:00:00.000Z", path: "/tmp/go" }, context), `{"path":"<repo>","pid":"<pid>","updatedAt":"<timestamp>"}`);
  assert.equal(canonicalJSON({ trees: { "hash-a": { path: "/tmp/go/a" } }, workspaces: { "hash-b": { path: "/tmp/go/a" } } }, context), `{"trees":[{"path":"<repo>/a"}],"workspaces":[{"path":"<repo>/a"}]}`);
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

test("scenario validation rejects malformed command and fixture definitions", () => {
  assert.throws(() => validateScenarios([{ name: "missing args" }]), /name and string args/);
  assert.throws(() => validateScenarios([{ name: "bad fixture", args: [], fixture: { files: { "file": 1 } } }]), /files are invalid/);
});
