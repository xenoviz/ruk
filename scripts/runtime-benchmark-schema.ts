export interface SampleSummary {
  minimum: number;
  median: number;
  maximum: number;
}

export interface ConcurrencyBenchmark {
  concurrency: number;
  elapsedMs: SampleSummary;
  coldResidentBytes: SampleSummary;
  idleResidentBytes: SampleSummary;
  peakResidentBytes: SampleSummary;
  idleChildProcessCount: SampleSummary;
  peakChildProcessCount: SampleSummary;
  peakWindowsPowerShellChildren: SampleSummary;
}

export interface TargetBenchmark {
  name: "node" | "go";
  runtimeVersion: string;
  binaryBytes: number;
  coldStartMs: SampleSummary;
  wrappers: ConcurrencyBenchmark[];
}

export interface RuntimeBenchmarkResult {
  schemaVersion: 2;
  generatedAt: string;
  platform: { os: NodeJS.Platform; architecture: string };
  sampleCount: number;
  wrapperDurationMs: number;
  assignmentTTLMinutes: number;
  concurrencyLevels: number[];
  targets: TargetBenchmark[];
  assertions: RuntimeBenchmarkAssertions;
}

export interface RuntimeBenchmarkAssertions {
  minimumRamReductionPercent: number;
  ramReductionPercentByConcurrency: Record<string, number | null>;
  ramTargetMet: boolean;
  zeroRoutineWindowsPowerShellChildren: boolean;
  observedWindowsPowerShellChildren: number;
  applicable: boolean;
  failureReasons: string[];
}

export function summarizeSamples(values: readonly number[]): SampleSummary {
  if (values.length === 0 || values.some((value) => !Number.isFinite(value) || value < 0)) {
    throw new Error("benchmark samples must be non-empty, finite, and non-negative");
  }
  const sorted = [...values].sort((left, right) => left - right);
  const middle = Math.floor(sorted.length / 2);
  const median = sorted.length % 2 === 0
    ? (sorted[middle - 1]! + sorted[middle]!) / 2
    : sorted[middle]!;
  return {
    minimum: Math.round(sorted[0]!),
    median: Math.round(median),
    maximum: Math.round(sorted.at(-1)!),
  };
}

export function runtimeBenchmarkResult(input: {
  generatedAt: string;
  platform: NodeJS.Platform;
  architecture: string;
  sampleCount: number;
  wrapperDurationMs: number;
  assignmentTTLMinutes: number;
  concurrencyLevels: number[];
  targets: TargetBenchmark[];
  assertions: RuntimeBenchmarkAssertions;
}): RuntimeBenchmarkResult {
  return {
    schemaVersion: 2,
    generatedAt: new Date(input.generatedAt).toISOString(),
    platform: { os: input.platform, architecture: input.architecture },
    sampleCount: input.sampleCount,
    wrapperDurationMs: input.wrapperDurationMs,
    assignmentTTLMinutes: input.assignmentTTLMinutes,
    concurrencyLevels: [...input.concurrencyLevels],
    targets: input.targets,
    assertions: input.assertions,
  };
}
