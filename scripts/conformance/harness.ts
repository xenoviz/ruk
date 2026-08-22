import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "../lib/process.js";
import type { GoldenScenario, GoldenStep, GoldenStream } from "./golden.js";
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
  ProcessResult,
  RepositoryFixture,
  ObservedCLIResult,
  ObservedScenario,
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

function verboseDifference(label: string, expected: string, actual: string): string {
  if (process.env["RUK_CONFORMANCE_VERBOSE"] !== "1") return label;
  const clip = (value: string): string => value.length <= 2_000 ? value : `${value.slice(0, 2_000)}...<truncated>`;
  return `${label}\n  Expected: ${clip(expected)}\n  Go: ${clip(actual)}`;
}

function stream(value: string, parsed: unknown | null, roots: readonly string[]): GoldenStream {
  const context = { roots: roots.map(normalizeRepositoryPath) };
  return parsed !== null
    ? { kind: "json", value: canonicalJSON(parsed, context) }
    : { kind: "text", value: normalizeText(value, context) };
}

function compareStream(label: string, expected: GoldenStream, actual: GoldenStream): string[] {
  if (expected.kind !== actual.kind) return [`${label} kind differs`];
  if (expected.value === actual.value) return [];
  return [verboseDifference(`${label}${expected.kind === "json" ? " JSON" : ""} differs`, expected.value, actual.value)];
}

export function compareGoldenStep(
  expected: GoldenStep,
  actual: ObservedCLIResult,
  roots: readonly string[],
  index = 0,
): string[] {
  const prefix = `step ${index + 1} (${expected.name})`;
  const differences: string[] = [];
  if (expected.exitCode !== actual.exitCode) differences.push(`${prefix}: exit code differs`);
  differences.push(...compareStream(`${prefix}: stdout`, expected.stdout, stream(actual.stdout, actual.stdoutJSON, roots)));
  differences.push(...compareStream(`${prefix}: stderr`, expected.stderr, stream(actual.stderr, actual.stderrJSON, roots)));
  if (expected.state !== undefined) {
    const actualState = canonicalJSON(actual.state, { roots: roots.map(normalizeRepositoryPath) });
    if (expected.state !== actualState) differences.push(verboseDifference(`${prefix}: state differs`, expected.state, actualState));
  }
  return differences;
}

export function compareGoldenScenario(
  expected: GoldenScenario,
  actual: ObservedScenario,
  roots: readonly string[],
): string[] {
  const differences: string[] = [];
  if (expected.steps.length !== actual.steps.length) {
    differences.push(`step count differs: expected ${expected.steps.length}, Go ${actual.steps.length}`);
  }
  for (let index = 0; index < Math.max(expected.steps.length, actual.steps.length); index += 1) {
    const expectedStep = expected.steps[index];
    const actualStep = actual.steps[index];
    if (!expectedStep || !actualStep) {
      differences.push(`step ${index + 1}: missing result`);
      continue;
    }
    differences.push(...compareGoldenStep(expectedStep, actualStep, roots, index));
  }
  if (expected.finalState !== undefined) {
    const actualState = canonicalJSON(actual.finalState, { roots: roots.map(normalizeRepositoryPath) });
    if (expected.finalState !== actualState) differences.push(verboseDifference("final state differs", expected.finalState, actualState));
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
  readonly options: Required<Pick<ConformanceOptions, "goPackage">>;
  private readonly keepTemporary: boolean;
  private temporaryRoot: string | undefined;

  constructor(options: ConformanceOptions = {}) {
    this.root = path.resolve(options.root ?? fileURLToPath(new URL("../..", import.meta.url)));
    this.options = {
      goPackage: options.goPackage ?? "./cmd/ruk",
    };
    this.keepTemporary = options.keepTemporary ?? false;
  }

  async buildGo(): Promise<BuiltCLI> {
    const buildRoot = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-conformance-build-"));
    this.temporaryRoot = buildRoot;
    const goOutput = path.join(buildRoot, process.platform === "win32" ? "ruk-go.exe" : "ruk-go");
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
    return { name: "go", command: goOutput, args: [] };
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

  async compareGolden(scenarios: readonly ConformanceScenario[], golden: readonly GoldenScenario[]): Promise<string[]> {
    const client = await this.buildGo();
    const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-conformance-"));
    const differences: string[] = [];
    try {
      for (let index = 0; index < scenarios.length; index += 1) {
        const scenario = scenarios[index]!;
        const expected = golden[index];
        if (!expected) {
          differences.push(`${scenario.name}: missing frozen golden result`);
          continue;
        }
        if (expected.name !== scenario.name) {
          differences.push(`scenario ${index + 1}: expected ${expected.name}, Go fixture is ${scenario.name}`);
          continue;
        }
        const expectedStepNames = expected.steps.map((step) => step.name);
        const actualStepNames = scenarioSteps(scenario).map((step) => step.name);
        if (JSON.stringify(expectedStepNames) !== JSON.stringify(actualStepNames)) {
          differences.push(`${scenario.name}: frozen step order differs`);
          continue;
        }
        const scenarioRoot = await fs.mkdtemp(path.join(temporary, `${scenario.name.replace(/[^a-z0-9-]/gi, "-")}-`));
        const go = await this.runScenario(client, scenario, scenarioRoot);
        differences.push(...compareGoldenScenario(expected, go, [await realRepositoryRoot(path.join(scenarioRoot, "go"))]).map((difference) => `${scenario.name}: ${difference}`));
      }
      return differences;
    } finally {
      if (!this.keepTemporary) await fs.rm(temporary, { recursive: true, force: true });
      if (this.temporaryRoot && !this.keepTemporary) await fs.rm(this.temporaryRoot, { recursive: true, force: true });
    }
  }
}

// Windows short (8.3) temporary-directory names such as THARUS~1 must not
// leak into comparisons: the CLI resolves its working directory to the long
// path, so the substitution root has to be resolved the same way or <repo>
// replacement never matches.
async function realRepositoryRoot(repository: string): Promise<string> {
  try {
    return await fs.realpath(repository);
  } catch {
    return repository;
  }
}

export function assertGoldenComparisons(differences: readonly string[]): void {
  if (differences.length === 0) return;
  throw new Error(differences.join("\n"));
}
