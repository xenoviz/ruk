import assert from "node:assert/strict";
import test from "node:test";
import {
  runtimeBenchmarkResult,
  summarizeSamples,
} from "../scripts/runtime-benchmark-schema.js";
import {
  hasExpectedManagedChild,
  hasCompleteRootSample,
  isTolerableLegacyWrapperFailure,
  makeAssertions,
  matchesExpectedExecutable,
  normalizeExecutableName,
  nominalWrapperEndFromReadiness,
  observedGoPowerShellChildren,
  collectTargetResults,
  parseBenchmarkTargets,
  shouldFailBenchmark,
} from "../scripts/benchmark-runtime.js";
import type { TargetBenchmark } from "../scripts/runtime-benchmark-schema.js";

const zeroSummary = { minimum: 0, median: 0, maximum: 0 };

test("benchmark target selection defaults and validates unique targets", () => {
  assert.deepEqual(parseBenchmarkTargets("node,go"), ["node", "go"]);
  assert.deepEqual(parseBenchmarkTargets(" go "), ["go"]);
  assert.throws(() => parseBenchmarkTargets("node,node"), /duplicate target node/);
  assert.throws(() => parseBenchmarkTargets("node,python"), /unsupported target/);
  assert.throws(() => parseBenchmarkTargets("node,"), /comma-separated list/);
});

function targetBenchmark(name: TargetBenchmark["name"], concurrencyLevels: readonly number[]): TargetBenchmark {
  return {
    name,
    runtimeVersion: "test",
    binaryBytes: 1,
    coldStartMs: zeroSummary,
    wrappers: concurrencyLevels.map((concurrency) => ({
      concurrency,
      elapsedMs: zeroSummary,
      coldResidentBytes: zeroSummary,
      idleResidentBytes: zeroSummary,
      peakResidentBytes: { minimum: 100, median: 100, maximum: 100 },
      idleChildProcessCount: zeroSummary,
      peakChildProcessCount: zeroSummary,
      peakWindowsPowerShellChildren: zeroSummary,
    })),
  };
}

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
    failures: [],
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
    schemaVersion: 3,
    fixtureMode: "shared-repository-readiness-gated",
    generatedAt: "2026-08-14T00:00:00.000Z",
    platform: { os: "linux", architecture: "x64" },
    sampleCount: 3,
    wrapperDurationMs: 1_500,
    assignmentTTLMinutes: 0.5,
    concurrencyLevels: [1, 10, 20],
    targets: [],
    failures: [],
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

test("runtime benchmark preserves target failure metadata", () => {
  const failure = {
    target: "node" as const,
    message: "benchmark wrapper failures: #1 code=1 stderr=EPERM",
  };
  const result = runtimeBenchmarkResult({
    generatedAt: "2026-08-14T00:00:00Z",
    platform: "win32",
    architecture: "x64",
    sampleCount: 3,
    wrapperDurationMs: 12_000,
    assignmentTTLMinutes: 0.5,
    concurrencyLevels: [1, 10, 20],
    targets: [],
    failures: [failure],
    assertions: {
      minimumRamReductionPercent: 50,
      ramReductionPercentByConcurrency: { "1": null, "10": null, "20": null },
      ramTargetMet: false,
      zeroRoutineWindowsPowerShellChildren: true,
      observedWindowsPowerShellChildren: 0,
      applicable: true,
      failureReasons: ["node benchmark failed: benchmark wrapper failures: #1 code=1 stderr=EPERM"],
    },
  });
  assert.deepEqual(result.failures, [failure]);
  assert.equal(result.fixtureMode, "shared-repository-readiness-gated");
});

test("incomplete target measurements cannot pass the RAM assertion", () => {
  const go = targetBenchmark("go", [1, 10, 20]);
  const assertions = makeAssertions([go], [1, 10, 20], [{
    target: "node",
    message: "benchmark wrapper failed",
  }]);
  assert.deepEqual(assertions.ramReductionPercentByConcurrency, {
    "1": null,
    "10": null,
    "20": null,
  });
  assert.equal(assertions.ramTargetMet, false);
  assert.ok(assertions.failureReasons.some((reason) => reason.includes("node")));

  const partialNode = targetBenchmark("node", [1]);
  const completeGo = targetBenchmark("go", [1, 10]);
  const partialAssertions = makeAssertions([partialNode, completeGo], [1, 10], []);
  assert.deepEqual(partialAssertions.ramReductionPercentByConcurrency, { "1": 0, "10": null });
  assert.equal(partialAssertions.ramTargetMet, false);
});

test("missing Node or Go produces an explicit unavailable RAM comparison failure", () => {
  const goOnly = makeAssertions([targetBenchmark("go", [1])], [1], []);
  const nodeOnly = makeAssertions([targetBenchmark("node", [1])], [1], []);
  for (const assertions of [goOnly, nodeOnly]) {
    assert.equal(assertions.ramTargetMet, false);
    assert.ok(assertions.failureReasons.some((reason) => /RAM comparison unavailable/i.test(reason)));
  }
});

test("target failure reasons are recorded once by the assertion builder", () => {
  const failure = { target: "node" as const, message: "legacy benchmark failed" };
  const assertions = makeAssertions([targetBenchmark("go", [1])], [1], [failure]);
  assert.equal(
    assertions.failureReasons.filter((reason) => reason === "node benchmark failed: legacy benchmark failed").length,
    1,
  );
});

test("Windows applicability requires Go PowerShell evidence while preserving RAM unavailability", () => {
  const nodeOnly = makeAssertions([targetBenchmark("node", [1])], [1], [], true);
  assert.equal(nodeOnly.zeroRoutineWindowsPowerShellChildren, false);
  assert.ok(nodeOnly.failureReasons.includes("Windows PowerShell evidence unavailable: Go runtime did not complete"));

  const goOnly = makeAssertions([targetBenchmark("go", [1])], [1], [], true);
  assert.equal(goOnly.zeroRoutineWindowsPowerShellChildren, true);
  assert.equal(goOnly.ramTargetMet, false);
  assert.ok(goOnly.failureReasons.some((reason) => /RAM comparison unavailable/i.test(reason)));
  assert.equal(goOnly.failureReasons.some((reason) => /Windows PowerShell evidence unavailable/i.test(reason)), false);
});

test("single-target allowance suppresses only the missing RAM comparison", () => {
  const node = targetBenchmark("node", [1]);
  const nodeAssertions = makeAssertions([node], [1], [], false);
  const nodeResult = { targets: [node], failures: [], assertions: nodeAssertions };
  assert.equal(shouldFailBenchmark(["node"], nodeResult, false), true);
  assert.equal(shouldFailBenchmark(["node"], nodeResult, true), false);

  const go = targetBenchmark("go", [1]);
  const goAssertions = makeAssertions([go], [1], [], true);
  const goResult = { targets: [go], failures: [], assertions: goAssertions };
  assert.equal(shouldFailBenchmark(["go"], goResult, true), false);
  go.wrappers[0]!.peakWindowsPowerShellChildren = { minimum: 1, median: 1, maximum: 1 };
  const powershellResult = { targets: [go], failures: [], assertions: makeAssertions([go], [1], [], true) };
  assert.equal(shouldFailBenchmark(["go"], powershellResult, true), true);
});

test("single-target allowance never bypasses selected-target failures or the two-target RAM gate", () => {
  const node = targetBenchmark("node", [1]);
  const go = targetBenchmark("go", [1]);
  const failedSingleTarget = {
    targets: [node],
    failures: [{ target: "node" as const, message: "benchmark failed" }],
    assertions: makeAssertions([node], [1], [{ target: "node", message: "benchmark failed" }], false),
  };
  assert.equal(shouldFailBenchmark(["node"], failedSingleTarget, true), true);

  const bothAssertions = makeAssertions([node, go], [1], [], false);
  assert.equal(shouldFailBenchmark(["node", "go"], { targets: [node, go], failures: [], assertions: bothAssertions }, true), true);
});

test("target collection records a Node failure and continues with Go", async () => {
  const calls: Array<TargetBenchmark["name"]> = [];
  const result = await collectTargetResults(["node", "go"], async (name) => {
    calls.push(name);
    if (name === "node") throw new Error("legacy benchmark failed");
    return targetBenchmark("go", [1]);
  });
  assert.deepEqual(calls, ["node", "go"]);
  assert.deepEqual(result.targets.map((target) => target.name), ["go"]);
  assert.deepEqual(result.failures, [{ target: "node", message: "legacy benchmark failed" }]);
});

test("normalizes executable names across platform path variants", () => {
  assert.equal(normalizeExecutableName("node"), "node");
  assert.equal(normalizeExecutableName("node.exe"), "node");
  assert.equal(normalizeExecutableName("C:\\Program Files\\nodejs\\node.exe"), "node");
  assert.equal(normalizeExecutableName("/usr/local/bin/node"), "node");
});

test("managed-child readiness requires the wrapper root record", () => {
  const childWithoutRoot = {
    processes: [{ pid: 101, parentPid: 100, name: "node", rssBytes: 1_024 }],
  };
  assert.equal(hasExpectedManagedChild(childWithoutRoot, 100, "node"), false);
});

test("managed-child readiness finds the expected executable through descendants", () => {
  const report = {
    processes: [
      { pid: 100, parentPid: 1, name: "ruk", rssBytes: 1_024 },
      { pid: 101, parentPid: 100, name: "powershell.exe", rssBytes: 1_024 },
      { pid: 102, parentPid: 101, name: "C:\\Program Files\\nodejs\\node.exe", rssBytes: 1_024 },
    ],
  };
  assert.equal(hasExpectedManagedChild(report, 100, "node"), true);
});

test("managed-child readiness rejects PowerShell-only and unrelated descendants", () => {
  const report = {
    processes: [
      { pid: 100, parentPid: 1, name: "ruk", rssBytes: 1_024 },
      { pid: 101, parentPid: 100, name: "powershell.exe", rssBytes: 1_024 },
      { pid: 102, parentPid: 100, name: "git.exe", rssBytes: 1_024 },
    ],
  };
  assert.equal(hasExpectedManagedChild(report, 100, "node"), false);
});

test("managed-child readiness recognizes Node's Linux MainThread process name", () => {
  assert.equal(matchesExpectedExecutable("node.exe", "node"), true);
  if (process.platform === "linux") {
    assert.equal(matchesExpectedExecutable("MainThread", "node"), true);
    assert.equal(matchesExpectedExecutable("MainThread", "python"), false);
  }
});

test("first wrapper nominal end starts after managed-child readiness settles", () => {
  assert.equal(nominalWrapperEndFromReadiness(30_250, 12_000), 42_250);
  assert.equal(nominalWrapperEndFromReadiness(38_500, 18_000) + 30_000, 86_500);
});

test("legacy benchmark tolerates only full-duration known cleanup failures", () => {
  assert.equal(isTolerableLegacyWrapperFailure({
    code: 1,
    stdout: "",
    stderr: "ruk: Process 2080 could not be identified, so its workspace cannot be released safely",
    elapsedMs: 12_001,
  }, 12_000), true);
  assert.equal(isTolerableLegacyWrapperFailure({
    code: 1,
    stdout: "",
    stderr: "ruk: EPERM: operation not permitted, rename 'C:\\repo\\.git\\ruk\\state.json.9964.tmp' -> 'C:\\repo\\.git\\ruk\\state.json'",
    elapsedMs: 11_900,
  }, 12_000), false);
  assert.equal(isTolerableLegacyWrapperFailure({
    code: 1,
    stdout: "",
    stderr: "ruk: Process 2080 could not be identified, so its workspace cannot be released safely",
    elapsedMs: 1_000,
  }, 12_000), false);
  assert.equal(isTolerableLegacyWrapperFailure({
    code: 1,
    stdout: "",
    stderr: "ruk: unexpected failure",
    elapsedMs: 12_001,
  }, 12_000), false);
});

test("runtime benchmark rejects incomplete root RSS snapshots", () => {
  assert.equal(hasCompleteRootSample({ processes: [
    { pid: 10, parentPid: 1, name: "ruk", rssBytes: 1024 },
    { pid: 12, parentPid: 10, name: "node", rssBytes: 2048 },
  ] }, [10, 11]), false);
  assert.equal(hasCompleteRootSample({ processes: [
    { pid: 10, parentPid: 1, name: "ruk", rssBytes: 1024 },
    { pid: 11, parentPid: 1, name: "ruk", rssBytes: 1024 },
  ] }, [10, 11]), true);
});

test("PowerShell assertion observes only the Go runtime", () => {
  const summary = { minimum: 0, median: 0, maximum: 0 };
  const target = (name: TargetBenchmark["name"], powershell: number): TargetBenchmark => ({
    name,
    runtimeVersion: "test",
    binaryBytes: 1,
    coldStartMs: summary,
    wrappers: [{
      concurrency: 1,
      elapsedMs: summary,
      coldResidentBytes: summary,
      idleResidentBytes: summary,
      peakResidentBytes: summary,
      idleChildProcessCount: summary,
      peakChildProcessCount: summary,
      peakWindowsPowerShellChildren: { minimum: 0, median: powershell, maximum: powershell },
    }],
  });
  assert.equal(observedGoPowerShellChildren([target("node", 12), target("go", 0)]), 0);
  assert.equal(observedGoPowerShellChildren([target("node", 0), target("go", 1)]), 1);
});
