import assert from "node:assert/strict";
import test from "node:test";
import {
  runtimeBenchmarkResult,
  summarizeSamples,
} from "../scripts/benchmark-runtime.js";

test("runtime benchmark summaries and result schema are deterministic", () => {
  assert.deepEqual(summarizeSamples([30, 10, 20, 40]), {
    minimum: 10,
    median: 25,
    maximum: 40,
  });
  assert.deepEqual(runtimeBenchmarkResult({
    generatedAt: "2026-08-14T00:00:00Z",
    platform: "linux",
    architecture: "x64",
    sampleCount: 3,
    wrapperDurationMs: 1_500,
    targets: [],
  }), {
    schemaVersion: 1,
    generatedAt: "2026-08-14T00:00:00.000Z",
    platform: { os: "linux", architecture: "x64" },
    sampleCount: 3,
    wrapperDurationMs: 1_500,
    targets: [],
  });
});
