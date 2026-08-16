import { spawn, type ChildProcessByStdio } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import type { Readable } from "node:stream";
import { fileURLToPath } from "node:url";
import {
  runtimeBenchmarkResult,
  summarizeSamples,
} from "./runtime-benchmark-schema.js";
import type {
  BenchmarkFailure,
  ConcurrencyBenchmark,
  RuntimeBenchmarkResult,
  TargetBenchmark,
} from "./runtime-benchmark-schema.js";

type TargetName = TargetBenchmark["name"];

interface CommandSpec {
  command: string;
  args: string[];
  cwd: string;
}

interface ProcessRecord {
  pid: number;
  parentPid: number;
  name: string;
  rssBytes: number;
}

interface ProcessReport {
  processes: ProcessRecord[];
}

interface Target {
  name: TargetName;
  version: string;
  sizePath: string;
  command: string;
  prefix: string[];
  childCommand: string;
  cwd: string;
}

interface Workload {
  commands: CommandSpec[];
  cleanup(): Promise<void>;
}

interface ParsedArguments {
  nodeCli: string;
  binary: string;
  targets: TargetName[];
  samples: number;
  durationMs: number;
  ttlMinutes: number;
  concurrencyLevels: number[];
  assertTarget: boolean;
}

interface Measurement {
  elapsedMs: number;
  coldResidentBytes: number;
  idleResidentBytes: number;
  peakResidentBytes: number;
  idleChildProcessCount: number;
  peakChildProcessCount: number;
  peakWindowsPowerShellChildren: number;
}

interface InspectorTarget {
  command: string;
  args: string[];
  cwd: string;
}

interface WrapperResult {
  code: number;
  stdout: string;
  stderr: string;
  elapsedMs: number;
}

const MANAGED_CHILD_READINESS_TIMEOUT_MS = 30_000;
const MANAGED_CHILD_SETTLE_MS = 250;
const MANAGED_CHILD_POLL_MS = 25;
type BenchmarkChildProcess = ChildProcessByStdio<null, Readable, Readable>;

export function normalizeExecutableName(command: string): string {
  const basename = command.toLowerCase().replaceAll("\\", "/").split("/").at(-1) ?? "";
  return basename.endsWith(".exe") ? basename.slice(0, -4) : basename;
}

export function matchesExpectedExecutable(observedName: string, expectedCommand: string): boolean {
  const observed = normalizeExecutableName(observedName);
  const expected = normalizeExecutableName(expectedCommand);
  // Node 24 names its main Linux thread `MainThread` in /proc/status even
  // though the executable is node. Keep this exception specific to the
  // expected Node workload rather than accepting an arbitrary descendant.
  return observed === expected || (process.platform === "linux" && expected === "node" && observed === "mainthread");
}

function run(spec: CommandSpec): Promise<{ code: number; stdout: string; stderr: string }> {
  return new Promise((resolve, reject) => {
    const child = spawn(spec.command, spec.args, {
      cwd: spec.cwd,
      stdio: ["ignore", "pipe", "pipe"],
      windowsHide: true,
    });
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8").on("data", (chunk: string) => { stdout += chunk; });
    child.stderr.setEncoding("utf8").on("data", (chunk: string) => { stderr += chunk; });
    child.once("error", reject);
    child.once("close", (code) => resolve({ code: code ?? 1, stdout, stderr }));
  });
}

async function requireSuccess(spec: CommandSpec): Promise<string> {
  const result = await run(spec);
  if (result.code !== 0) {
    throw new Error(`${spec.command} ${spec.args.join(" ")} failed: ${result.stderr.trim() || result.stdout.trim()}`);
  }
  return result.stdout.trim();
}

async function fileSize(target: string): Promise<number> {
  const stat = await fs.stat(target);
  if (stat.isFile()) return stat.size;
  const entries = await fs.readdir(target, { withFileTypes: true });
  const sizes = await Promise.all(entries.map((entry) => fileSize(path.join(target, entry.name))));
  return sizes.reduce((total, size) => total + size, 0);
}

export function parseBenchmarkTargets(value: string): TargetName[] {
  const values = value.split(",").map((target) => target.trim());
  if (values.length === 0 || values.some((target) => target.length === 0)) {
    throw new Error("--targets must be a comma-separated list containing node and/or go");
  }
  const targets: TargetName[] = [];
  for (const target of values) {
    if (target !== "node" && target !== "go") {
      throw new Error(`--targets contains unsupported target ${JSON.stringify(target)}; expected node or go`);
    }
    if (targets.includes(target)) {
      throw new Error(`--targets contains duplicate target ${target}`);
    }
    targets.push(target);
  }
  return targets;
}

function parseArguments(args: readonly string[], root: string): ParsedArguments {
  const value = (name: string): string | undefined => {
    const index = args.indexOf(name);
    return index < 0 ? undefined : args[index + 1];
  };
  const nodeCli = path.resolve(value("--node") ?? path.join(root, "dist", "bin", "ruk.js"));
  const binary = path.resolve(value("--binary") ?? path.join(root, "artifacts", process.platform === "win32" ? "ruk.exe" : "ruk"));
  const targetArgument = value("--targets");
  if (args.includes("--targets") && targetArgument === undefined) {
    throw new Error("--targets requires a comma-separated value containing node and/or go");
  }
  const targets = parseBenchmarkTargets(targetArgument ?? "node,go");
  const samples = Number(value("--samples") ?? 3);
  const durationMs = Number(value("--duration") ?? 26_000);
  const ttlMinutes = Number(value("--ttl") ?? 0.5);
  const concurrencyLevels = (value("--concurrency") ?? "1,10,20").split(",").map(Number);
  const assertTarget = !args.includes("--no-assert");
  if (!Number.isSafeInteger(samples) || samples < 1) throw new Error("--samples must be a positive integer");
  if (!Number.isFinite(durationMs) || durationMs < 1_000) throw new Error("--duration must be at least 1000 milliseconds");
  if (!Number.isFinite(ttlMinutes) || ttlMinutes <= 0) throw new Error("--ttl must be a positive number of minutes");
  const heartbeatMs = ttlMinutes * 60_000 / 3;
  const expiryMs = ttlMinutes * 60_000;
  if (durationMs <= heartbeatMs) {
    throw new Error("--duration must be longer than one activity heartbeat interval (--ttl / 3)");
  }
  if (durationMs >= expiryMs) {
    throw new Error("--duration must finish before the benchmark assignment TTL expires");
  }
  if (concurrencyLevels.length === 0 || concurrencyLevels.some((level) => !Number.isSafeInteger(level) || level < 1)) {
    throw new Error("--concurrency must be a comma-separated list of positive integers");
  }
  return { nodeCli, binary, targets, samples, durationMs, ttlMinutes, concurrencyLevels, assertTarget };
}

async function makeRepository(root: string, target: Target, nodeExecutable: string): Promise<string> {
  const fixtureRoot = path.join(root, `fixture-${target.name}`);
  const repository = path.join(fixtureRoot, "repository");
  const installer = path.join(fixtureRoot, "install.mjs");
  await fs.mkdir(repository, { recursive: true });
  await fs.writeFile(installer, 'import fs from "node:fs/promises"; await fs.mkdir("node_modules", { recursive: true });\n');
  await fs.writeFile(path.join(repository, "package.json"), '{"name":"ruk-runtime-benchmark"}\n');
  await fs.writeFile(path.join(repository, ".gitignore"), "node_modules/\n");
  await fs.writeFile(path.join(repository, ".rukrc.json"), `${JSON.stringify({
    dependencyMode: "managed",
    installCommand: [nodeExecutable, installer],
  })}\n`);
  for (const args of [
    ["init", "-q"],
    ["config", "user.email", "benchmark@example.com"],
    ["config", "user.name", "ruk benchmark"],
    ["add", "."],
    ["commit", "-qm", "fixture"],
  ]) {
    await requireSuccess({ command: "git", args, cwd: repository });
  }
  await requireSuccess({ command: target.command, args: [...target.prefix, "init"], cwd: repository });
  return repository;
}

async function compileInspector(root: string, output: string): Promise<void> {
  await requireSuccess({ command: "go", args: ["build", "-trimpath", "-o", output, "./scripts/benchmark"], cwd: root });
}

async function inspect(inspector: InspectorTarget, pids: readonly number[]): Promise<ProcessReport> {
  const output = await requireSuccess({
    command: inspector.command,
    args: [...inspector.args, ...pids.flatMap((pid) => ["--pid", String(pid)])],
    cwd: inspector.cwd,
  });
  const report = JSON.parse(output) as unknown;
  if (!report || typeof report !== "object" || !Array.isArray((report as { processes?: unknown }).processes)) {
    throw new Error("process inspector returned an invalid report");
  }
  return report as ProcessReport;
}

export function hasExpectedManagedChild(
  report: ProcessReport,
  rootPid: number,
  expectedCommand: string,
): boolean {
  const records = new Map(report.processes.map((process) => [process.pid, process]));
  if (!records.has(rootPid)) return false;
  const children = new Map<number, ProcessRecord[]>();
  for (const process of report.processes) {
    const siblings = children.get(process.parentPid) ?? [];
    siblings.push(process);
    children.set(process.parentPid, siblings);
  }
  const pending = [rootPid];
  const seen = new Set<number>(pending);
  while (pending.length > 0) {
    const parent = pending.shift()!;
    for (const child of children.get(parent) ?? []) {
      if (seen.has(child.pid)) continue;
      seen.add(child.pid);
      if (matchesExpectedExecutable(child.name, expectedCommand)) return true;
      pending.push(child.pid);
    }
  }
  return false;
}

function isPowerShell(name: string): boolean {
  const normalized = name.toLowerCase().replaceAll("\\", "/").split("/").at(-1) ?? "";
  return normalized === "powershell.exe" || normalized === "pwsh.exe" || normalized === "powershell" || normalized === "pwsh";
}

export function hasCompleteRootSample(report: ProcessReport, rootPids: readonly number[]): boolean {
  const processIDs = new Set(report.processes.map((process) => process.pid));
  return rootPids.every((pid) => processIDs.has(pid));
}

export function nominalWrapperEndFromReadiness(readyAt: number, durationMs: number): number {
  return readyAt + durationMs;
}

async function waitForManagedChild(
  inspector: InspectorTarget,
  rootPid: number,
  expectedCommand: string,
  isAlive: () => boolean,
): Promise<number> {
  const expectedName = normalizeExecutableName(expectedCommand);
  const deadline = performance.now() + MANAGED_CHILD_READINESS_TIMEOUT_MS;
  let observed = "none";
  while (performance.now() < deadline) {
    if (!isAlive()) throw new Error(`benchmark wrapper root ${rootPid} exited before managed ${expectedName} child readiness; observed ${observed}`);
    const report = await inspect(inspector, [rootPid]);
    observed = report.processes.length === 0
      ? "none"
      : report.processes.map((process) => `${process.pid}:${normalizeExecutableName(process.name)}<-ppid:${process.parentPid}`).join(", ");
    if (hasExpectedManagedChild(report, rootPid, expectedCommand)) {
      await new Promise((resolve) => setTimeout(resolve, MANAGED_CHILD_SETTLE_MS));
      if (!isAlive()) throw new Error(`benchmark wrapper root ${rootPid} exited during managed ${expectedName} child settling`);
      return performance.now();
    }
    await new Promise((resolve) => setTimeout(resolve, MANAGED_CHILD_POLL_MS));
  }
  throw new Error(`benchmark wrapper root ${rootPid} did not expose managed ${expectedName} child within ${MANAGED_CHILD_READINESS_TIMEOUT_MS}ms; observed ${observed}`);
}

async function waitForWrapperCompletion(completion: Promise<WrapperResult[]>, timeoutMs: number): Promise<boolean> {
  let settled = false;
  await Promise.race([
    completion.then(
      () => { settled = true; },
      () => { settled = true; },
    ),
    new Promise((resolve) => setTimeout(resolve, timeoutMs)),
  ]);
  return settled;
}

async function stopWrapperRoots(
  children: readonly BenchmarkChildProcess[],
  completion: Promise<WrapperResult[]>,
): Promise<void> {
  for (const child of children) {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGTERM");
  }
  if (await waitForWrapperCompletion(completion, 5_000)) return;
  for (const child of children) {
    if (child.exitCode === null && child.signalCode === null) child.kill("SIGKILL");
  }
  if (!await waitForWrapperCompletion(completion, 5_000)) {
    throw new Error("benchmark wrapper roots did not settle after termination");
  }
}

async function measureWrappers(
  commands: readonly CommandSpec[],
  durationMs: number,
  inspector: InspectorTarget,
  expectedChildCommand: string,
  legacyBaseline: boolean,
): Promise<Measurement> {
  const started = performance.now();
  const children: BenchmarkChildProcess[] = [];
  const completions: Array<Promise<WrapperResult>> = [];
  let completion: Promise<WrapperResult[]> | undefined;
  let firstWrapperNominalEnd = Number.POSITIVE_INFINITY;
  let idleResidentBytes = 0;
  let coldResidentBytes = 0;
  let idleChildProcessCount = 0;
  let peakResidentBytes = 0;
  let peakChildProcessCount = 0;
  let peakWindowsPowerShellChildren = 0;
  let idleCaptured = false;
  let coldCaptured = false;
  const recordReport = (report: ProcessReport, rootPids: readonly number[], includeRSS: boolean): boolean => {
    const rootSet = new Set(rootPids);
    const childProcesses = report.processes.filter((process) => !rootSet.has(process.pid));
    peakChildProcessCount = Math.max(peakChildProcessCount, childProcesses.length);
    peakWindowsPowerShellChildren = Math.max(
      peakWindowsPowerShellChildren,
      childProcesses.filter((process) => isPowerShell(process.name)).length,
    );
    if (!includeRSS) return true;
    if (!hasCompleteRootSample(report, rootPids)) return false;
    const residentBytes = report.processes
      .filter((process) => rootSet.has(process.pid))
      .reduce((total, process) => total + process.rssBytes, 0);
    peakResidentBytes = Math.max(peakResidentBytes, residentBytes);
    if (!coldCaptured) {
      coldResidentBytes = residentBytes;
      coldCaptured = true;
    }
    if (!idleCaptured && performance.now() - started >= durationMs / 4) {
      idleResidentBytes = residentBytes;
      idleChildProcessCount = childProcesses.length;
      idleCaptured = true;
    }
    return true;
  };
  let deadline = Number.POSITIVE_INFINITY;
  let results: WrapperResult[];
  try {
    // Every target uses the same readiness-gated schedule. The legacy Node
    // runtime performs state reads and writes during `run`; a fixed launch
    // delay is not enough to prove that its managed child has started and its
    // initial state mutation has settled. Starting the next wrapper only
    // after this proof avoids measuring a synchronized fixture-write storm
    // while retaining one shared repository and overlapping steady state.
    for (const spec of commands) {
      if (performance.now() >= started + durationMs) {
        throw new Error("benchmark readiness schedule did not leave an overlapping measurement window");
      }
      const childStarted = performance.now();
      const child = spawn(spec.command, spec.args, {
        cwd: spec.cwd,
        stdio: ["ignore", "pipe", "pipe"],
        windowsHide: true,
      });
      children.push(child);
      completions.push(new Promise<WrapperResult>((resolve) => {
        let stdout = "";
        let stderr = "";
        child.stdout.setEncoding("utf8").on("data", (chunk: string) => { stdout += chunk; });
        child.stderr.setEncoding("utf8").on("data", (chunk: string) => { stderr += chunk; });
        child.once("error", (error) => resolve({
          code: 1,
          stdout,
          stderr: `${stderr}${stderr ? "\n" : ""}${error instanceof Error ? error.message : String(error)}`,
          elapsedMs: performance.now() - childStarted,
        }));
        child.once("close", (code) => resolve({ code: code ?? 1, stdout, stderr, elapsedMs: performance.now() - childStarted }));
      }));
      if (!child.pid) throw new Error("benchmark wrapper did not expose a process ID");
      const readyAt = await waitForManagedChild(
        inspector,
        child.pid,
        expectedChildCommand,
        () => child.exitCode === null && child.signalCode === null,
      );
      if (children.length === 1) firstWrapperNominalEnd = nominalWrapperEndFromReadiness(readyAt, durationMs);
      deadline = nominalWrapperEndFromReadiness(readyAt, durationMs) + 30_000;
    }
    const remainingBeforeFirstWrapperExit = firstWrapperNominalEnd - performance.now();
    if (remainingBeforeFirstWrapperExit < durationMs / 2) {
      throw new Error(`benchmark readiness schedule left only ${Math.max(0, Math.round(remainingBeforeFirstWrapperExit))}ms before the first wrapper ended; need at least ${Math.round(durationMs / 2)}ms of overlap`);
    }
    completion = Promise.all(completions);
    while (children.some(({ exitCode, signalCode }) => exitCode === null && signalCode === null)) {
      if (performance.now() > deadline) {
        throw new Error(`benchmark workload exceeded ${Math.round((deadline - started) / 1000)} seconds`);
      }
      const rootPids = children.flatMap(({ pid, exitCode, signalCode }) => (
        pid && exitCode === null && signalCode === null ? [pid] : []
      ));
      if (rootPids.length > 0) {
        recordReport(await inspect(inspector, rootPids), rootPids, true);
      }
      await new Promise((resolve) => setTimeout(resolve, process.platform === "win32" ? 100 : 50));
    }
    results = await completion;
    if (process.platform === "win32") {
      // Toolhelp keeps a child process's creator PID after the parent exits.
      // Inspect every original root once more so an orphaned PowerShell child
      // cannot disappear merely because its Ruk wrapper has completed.
      await new Promise((resolve) => setTimeout(resolve, 100));
      const allRootPids = children.flatMap(({ pid }) => pid ? [pid] : []);
      if (allRootPids.length > 0) recordReport(await inspect(inspector, allRootPids), allRootPids, false);
    }
  } catch (error) {
    completion ??= Promise.all(completions);
    try {
      await stopWrapperRoots(children, completion);
    } catch (cleanupError) {
      process.stderr.write(`Benchmark wrapper cleanup failed: ${String(cleanupError)}\n`);
    }
    throw error;
  }
  if (!coldCaptured || !idleCaptured || peakResidentBytes === 0) {
    throw new Error("process inspector never returned a complete root RSS sample");
  }
  const tolerated = legacyBaseline
    ? results.filter((result) => isTolerableLegacyWrapperFailure(result, durationMs))
    : [];
  const failures = results
    .map((result, index) => ({ ...result, index }))
    .filter((result) => result.code !== 0 && !isTolerableLegacyWrapperFailure(result, legacyBaseline ? durationMs : Number.POSITIVE_INFINITY));
  if (failures.length > 0) {
    throw new Error(`benchmark wrapper failures: ${failures.map((failure) => (
      `#${failure.index + 1} code=${failure.code} stderr=${JSON.stringify(failure.stderr.trim())} stdout=${JSON.stringify(failure.stdout.trim())}`
    )).join("; ")}`);
  }
  if (tolerated.length > 0) {
    process.stderr.write(`Tolerated ${tolerated.length} full-duration legacy cleanup failure(s).\n`);
  }
  return {
    elapsedMs: performance.now() - started,
    coldResidentBytes: coldCaptured ? coldResidentBytes : peakResidentBytes,
    idleResidentBytes: idleCaptured ? idleResidentBytes : peakResidentBytes,
    peakResidentBytes,
    idleChildProcessCount: idleCaptured ? idleChildProcessCount : peakChildProcessCount,
    peakChildProcessCount,
    peakWindowsPowerShellChildren,
  };
}

export function isTolerableLegacyWrapperFailure(result: WrapperResult, durationMs: number): boolean {
  if (result.code === 0 || result.elapsedMs < durationMs - 250 || result.stdout.trim() !== "") return false;
  const message = result.stderr.trim();
  return /^ruk: Process \d+ could not be identified, so its workspace cannot be released safely$/.test(message);
}

export async function collectTargetResults(
  targetNames: readonly TargetName[],
  runTarget: (target: TargetName) => Promise<TargetBenchmark>,
): Promise<{
  targets: TargetBenchmark[];
  failures: BenchmarkFailure[];
}> {
  const targets: TargetBenchmark[] = [];
  const failures: BenchmarkFailure[] = [];
  for (const target of targetNames) {
    try {
      targets.push(await runTarget(target));
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      failures.push({ target, message });
    }
  }
  return { targets, failures };
}

async function benchmarkTarget(
  target: Target,
  repository: string,
  inspector: InspectorTarget,
  samples: number,
  durationMs: number,
  ttlMinutes: number,
  concurrencyLevels: readonly number[],
): Promise<TargetBenchmark> {
  const coldStartMs: number[] = [];
  for (let sample = 0; sample < samples; sample += 1) {
    const started = performance.now();
    await requireSuccess({ command: target.command, args: [...target.prefix, "--version"], cwd: target.cwd });
    coldStartMs.push(performance.now() - started);
  }
  const wrappers: ConcurrencyBenchmark[] = [];
  for (const concurrency of concurrencyLevels) {
    const elapsed: number[] = [];
    const idle: number[] = [];
    const cold: number[] = [];
    const peak: number[] = [];
    const idleChildren: number[] = [];
    const peakChildren: number[] = [];
    const powershellChildren: number[] = [];
    for (let sample = 0; sample < samples; sample += 1) {
      const workload = await createWorkload(target, repository, concurrency, sample, durationMs, ttlMinutes);
      try {
        const measurement = await measureWrappers(workload.commands, durationMs, inspector, target.childCommand, target.name === "node");
        elapsed.push(measurement.elapsedMs);
        idle.push(measurement.idleResidentBytes);
        cold.push(measurement.coldResidentBytes);
        peak.push(measurement.peakResidentBytes);
        idleChildren.push(measurement.idleChildProcessCount);
        peakChildren.push(measurement.peakChildProcessCount);
        powershellChildren.push(measurement.peakWindowsPowerShellChildren);
      } finally {
        await workload.cleanup();
      }
    }
    wrappers.push({
      concurrency,
      elapsedMs: summarizeSamples(elapsed),
      coldResidentBytes: summarizeSamples(cold),
      idleResidentBytes: summarizeSamples(idle),
      peakResidentBytes: summarizeSamples(peak),
      idleChildProcessCount: summarizeSamples(idleChildren),
      peakChildProcessCount: summarizeSamples(peakChildren),
      peakWindowsPowerShellChildren: summarizeSamples(powershellChildren),
    });
  }
  return {
    name: target.name,
    runtimeVersion: target.version,
    binaryBytes: await fileSize(target.sizePath),
    coldStartMs: summarizeSamples(coldStartMs),
    wrappers,
  };
}

async function createWorkload(
  target: Target,
  repository: string,
  concurrency: number,
  sample: number,
  durationMs: number,
  ttlMinutes: number,
): Promise<Workload> {
  const assignments: Array<{ assignmentId: string; path: string }> = [];
  try {
    for (let index = 0; index < concurrency; index += 1) {
      const output = await requireSuccess({
        command: target.command,
        args: [...target.prefix, "acquire", `benchmark/${target.name}/${sample}/${index}`, "--ttl", String(ttlMinutes), "--json"],
        cwd: repository,
      });
      const parsed = JSON.parse(output) as { assignmentId?: unknown; path?: unknown };
      if (typeof parsed.assignmentId !== "string" || typeof parsed.path !== "string") {
        throw new Error("acquire returned an incomplete assignment");
      }
      assignments.push({ assignmentId: parsed.assignmentId, path: parsed.path });
    }
  } catch (error) {
    await releaseAssignments(target, repository, assignments);
    throw error;
  }
  return {
    commands: assignments.map(({ path: cwd }) => ({
      command: target.command,
      args: [...target.prefix, "run", "--", target.childCommand, "-e", `setTimeout(() => {}, ${durationMs})`],
      cwd,
    })),
    cleanup: async () => releaseAssignments(target, repository, assignments),
  };
}

async function releaseAssignments(target: Target, repository: string, assignments: readonly { assignmentId: string }[]): Promise<void> {
  const errors: string[] = [];
  for (const { assignmentId } of assignments) {
    try {
      await requireSuccess({ command: target.command, args: [...target.prefix, "release", assignmentId, "--json"], cwd: repository });
    } catch (error) {
      errors.push(String(error));
    }
  }
  if (errors.length > 0) throw new Error(`failed to release benchmark assignments: ${errors.join("; ")}`);
}

function makeSummary(result: RuntimeBenchmarkResult): string {
  const lines = [
    `Ruk runtime benchmark (${result.platform.os}/${result.platform.architecture})`,
    `samples=${result.sampleCount}, duration=${result.wrapperDurationMs}ms, ttl=${result.assignmentTTLMinutes}m, concurrency=${result.concurrencyLevels.join(",")}`,
  ];
  for (const target of result.targets) {
    lines.push(`${target.name}: binary=${target.binaryBytes}B, cold-median=${target.coldStartMs.median}ms`);
    for (const wrapper of target.wrappers) {
      lines.push(`  c=${wrapper.concurrency}: cold-rss=${wrapper.coldResidentBytes.median}B, idle-rss=${wrapper.idleResidentBytes.median}B, peak-rss=${wrapper.peakResidentBytes.median}B, child-processes=${wrapper.peakChildProcessCount.median}, powershell=${wrapper.peakWindowsPowerShellChildren.median}`);
    }
  }
  const assertion = result.assertions;
  for (const failure of result.failures) {
    lines.push(`Target failure (${failure.target}): ${failure.message}`);
  }
  const reductions = Object.entries(assertion.ramReductionPercentByConcurrency)
    .map(([concurrency, percent]) => `c=${concurrency}:${percent === null ? "n/a" : `${percent.toFixed(1)}%`}`)
    .join(", ");
  lines.push(`Go peak-RSS reduction: ${reductions}`);
  lines.push(`RAM target (>=${assertion.minimumRamReductionPercent}%): ${assertion.ramTargetMet ? "PASS" : "FAIL"}`);
  lines.push(`Windows PowerShell children: ${assertion.applicable ? assertion.observedWindowsPowerShellChildren : "not applicable"} (${assertion.zeroRoutineWindowsPowerShellChildren ? "PASS" : "FAIL"})`);
  return lines.join("\n");
}

export function observedGoPowerShellChildren(targets: readonly TargetBenchmark[]): number {
  const go = targets.find((target) => target.name === "go");
  return (go?.wrappers ?? [])
    .reduce((maximum, wrapper) => Math.max(maximum, wrapper.peakWindowsPowerShellChildren.maximum), 0);
}

export function makeAssertions(
  targets: readonly TargetBenchmark[],
  concurrencyLevels: readonly number[],
  failures: readonly BenchmarkFailure[] = [],
  windowsApplicable = process.platform === "win32",
): RuntimeBenchmarkResult["assertions"] {
  const node = targets.find((target) => target.name === "node");
  const go = targets.find((target) => target.name === "go");
  const failureReasons: string[] = [];
  const ramReductionPercentByConcurrency: Record<string, number | null> = {};
  for (const concurrency of concurrencyLevels) {
    const nodeSample = node?.wrappers.find((wrapper) => wrapper.concurrency === concurrency)?.peakResidentBytes.median;
    const goSample = go?.wrappers.find((wrapper) => wrapper.concurrency === concurrency)?.peakResidentBytes.median;
    ramReductionPercentByConcurrency[String(concurrency)] = nodeSample && nodeSample > 0 && goSample !== undefined
      ? ((nodeSample - goSample) / nodeSample) * 100
      : null;
  }
  const reductions = Object.values(ramReductionPercentByConcurrency);
  const ramTargetMet = reductions.length > 0 && reductions.every((value) => value !== null && value >= 50);
  if (!node || !go) {
    failureReasons.push("RAM comparison unavailable: both runtimes must complete");
  } else if (!ramTargetMet) {
    failureReasons.push("Go did not reduce median peak RSS by at least 50% at every requested concurrency level");
  }
  for (const failure of failures) {
    failureReasons.push(`${failure.target} benchmark failed: ${failure.message}`);
  }
  const observedWindowsPowerShellChildren = observedGoPowerShellChildren(targets);
  const applicable = windowsApplicable;
  const zeroRoutineWindowsPowerShellChildren = !applicable || (go !== undefined && observedWindowsPowerShellChildren === 0);
  if (applicable && !go) {
    failureReasons.push("Windows PowerShell evidence unavailable: Go runtime did not complete");
  } else if (!zeroRoutineWindowsPowerShellChildren) {
    failureReasons.push("routine Windows PowerShell children were observed");
  }
  return {
    minimumRamReductionPercent: 50,
    ramReductionPercentByConcurrency,
    ramTargetMet,
    zeroRoutineWindowsPowerShellChildren,
    observedWindowsPowerShellChildren,
    applicable,
    failureReasons,
  };
}

export async function main(args = process.argv.slice(2)): Promise<RuntimeBenchmarkResult> {
  const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
  const parsed = parseArguments(args, root);
  const requiredInputs = parsed.targets.map((target) => target === "node" ? parsed.nodeCli : parsed.binary);
  await Promise.all(requiredInputs.map((input) => fs.access(input))).catch(() => {
    throw new Error("Build the TypeScript/Node oracle and production Go binary before benchmarking");
  });
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-runtime-benchmark-"));
  try {
    const inspectorPath = path.join(temporary, process.platform === "win32" ? "process-inspector.exe" : "process-inspector");
    await compileInspector(root, inspectorPath);
    const nodeExecutable = process.env["RUK_BENCH_NODE"] ?? "node";
    const targetByName = new Map<TargetName, Target>();
    if (parsed.targets.includes("node")) {
      const nodeVersion = await requireSuccess({ command: nodeExecutable, args: ["--version"], cwd: root });
      targetByName.set("node", {
        name: "node", version: nodeVersion, sizePath: path.dirname(path.dirname(parsed.nodeCli)), command: nodeExecutable,
        prefix: [parsed.nodeCli], childCommand: nodeExecutable, cwd: root,
      });
    }
    if (parsed.targets.includes("go")) {
      const goVersion = await requireSuccess({ command: parsed.binary, args: ["--version"], cwd: root });
      targetByName.set("go", {
        name: "go", version: goVersion, sizePath: parsed.binary, command: parsed.binary,
        prefix: [], childCommand: nodeExecutable, cwd: root,
      });
    }
    const inspector = { command: inspectorPath, args: [], cwd: root };
    const { targets, failures } = await collectTargetResults(parsed.targets, async (name) => {
      const target = targetByName.get(name);
      if (!target) throw new Error(`benchmark target ${name} is not configured`);
      // Keep one clean fixture per runtime target. Every wrapper and sample for
      // that target intentionally shares this repository to preserve contention.
      const repository = await makeRepository(temporary, target, nodeExecutable);
      return benchmarkTarget(target, repository, inspector, parsed.samples, parsed.durationMs, parsed.ttlMinutes, parsed.concurrencyLevels);
    });
    const result = runtimeBenchmarkResult({
      generatedAt: new Date().toISOString(),
      platform: process.platform,
      architecture: process.arch,
      sampleCount: parsed.samples,
      wrapperDurationMs: parsed.durationMs,
      assignmentTTLMinutes: parsed.ttlMinutes,
      concurrencyLevels: [...parsed.concurrencyLevels],
      targets,
      failures,
      assertions: makeAssertions(targets, parsed.concurrencyLevels, failures),
    });
    process.stderr.write(`${makeSummary(result)}\n`);
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    if (parsed.assertTarget && (result.failures.length > 0 || result.assertions.failureReasons.length > 0)) {
      throw new Error(result.assertions.failureReasons.join("; "));
    }
    return result;
  } finally {
    await fs.rm(temporary, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }).catch((error) => {
      process.stderr.write(`Benchmark cleanup retained ${temporary}: ${String(error)}\n`);
    });
  }
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  await main();
}
