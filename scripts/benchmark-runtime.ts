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

interface CommandSpec {
  command: string;
  args: string[];
  cwd: string;
}

interface Target {
  name: TargetBenchmark["name"];
  version: string;
  sizePath: string;
  cold: CommandSpec;
  wrappers(concurrency: number, sample: number): Promise<{ commands: CommandSpec[]; cleanup(): Promise<void> }>;
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
    child.stdout.setEncoding("utf8").on("data", (chunk) => { stdout += chunk; });
    child.stderr.setEncoding("utf8").on("data", (chunk) => { stderr += chunk; });
    child.once("error", reject);
    child.once("close", (code) => resolve({ code: code ?? 1, stdout, stderr }));
  });
}

async function requireSuccess(spec: CommandSpec): Promise<string> {
  const result = await run(spec);
  if (result.code !== 0) {
    throw new Error(`${spec.command} ${spec.args.join(" ")} failed: ${result.stderr.trim()}`);
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

async function residentBytes(pids: readonly number[]): Promise<number> {
  if (pids.length === 0) return 0;
  if (process.platform === "linux") {
    const values = await Promise.all(pids.map(async (pid) => {
      try {
        const status = await fs.readFile(`/proc/${pid}/status`, "utf8");
        return Number(/^VmRSS:\s+(\d+)\s+kB$/m.exec(status)?.[1] ?? 0) * 1024;
      } catch {
        return 0;
      }
    }));
    return values.reduce((total, value) => total + value, 0);
  }
  if (process.platform === "darwin") {
    const output = await requireSuccess({
      command: "ps",
      args: ["-o", "rss=", "-p", pids.join(",")],
      cwd: process.cwd(),
    }).catch(() => "");
    return output.split(/\s+/).filter(Boolean).reduce((total, value) => total + Number(value) * 1024, 0);
  }
  const expression = `(Get-Process -Id ${pids.join(",")} -ErrorAction SilentlyContinue | Measure-Object WorkingSet64 -Sum).Sum`;
  const output = await requireSuccess({
    command: "powershell.exe",
    args: ["-NoLogo", "-NoProfile", "-NonInteractive", "-Command", expression],
    cwd: process.cwd(),
  }).catch(() => "0");
  return Number(output) || 0;
}

async function coldStart(spec: CommandSpec): Promise<number> {
  const started = performance.now();
  await requireSuccess(spec);
  return performance.now() - started;
}

async function measureWrappers(commands: readonly CommandSpec[], durationMs: number): Promise<{
  elapsedMs: number;
  idleResidentBytes: number;
  peakResidentBytes: number;
}> {
  const started = performance.now();
  const children = commands.map((spec) => spawn(spec.command, spec.args, {
    cwd: spec.cwd,
    stdio: "ignore",
    windowsHide: true,
  }));
  const completion = Promise.all(children.map((child) => new Promise<number>((resolve, reject) => {
    child.once("error", reject);
    child.once("close", (code) => resolve(code ?? 1));
  })));
  let peakResidentBytes = 0;
  let idleResidentBytes = 0;
  while (children.some(({ exitCode, signalCode }) => exitCode === null && signalCode === null)) {
    const current = await residentBytes(
      children.flatMap(({ pid, exitCode, signalCode }) => pid && exitCode === null && signalCode === null ? [pid] : []),
    );
    peakResidentBytes = Math.max(peakResidentBytes, current);
    if (performance.now() - started >= durationMs / 2) {
      idleResidentBytes = Math.max(idleResidentBytes, current);
    }
    await new Promise((resolve) => setTimeout(resolve, process.platform === "win32" ? 100 : 25));
  }
  const codes = await completion;
  if (codes.some((code) => code !== 0)) {
    throw new Error(`benchmark wrapper exited nonzero: ${codes.join(", ")}`);
  }
  return { elapsedMs: performance.now() - started, idleResidentBytes, peakResidentBytes };
}

async function makeRepository(root: string, nodeTarget: Omit<Target, "wrappers">): Promise<string> {
  const repository = path.join(root, "repository");
  const installer = path.join(root, "install.mjs");
  await fs.mkdir(repository, { recursive: true });
  await fs.writeFile(installer, 'import fs from "node:fs/promises"; await fs.mkdir("node_modules", { recursive: true });\n');
  await fs.writeFile(path.join(repository, "package.json"), '{"name":"ruk-runtime-benchmark"}\n');
  await fs.writeFile(path.join(repository, ".gitignore"), "node_modules/\n");
  await fs.writeFile(path.join(repository, ".rukrc.json"), `${JSON.stringify({
    dependencyMode: "managed",
    installCommand: [process.execPath, installer],
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
  await requireSuccess({ ...nodeTarget.cold, args: [...nodeTarget.cold.args.slice(0, -1), "init"], cwd: repository });
  return repository;
}

function parseArguments(args: readonly string[]): {
  binary: string;
  samples: number;
  durationMs: number;
  concurrencyLevels: number[];
} {
  const value = (name: string) => {
    const index = args.indexOf(name);
    return index < 0 ? undefined : args[index + 1];
  };
  const binary = path.resolve(value("--binary") ?? path.join("artifacts", process.platform === "win32" ? "ruk.exe" : "ruk"));
  const samples = Number(value("--samples") ?? 3);
  const durationMs = Number(value("--duration") ?? 25_000);
  const concurrencyLevels = (value("--concurrency") ?? "1,10,20").split(",").map(Number);
  if (!Number.isSafeInteger(samples) || samples < 1) throw new Error("--samples must be a positive integer");
  if (!Number.isFinite(durationMs) || durationMs < 250) throw new Error("--duration must be at least 250 milliseconds");
  if (concurrencyLevels.length === 0 || concurrencyLevels.some((level) => !Number.isSafeInteger(level) || level < 1)) {
    throw new Error("--concurrency must be a comma-separated list of positive integers");
  }
  return { binary, samples, durationMs, concurrencyLevels };
}

async function benchmarkTarget(
  target: Target,
  samples: number,
  durationMs: number,
  concurrencyLevels: readonly number[],
): Promise<TargetBenchmark> {
  const coldStartMs: number[] = [];
  for (let sample = 0; sample < samples; sample += 1) {
    coldStartMs.push(await coldStart(target.cold));
  }
  const wrappers: ConcurrencyBenchmark[] = [];
  for (const concurrency of concurrencyLevels) {
    const elapsed: number[] = [];
    const idle: number[] = [];
    const peak: number[] = [];
    for (let sample = 0; sample < samples; sample += 1) {
      const workload = await target.wrappers(concurrency, sample);
      try {
        const measurement = await measureWrappers(workload.commands, durationMs);
        elapsed.push(measurement.elapsedMs);
        idle.push(measurement.idleResidentBytes);
        peak.push(measurement.peakResidentBytes);
      } finally {
        await workload.cleanup();
      }
    }
    wrappers.push({
      concurrency,
      elapsedMs: summarizeSamples(elapsed),
      idleResidentBytes: summarizeSamples(idle),
      peakResidentBytes: summarizeSamples(peak),
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

export async function main(args = process.argv.slice(2)): Promise<RuntimeBenchmarkResult> {
  const { binary, samples, durationMs, concurrencyLevels } = parseArguments(args);
  const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
  const nodeCli = path.join(root, "dist", "bin", "ruk.js");
  await Promise.all([fs.access(nodeCli), fs.access(binary)]).catch(() => {
    throw new Error("Build the Node distribution and standalone binary before benchmarking");
  });
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-runtime-benchmark-"));
  try {
    const goBinary = path.join(temporary, process.platform === "win32" ? "go-supervisor.exe" : "go-supervisor");
    await requireSuccess({
      command: "go",
      args: ["build", "-o", goBinary, path.join(root, "experiments", "go-supervisor", "main.go")],
      cwd: root,
    });
    const nodeExecutable = process.env["RUK_BENCH_NODE"] ?? "node";
    const nodeVersion = await requireSuccess({ command: nodeExecutable, args: ["--version"], cwd: root });
    const nodeBase = {
      name: "node" as const,
      version: nodeVersion,
      sizePath: path.join(root, "dist"),
      cold: { command: nodeExecutable, args: [nodeCli, "--version"], cwd: root },
    };
    const repository = await makeRepository(temporary, nodeBase);
    const childArgs = ["-e", `setTimeout(() => {}, ${durationMs})`];

    const rukTarget = (
      name: "node" | "bun-standalone",
      command: string,
      prefix: string[],
      version: string,
      sizePath: string,
    ): Target => ({
      name,
      version,
      sizePath,
      cold: { command, args: [...prefix, "--version"], cwd: root },
      wrappers: async (concurrency, sample) => {
        const assignments: Array<{ path: string; assignmentId: string }> = [];
        for (let index = 0; index < concurrency; index += 1) {
          const output = await requireSuccess({
            command,
            args: [...prefix, "acquire", `benchmark/${name}/${sample}/${index}`, "--ttl", "1", "--json"],
            cwd: repository,
          });
          assignments.push(JSON.parse(output) as { path: string; assignmentId: string });
        }
        return {
          commands: assignments.map(({ path: cwd }) => ({
            command,
            args: [...prefix, "run", "--", nodeExecutable, ...childArgs],
            cwd,
          })),
          cleanup: async () => {
            for (const { assignmentId } of assignments) {
              await requireSuccess({
                command,
                args: [...prefix, "release", assignmentId, "--json"],
                cwd: repository,
              });
            }
          },
        };
      },
    });
    const bunVersion = await requireSuccess({ command: "bun", args: ["--version"], cwd: root });
    const goVersion = await requireSuccess({ command: "go", args: ["version"], cwd: root });
    const targets: Target[] = [
      rukTarget("node", nodeExecutable, [nodeCli], nodeVersion, path.join(root, "dist")),
      rukTarget("bun-standalone", binary, [], `Bun ${bunVersion}`, binary),
      {
        name: "go-supervisor",
        version: goVersion,
        sizePath: goBinary,
        cold: { command: goBinary, args: ["--help"], cwd: root },
        wrappers: async (concurrency, sample) => {
          const commands: CommandSpec[] = [];
          for (let index = 0; index < concurrency; index += 1) {
            const state = path.join(temporary, `go-${sample}-${concurrency}-${index}.json`);
            await fs.writeFile(state, `${JSON.stringify({ assignmentId: `go-${sample}-${index}`, heartbeatAt: new Date(0).toISOString() })}\n`);
            commands.push({
              command: goBinary,
              args: ["--state", state, "--heartbeat", "20s", nodeExecutable, ...childArgs],
              cwd: temporary,
            });
          }
          return { commands, cleanup: async () => {} };
        },
      },
    ];
    const targetResults: TargetBenchmark[] = [];
    for (const target of targets) {
      targetResults.push(await benchmarkTarget(target, samples, durationMs, concurrencyLevels));
    }
    const result = runtimeBenchmarkResult({
      generatedAt: new Date().toISOString(),
      platform: process.platform,
      architecture: process.arch,
      sampleCount: samples,
      wrapperDurationMs: durationMs,
      targets: targetResults,
    });
    process.stdout.write(`${JSON.stringify(result, null, 2)}\n`);
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
