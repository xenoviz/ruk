import assert from "node:assert/strict";
import test from "node:test";
import {
  GOLDEN_NORMALIZER_VERSION,
  GOLDEN_SCHEMA_VERSION,
  validateGolden,
  validateScenarioManifest,
  type GoldenFile,
} from "../../scripts/conformance/golden.js";

function golden(): GoldenFile {
  return {
    schemaVersion: GOLDEN_SCHEMA_VERSION,
    normalizerVersion: GOLDEN_NORMALIZER_VERSION,
    oracle: { runtime: "typescript", commit: "abc123", version: "0.1.2" },
    scenarioCount: 1,
    scenarioFiles: [{
      path: "test/conformance/fixtures/core.json",
      sha256: "a".repeat(64),
      scenarios: [{ name: "help", stepNames: ["help"] }],
    }],
    scenarios: [{
      name: "help",
      steps: [{
        name: "help",
        exitCode: 0,
        stdout: { kind: "text", value: "Ruk\n" },
        stderr: { kind: "text", value: "" },
        state: "null",
      }],
      finalState: "null",
    }],
  };
}

test("golden schema validates canonical streams, states, and ordered manifests", () => {
  const value = validateGolden(golden(), { expectedScenarioCount: 1 });
  assert.equal(value.scenarios[0]?.steps[0]?.stdout.kind, "text");
  assert.equal(value.scenarios[0]?.finalState, "null");
  validateScenarioManifest(value, value.scenarioFiles, 1);
});

test("golden schema rejects unknown fields and non-canonical stream JSON", () => {
  const unknown = structuredClone(golden()) as unknown as Record<string, unknown>;
  unknown["extra"] = true;
  assert.throws(() => validateGolden(unknown, { expectedScenarioCount: 1 }), /unknown field extra/);

  const invalidStream = structuredClone(golden()) as unknown as Record<string, unknown>;
  const scenarios = invalidStream["scenarios"] as Array<Record<string, unknown>>;
  const steps = scenarios[0]!["steps"] as Array<Record<string, unknown>>;
  steps[0]!["stdout"] = { kind: "json", value: '{"b":1,"a":2}' };
  assert.throws(() => validateGolden(invalidStream, { expectedScenarioCount: 1 }), /must contain canonical JSON/);
});

test("golden schema rejects changed scenario and step order", () => {
  const value = validateGolden(golden(), { expectedScenarioCount: 1 });
  const current = [{
    ...value.scenarioFiles[0]!,
    scenarios: [{ name: "other", stepNames: ["help"] }],
  }];
  assert.throws(() => validateScenarioManifest(value, current, 1), /scenario order differs/);
});
