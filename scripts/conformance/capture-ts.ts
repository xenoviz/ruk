import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { readPackageVersion } from "../lib/package.js";
import { run } from "../lib/process.js";
import {
  DEFAULT_SCENARIO_COUNT,
  GOLDEN_NORMALIZER_VERSION,
  GOLDEN_SCHEMA_VERSION,
  sha256File,
  writeGoldenFile,
  type GoldenFile,
  type GoldenFixtureManifest,
  type GoldenScenario,
  type GoldenStream,
} from "./golden.js";
import { ConformanceHarness } from "./harness.js";
import { defaultScenarioFiles, loadScenarios, scenarioSteps } from "./scenarios.js";
import type { ConformanceScenario, ObservedCLIResult, ObservedScenario, BuiltCLI } from "./types.js";
import { canonicalJSON, normalizeRepositoryPath, normalizeText, parseJSON } from "./normalize.js";

const root = path.resolve(fileURLToPath(new URL("../..", import.meta.url)));
const defaultOutput = path.join(root, "test", "conformance", "golden.json");

function parseArguments(args: readonly string[]): string {
  let output = defaultOutput;
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (argument !== "--output") throw new Error(`Unsupported capture option ${String(argument)}`);
    const value = args[index + 1];
    if (!value || value.startsWith("--")) throw new Error("--output requires a file path");
    output = path.resolve(value);
    index += 1;
  }
  return output;
}

function stream(value: string, repositoryRoot: string): GoldenStream {
  const context = { roots: [normalizeRepositoryPath(repositoryRoot)] };
  const parsed = parseJSON(value);
  if (parsed !== null) return { kind: "json", value: canonicalJSON(parsed, context) };
  return { kind: "text", value: normalizeText(value, context) };
}

function state(value: unknown, repositoryRoot: string): string {
  return canonicalJSON(value, { roots: [normalizeRepositoryPath(repositoryRoot)] });
}

function captureStep(
  scenario: ConformanceScenario,
  stepIndex: number,
  observed: ObservedCLIResult,
  repositoryRoot: string,
): GoldenScenario["steps"][number] {
  const step = scenarioSteps(scenario)[stepIndex];
  if (!step) throw new Error(`Scenario ${scenario.name} has no step ${stepIndex + 1}`);
  const compareState = scenario.compareState !== false && step.compareState !== false;
  return {
    name: step.name,
    exitCode: observed.exitCode,
    stdout: stream(observed.stdout, repositoryRoot),
    stderr: stream(observed.stderr, repositoryRoot),
    ...(compareState ? { state: state(observed.state, repositoryRoot) } : {}),
  };
}

function captureScenario(
  scenario: ConformanceScenario,
  observed: ObservedScenario,
  repositoryRoot: string,
): GoldenScenario {
  const compareFinalState = scenario.compareFinalState ?? scenario.compareState !== false;
  return {
    name: scenario.name,
    steps: observed.steps.map((step, index) => captureStep(scenario, index, step, repositoryRoot)),
    ...(compareFinalState ? { finalState: state(observed.finalState, repositoryRoot) } : {}),
  };
}

function descriptor(scenarios: readonly ConformanceScenario[]): GoldenFixtureManifest["scenarios"] {
  return scenarios.map((scenario) => ({
    name: scenario.name,
    stepNames: scenarioSteps(scenario).map((step) => step.name),
  }));
}

async function loadFixtureManifest(files: readonly string[]): Promise<{
  manifests: readonly GoldenFixtureManifest[];
  scenarios: readonly ConformanceScenario[];
}> {
  const manifests: GoldenFixtureManifest[] = [];
  const scenarios: ConformanceScenario[] = [];
  for (const file of files) {
    const loaded = await loadScenarios(file);
    const relative = path.relative(root, file).replaceAll("\\", "/");
    manifests.push({ path: relative, sha256: await sha256File(file), scenarios: descriptor(loaded) });
    scenarios.push(...loaded);
  }
  return { manifests, scenarios };
}

async function capture(output: string): Promise<void> {
  const files = defaultScenarioFiles(root);
  const loaded = await loadFixtureManifest(files);
  if (loaded.scenarios.length !== DEFAULT_SCENARIO_COUNT) {
    throw new Error(`Expected exactly ${DEFAULT_SCENARIO_COUNT} conformance scenarios, found ${loaded.scenarios.length}`);
  }
  const names = loaded.scenarios.map((scenario) => scenario.name);
  if (new Set(names).size !== names.length) throw new Error("Conformance scenario names must be unique");

  const buildRoot = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-conformance-capture-build-"));
  const workspaceRoot = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-conformance-capture-"));
  try {
    const typescriptOutput = path.join(buildRoot, process.platform === "win32" ? "ruk-ts.js" : "ruk-ts.js");
    await run("bun", [
      "build",
      path.join(root, "bin", "ruk.ts"),
      "--target=node",
      "--outfile",
      typescriptOutput,
    ], { cwd: root });
    const oracle: BuiltCLI = { name: "typescript", command: "bun", args: [typescriptOutput] };
    const harness = new ConformanceHarness({ root });
    const scenarios: GoldenScenario[] = [];
    for (const scenario of loaded.scenarios) {
      const scenarioRoot = await fs.mkdtemp(path.join(workspaceRoot, `${scenario.name.replace(/[^a-z0-9-]/gi, "-")}-`));
      const repositoryRoot = path.join(scenarioRoot, oracle.name);
      const observed = await harness.runScenario(oracle, scenario, scenarioRoot);
      scenarios.push(captureScenario(scenario, observed, repositoryRoot));
    }
    const golden: GoldenFile = {
      schemaVersion: GOLDEN_SCHEMA_VERSION,
      normalizerVersion: GOLDEN_NORMALIZER_VERSION,
      oracle: {
        runtime: "typescript",
        commit: (await run("git", ["rev-parse", "HEAD"], { cwd: root })).stdout.trim(),
        version: await readPackageVersion(root),
      },
      scenarioCount: loaded.scenarios.length,
      scenarioFiles: loaded.manifests,
      scenarios,
    };
    await writeGoldenFile(output, golden);
    process.stdout.write(`Captured ${scenarios.length} TypeScript conformance scenarios to ${output}.\n`);
  } finally {
    await fs.rm(buildRoot, { recursive: true, force: true });
    await fs.rm(workspaceRoot, { recursive: true, force: true });
  }
}

const output = parseArguments(process.argv.slice(2));
await capture(output);
