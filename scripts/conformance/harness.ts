import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "../../src/process.js";
import {
  canonicalJSON,
  normalizeRepositoryPath,
  normalizeText,
  parseJSON,
} from "./normalize.js";
import { scenarioSteps } from "./scenarios.js";
import type {
  BuiltCLI,
  ConformanceOptions,
  ConformanceScenario,
  ConformanceStep,
  ProcessResult,
  RepositoryFixture,
  ObservedCLIResult,
  ObservedScenario,
  ScenarioComparison,
} from "./types.js";

const FIXTURE_COMMIT_DATE = "2026-01-01T00:00:00.000Z";

async function writeFixture(repository: string, fixture: RepositoryFixture): Promise<void> {
  for (const [relative, contents] of Object.entries(fixture.files ?? {})) {
    const target = path.resolve(repository, relative);
    if (target !== repository && !target.startsWith(`${repository}${path.sep}`)) {
      throw new Error(`Fixture path escapes repository: ${relative}`);
    }
    await fs.mkdir(path.dirname(target), { recursive: true });
    await fs.writeFile(target, contents);
  }
}

async function freshRepository(root: string, fixture: RepositoryFixture = {}): Promise<string> {
  const repository = path.resolve(root);
  await fs.mkdir(repository, { recursive: false });
  await writeFixture(repository, fixture);
  if (fixture.git !== false) {
    const gitEnvironment = {
      ...process.env,
      GIT_AUTHOR_DATE: FIXTURE_COMMIT_DATE,
      GIT_COMMITTER_DATE: FIXTURE_COMMIT_DATE,
    };
    await run("git", ["init", "-q"], { cwd: repository });
    await run("git", ["config", "user.email", "conformance@example.invalid"], { cwd: repository, env: gitEnvironment });
    await run("git", ["config", "user.name", "Conformance Harness"], { cwd: repository, env: gitEnvironment });
    if (Object.keys(fixture.files ?? {}).length === 0) await fs.writeFile(path.join(repository, ".keep"), "fixture\n");
    await run("git", ["add", "--all"], { cwd: repository, env: gitEnvironment });
    await run("git", ["commit", "-qm", "fixture"], { cwd: repository, env: gitEnvironment });
    if (fixture.state !== undefined) {
      const statePath = path.join(repository, ".git", "ruk", "state.json");
      await fs.mkdir(path.dirname(statePath), { recursive: true });
      await fs.writeFile(statePath, `${JSON.stringify(fixture.state, null, 2)}\n`);
    }
  }
  return repository;
}

async function readState(repository: string): Promise<unknown | null> {
  const candidates = [
    path.join(repository, ".git", "ruk", "state.json"),
    path.join(repository, "ruk", "state.json"),
  ];
  for (const candidate of candidates) {
    try {
      return JSON.parse(await fs.readFile(candidate, "utf8")) as unknown;
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    }
  }
  return null;
}

function processOutput(result: ProcessResult): ObservedCLIResult {
  return {
    exitCode: result.code,
    stdout: result.stdout,
    stderr: result.stderr,
    stdoutJSON: parseJSON(result.stdout),
    stderrJSON: parseJSON(result.stderr),
    state: null,
  };
}

async function observe(result: ProcessResult, repository: string): Promise<ObservedCLIResult> {
  const observed = processOutput(result);
  observed.state = await readState(repository);
  return observed;
}

function comparisonPrefix(index: number, step: ConformanceStep): string {
  return `step ${index + 1} (${step.name})`;
}

function verboseDifference(label: string, typescript: string, go: string): string {
  if (process.env["RUK_CONFORMANCE_VERBOSE"] !== "1") return label;
  const clip = (value: string): string => value.length <= 2_000 ? value : `${value.slice(0, 2_000)}...<truncated>`;
  return `${label}\n  TypeScript: ${clip(typescript)}\n  Go: ${clip(go)}`;
}

function streamDifference(
  left: string,
  leftJSON: unknown | null,
  right: string,
  rightJSON: unknown | null,
  context: { roots: readonly string[] },
): "text" | "json" | null {
  if (leftJSON !== null || rightJSON !== null) {
    return canonicalJSON(leftJSON, context) === canonicalJSON(rightJSON, context) ? null : "json";
  }
  return normalizeText(left, context) === normalizeText(right, context) ? null : "text";
}

export function compareStepOutput(
  step: ConformanceStep,
  typescript: ObservedCLIResult,
  go: ObservedCLIResult,
  roots: readonly string[],
  index = 0,
): string[] {
  const context = { roots: roots.map(normalizeRepositoryPath) };
  const prefix = comparisonPrefix(index, step);
  const differences: string[] = [];
  if (typescript.exitCode !== go.exitCode) differences.push(`${prefix}: exit code differs`);
  const stdoutDifference = streamDifference(typescript.stdout, typescript.stdoutJSON, go.stdout, go.stdoutJSON, context);
  if (stdoutDifference) differences.push(verboseDifference(
    `${prefix}: stdout${stdoutDifference === "json" ? " JSON" : ""} differs`,
    stdoutDifference === "json" ? canonicalJSON(typescript.stdoutJSON, context) : normalizeText(typescript.stdout, context),
    stdoutDifference === "json" ? canonicalJSON(go.stdoutJSON, context) : normalizeText(go.stdout, context),
  ));
  const stderrDifference = streamDifference(typescript.stderr, typescript.stderrJSON, go.stderr, go.stderrJSON, context);
  if (stderrDifference) differences.push(verboseDifference(
    `${prefix}: stderr${stderrDifference === "json" ? " JSON" : ""} differs`,
    stderrDifference === "json" ? canonicalJSON(typescript.stderrJSON, context) : normalizeText(typescript.stderr, context),
    stderrDifference === "json" ? canonicalJSON(go.stderrJSON, context) : normalizeText(go.stderr, context),
  ));
  const typescriptState = canonicalJSON(typescript.state, context);
  const goState = canonicalJSON(go.state, context);
  if (step.compareState !== false && typescriptState !== goState) {
    differences.push(verboseDifference(`${prefix}: state differs`, typescriptState, goState));
  }
  return differences;
}

export function compareOutput(
  scenario: ConformanceScenario,
  typescript: ObservedScenario,
  go: ObservedScenario,
  roots: readonly string[],
): string[] {
  const steps = scenarioSteps(scenario);
  const differences: string[] = [];
  const typescriptSteps = typescript.steps;
  const goSteps = go.steps;
  if (typescriptSteps.length !== goSteps.length) {
    differences.push(`step count differs: TypeScript ${typescriptSteps.length}, Go ${goSteps.length}`);
  }
  for (let index = 0; index < Math.max(typescriptSteps.length, goSteps.length); index += 1) {
    const step = steps[index] ?? {
      name: `unexpected-${index + 1}`,
      args: [],
      compareState: false,
    };
    const typescriptStep = typescriptSteps[index];
    const goStep = goSteps[index];
    if (!typescriptStep || !goStep) {
      differences.push(`${comparisonPrefix(index, step)}: missing result`);
      continue;
    }
    differences.push(...compareStepOutput(
      scenario.compareState === false ? { ...step, compareState: false } : step,
      typescriptStep,
      goStep,
      roots,
      index,
    ));
  }
  const compareFinalState = scenario.compareFinalState ?? scenario.compareState !== false;
  if (compareFinalState) {
    const context = { roots: roots.map(normalizeRepositoryPath) };
    const typescriptState = canonicalJSON(typescript.finalState, context);
    const goState = canonicalJSON(go.finalState, context);
    if (typescriptState !== goState) {
      differences.push(verboseDifference("final state differs", typescriptState, goState));
    }
  }
  return differences;
}

function resultValue(result: ObservedCLIResult, property: string): string | undefined {
  if (!result.stdoutJSON || typeof result.stdoutJSON !== "object" || Array.isArray(result.stdoutJSON)) return undefined;
  const value = (result.stdoutJSON as Record<string, unknown>)[property];
  return typeof value === "string" || typeof value === "number" ? String(value) : undefined;
}

function sourceOutput(label: string, result: ObservedCLIResult): string {
  const stdout = result.stdout.trim() || "<empty>";
  const stderr = result.stderr.trim() || "<empty>";
  return `${label} exited with code ${result.exitCode}; stdout=${JSON.stringify(stdout)}; stderr=${JSON.stringify(stderr)}`;
}

export function resolveStepArguments(
  args: readonly string[],
  previous: ReadonlyMap<string, ObservedCLIResult>,
  last: ObservedCLIResult | undefined,
): string[] {
  return args.map((argument) => argument.replace(/\$\{([^}]+)\}/g, (_match, expression: string) => {
    const separator = expression.indexOf(".");
    const stepName = separator < 0 ? undefined : expression.slice(0, separator);
    const property = separator < 0 ? expression : expression.slice(separator + 1);
    const sourceLabel = stepName ? `step ${stepName}` : "the previous step";
    const source = stepName ? previous.get(stepName) : last;
    if (!source) {
      throw new Error(`Conformance step reference ${expression} is unavailable: ${sourceLabel} has no result`);
    }
    if (source.exitCode !== 0) {
      throw new Error(`Conformance step reference ${expression} cannot use ${sourceLabel}: ${sourceOutput(sourceLabel, source)}`);
    }
    const value = resultValue(source, property);
    if (value === undefined) {
      throw new Error(`Conformance step reference ${expression} is unavailable: ${sourceOutput(sourceLabel, source)}`);
    }
    return value;
  }));
}

export class ConformanceHarness {
  readonly root: string;
  readonly options: Required<Pick<ConformanceOptions, "typescriptEntry" | "goPackage">>;
  private readonly keepTemporary: boolean;
  private temporaryRoot: string | undefined;

  constructor(options: ConformanceOptions = {}) {
    this.root = path.resolve(options.root ?? fileURLToPath(new URL("../..", import.meta.url)));
    this.options = {
      typescriptEntry: options.typescriptEntry ?? path.join(this.root, "bin", "ruk.ts"),
      goPackage: options.goPackage ?? "./cmd/ruk",
    };
    this.keepTemporary = options.keepTemporary ?? false;
  }

  async build(): Promise<readonly BuiltCLI[]> {
    const buildRoot = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-conformance-build-"));
    this.temporaryRoot = buildRoot;
    const typescriptOutput = path.join(buildRoot, "ruk-ts.js");
    const goOutput = path.join(buildRoot, process.platform === "win32" ? "ruk-go.exe" : "ruk-go");
    await run("bun", ["build", this.options.typescriptEntry, "--target=node", "--outfile", typescriptOutput], { cwd: this.root });
    const packageJSON = JSON.parse(await fs.readFile(path.join(this.root, "package.json"), "utf8")) as { version?: unknown };
    if (typeof packageJSON.version !== "string" || packageJSON.version.length === 0) {
      throw new Error("package.json version is unavailable for conformance build");
    }
    await run("go", [
      "build",
      "-ldflags",
      `-X main.version=${packageJSON.version} -X main.distribution=package`,
      "-o",
      goOutput,
      this.options.goPackage,
    ], { cwd: this.root });
    return [
      { name: "typescript", command: "bun", args: [typescriptOutput] },
      { name: "go", command: goOutput, args: [] },
    ];
  }

  async runScenario(cli: BuiltCLI, scenario: ConformanceScenario, workspace: string): Promise<ObservedScenario> {
    const repository = await freshRepository(path.join(workspace, cli.name), scenario.fixture);
    const environment = { ...process.env, ...(scenario.fixture?.env ?? {}) };
    const outputs: ObservedCLIResult[] = [];
    const previous = new Map<string, ObservedCLIResult>();
    for (const step of scenarioSteps(scenario)) {
      const args = resolveStepArguments(step.args, previous, outputs.at(-1));
      const result = await run(cli.command, [...cli.args, ...args], {
        cwd: repository,
        env: environment,
        allowFailure: true,
      });
      const observed = await observe(result, repository);
      outputs.push(observed);
      previous.set(step.name, observed);
    }
    const final = outputs.at(-1);
    if (!final) throw new Error(`Conformance scenario ${scenario.name} has no executable steps`);
    return { ...final, steps: outputs, finalState: await readState(repository) };
  }

  async compare(scenarios: readonly ConformanceScenario[]): Promise<ScenarioComparison[]> {
    const clients = await this.build();
    const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-conformance-"));
    const comparisons: ScenarioComparison[] = [];
    try {
      for (const scenario of scenarios) {
        const scenarioRoot = await fs.mkdtemp(path.join(temporary, `${scenario.name.replace(/[^a-z0-9-]/gi, "-")}-`));
        const typescript = await this.runScenario(clients[0]!, scenario, scenarioRoot);
        const go = await this.runScenario(clients[1]!, scenario, scenarioRoot);
        comparisons.push({
          scenario: scenario.name,
          typescript,
          go,
          typescriptSteps: typescript.steps,
          goSteps: go.steps,
          differences: compareOutput(scenario, typescript, go, [
            path.join(scenarioRoot, "typescript"),
            path.join(scenarioRoot, "go"),
          ]),
        });
      }
      return comparisons;
    } finally {
      if (!this.keepTemporary) await fs.rm(temporary, { recursive: true, force: true });
      if (this.temporaryRoot && !this.keepTemporary) await fs.rm(this.temporaryRoot, { recursive: true, force: true });
    }
  }
}

export function assertComparisons(comparisons: readonly ScenarioComparison[]): void {
  const failures = comparisons.filter((comparison) => comparison.differences.length > 0);
  if (failures.length === 0) return;
  throw new Error(failures.map((failure) => `${failure.scenario}: ${failure.differences.join(", ")}`).join("\n"));
}
