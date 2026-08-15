import assert from "node:assert/strict";
import test from "node:test";
import {
  runtimeBenchmarkResult,
  summarizeSamples,
} from "../scripts/runtime-benchmark-schema.js";

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
    assignmentTTLMinutes: 0.5,
    concurrencyLevels: [1, 10, 20],
    targets: [],
    assertions: {
      minimumRamReductionPercent: 50,
      ramReductionPercentByConcurrency: { "1": 75, "10": 80, "20": 82 },
      ramTargetMet: true,
      zeroRoutineWindowsPowerShellChildren: true,
      observedWindowsPowerShellChildren: 0,
      applicable: true,
      failureReasons: [],
    },
  }), {
    schemaVersion: 2,
    generatedAt: "2026-08-14T00:00:00.000Z",
    platform: { os: "linux", architecture: "x64" },
    sampleCount: 3,
    wrapperDurationMs: 1_500,
    assignmentTTLMinutes: 0.5,
    concurrencyLevels: [1, 10, 20],
    targets: [],
    assertions: {
      minimumRamReductionPercent: 50,
      ramReductionPercentByConcurrency: { "1": 75, "10": 80, "20": 82 },
      ramTargetMet: true,
      zeroRoutineWindowsPowerShellChildren: true,
      observedWindowsPowerShellChildren: 0,
      applicable: true,
      failureReasons: [],
    },
  });
});
