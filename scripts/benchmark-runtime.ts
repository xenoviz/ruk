import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  runtimeBenchmarkResult,
  summarizeSamples,
} from "./runtime-benchmark-schema.js";
import type {
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

function parseArguments(args: readonly string[], root: string): ParsedArguments {
  const value = (name: string): string | undefined => {
    const index = args.indexOf(name);
    return index < 0 ? undefined : args[index + 1];
  };
  const nodeCli = path.resolve(value("--node") ?? path.join(root, "dist", "bin", "ruk.js"));
  const binary = path.resolve(value("--binary") ?? path.join(root, "artifacts", process.platform === "win32" ? "ruk.exe" : "ruk"));
  const samples = Number(value("--samples") ?? 3);
  const durationMs = Number(value("--duration") ?? 12_000);
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
  return { nodeCli, binary, samples, durationMs, ttlMinutes, concurrencyLevels, assertTarget };
}

async function makeRepository(root: string, node: Target, nodeExecutable: string): Promise<string> {
  const repository = path.join(root, "repository");
  const installer = path.join(root, "install.mjs");
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
  await requireSuccess({ command: node.command, args: [...node.prefix, "init"], cwd: repository });
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

function isPowerShell(name: string): boolean {
  const normalized = name.toLowerCase().replaceAll("\\", "/").split("/").at(-1) ?? "";
  return normalized === "powershell.exe" || normalized === "pwsh.exe" || normalized === "powershell" || normalized === "pwsh";
}

async function measureWrappers(
  commands: readonly CommandSpec[],
  durationMs: number,
  inspector: InspectorTarget,
): Promise<Measurement> {
  const started = performance.now();
  const children = commands.map((spec) => spawn(spec.command, spec.args, {
    cwd: spec.cwd,
    stdio: ["ignore", "pipe", "pipe"],
    windowsHide: true,
  }));
  const completion = Promise.all(children.map((child) => new Promise<{ code: number; stdout: string; stderr: string }>((resolve, reject) => {
    let stdout = "";
    let stderr = "";
    child.stdout.setEncoding("utf8").on("data", (chunk: string) => { stdout += chunk; });
    child.stderr.setEncoding("utf8").on("data", (chunk: string) => { stderr += chunk; });
    child.once("error", reject);
    child.once("close", (code) => resolve({ code: code ?? 1, stdout, stderr }));
  })));
  let idleResidentBytes = 0;
  let coldResidentBytes = 0;
  let idleChildProcessCount = 0;
  let peakResidentBytes = 0;
  let peakChildProcessCount = 0;
  let peakWindowsPowerShellChildren = 0;
  let idleCaptured = false;
  let coldCaptured = false;
  const deadline = started + durationMs + 30_000;
  while (children.some(({ exitCode, signalCode }) => exitCode === null && signalCode === null)) {
    if (performance.now() > deadline) {
      for (const child of children) child.kill();
      throw new Error(`benchmark workload exceeded ${Math.round((deadline - started) / 1000)} seconds`);
    }
    const rootPids = children.flatMap(({ pid, exitCode, signalCode }) => (
      pid && exitCode === null && signalCode === null ? [pid] : []
    ));
    if (rootPids.length > 0) {
      const report = await inspect(inspector, rootPids);
      const rootSet = new Set(rootPids);
      // Compare Ruk wrapper overhead, not the workload process both runtimes
      // are supervising. Descendants remain in the report for process-count
      // and PowerShell-storm detection.
      const residentBytes = report.processes
        .filter((process) => rootSet.has(process.pid))
        .reduce((total, process) => total + process.rssBytes, 0);
      const childProcesses = report.processes.filter((process) => !rootSet.has(process.pid));
      const childProcessCount = childProcesses.length;
      const powershellChildren = childProcesses.filter((process) => isPowerShell(process.name)).length;
      peakResidentBytes = Math.max(peakResidentBytes, residentBytes);
      peakChildProcessCount = Math.max(peakChildProcessCount, childProcessCount);
      peakWindowsPowerShellChildren = Math.max(peakWindowsPowerShellChildren, powershellChildren);
      if (!coldCaptured && report.processes.length > 0) {
        coldResidentBytes = residentBytes;
        coldCaptured = true;
      }
      if (!idleCaptured && performance.now() - started >= durationMs / 4 && report.processes.length > 0) {
        idleResidentBytes = residentBytes;
        idleChildProcessCount = childProcessCount;
        idleCaptured = true;
      }
    }
    await new Promise((resolve) => setTimeout(resolve, process.platform === "win32" ? 100 : 50));
  }
  const results = await completion;
  const failures = results
    .map((result, index) => ({ ...result, index }))
    .filter((result) => result.code !== 0);
  if (failures.length > 0) {
    throw new Error(`benchmark wrapper failures: ${failures.map((failure) => (
      `#${failure.index + 1} code=${failure.code} stderr=${JSON.stringify(failure.stderr.trim())} stdout=${JSON.stringify(failure.stdout.trim())}`
    )).join("; ")}`);
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
        const measurement = await measureWrappers(workload.commands, durationMs, inspector);
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
  const reductions = Object.entries(assertion.ramReductionPercentByConcurrency)
    .map(([concurrency, percent]) => `c=${concurrency}:${percent === null ? "n/a" : `${percent.toFixed(1)}%`}`)
    .join(", ");
  lines.push(`Go peak-RSS reduction: ${reductions}`);
  lines.push(`RAM target (>=${assertion.minimumRamReductionPercent}%): ${assertion.ramTargetMet ? "PASS" : "FAIL"}`);
  lines.push(`Windows PowerShell children: ${assertion.applicable ? assertion.observedWindowsPowerShellChildren : "not applicable"} (${assertion.zeroRoutineWindowsPowerShellChildren ? "PASS" : "FAIL"})`);
  return lines.join("\n");
}

function makeAssertions(targets: readonly TargetBenchmark[], concurrencyLevels: readonly number[]): RuntimeBenchmarkResult["assertions"] {
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
  if (!ramTargetMet) failureReasons.push("Go did not reduce median peak RSS by at least 50% at every requested concurrency level");
  const observedWindowsPowerShellChildren = targets.flatMap((target) => target.wrappers)
    .reduce((maximum, wrapper) => Math.max(maximum, wrapper.peakWindowsPowerShellChildren.maximum), 0);
  const applicable = process.platform === "win32";
  const zeroRoutineWindowsPowerShellChildren = !applicable || observedWindowsPowerShellChildren === 0;
  if (!zeroRoutineWindowsPowerShellChildren) failureReasons.push("routine Windows PowerShell children were observed");
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
  await Promise.all([fs.access(parsed.nodeCli), fs.access(parsed.binary)]).catch(() => {
    throw new Error("Build the TypeScript/Node oracle and production Go binary before benchmarking");
  });
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-runtime-benchmark-"));
  try {
    const inspectorPath = path.join(temporary, process.platform === "win32" ? "process-inspector.exe" : "process-inspector");
    await compileInspector(root, inspectorPath);
    const nodeExecutable = process.env["RUK_BENCH_NODE"] ?? "node";
    const nodeVersion = await requireSuccess({ command: nodeExecutable, args: ["--version"], cwd: root });
    const node: Target = {
      name: "node", version: nodeVersion, sizePath: path.dirname(path.dirname(parsed.nodeCli)), command: nodeExecutable,
      prefix: [parsed.nodeCli], childCommand: nodeExecutable, cwd: root,
    };
    const repository = await makeRepository(temporary, node, nodeExecutable);
    const goVersion = await requireSuccess({ command: parsed.binary, args: ["--version"], cwd: root });
    const go: Target = {
      name: "go", version: goVersion, sizePath: parsed.binary, command: parsed.binary,
      prefix: [], childCommand: nodeExecutable, cwd: root,
    };
    const inspector = { command: inspectorPath, args: [], cwd: root };
    const targets = [
      await benchmarkTarget(node, repository, inspector, parsed.samples, parsed.durationMs, parsed.ttlMinutes, parsed.concurrencyLevels),
      await benchmarkTarget(go, repository, inspector, parsed.samples, parsed.durationMs, parsed.ttlMinutes, parsed.concurrencyLevels),
    ];
    const result = runtimeBenchmarkResult({
      generatedAt: new Date().toISOString(),
      platform: process.platform,
      architecture: process.arch,
      sampleCount: parsed.samples,
      wrapperDurationMs: parsed.durationMs,
      assignmentTTLMinutes: parsed.ttlMinutes,
      concurrencyLevels: [...parsed.concurrencyLevels],
      targets,
      assertions: makeAssertions(targets, parsed.concurrencyLevels),
    });
    process.stderr.write(`${makeSummary(result)}\n`);
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
    if (parsed.assertTarget && result.assertions.failureReasons.length > 0) {
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
