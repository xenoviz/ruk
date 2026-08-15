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
import type {
  BuiltCLI,
  ConformanceOptions,
  ConformanceScenario,
  ProcessResult,
  RepositoryFixture,
  ObservedCLIResult,
  ScenarioComparison,
} from "./types.js";

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
    await run("git", ["init", "-q"], { cwd: repository });
    await run("git", ["config", "user.email", "conformance@example.invalid"], { cwd: repository });
    await run("git", ["config", "user.name", "Conformance Harness"], { cwd: repository });
    if (Object.keys(fixture.files ?? {}).length === 0) await fs.writeFile(path.join(repository, ".keep"), "fixture\n");
    await run("git", ["add", "--all"], { cwd: repository });
    await run("git", ["commit", "-qm", "fixture"], { cwd: repository });
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

export function compareOutput(
  scenario: ConformanceScenario,
  typescript: ObservedCLIResult,
  go: ObservedCLIResult,
  roots: readonly string[],
): string[] {
  const context = { roots: roots.map(normalizeRepositoryPath) };
  const differences: string[] = [];
  if (typescript.exitCode !== go.exitCode) differences.push(`exit code: TypeScript ${typescript.exitCode}, Go ${go.exitCode}`);
  if (normalizeText(typescript.stdout, context) !== normalizeText(go.stdout, context)) differences.push("stdout differs");
  if (normalizeText(typescript.stderr, context) !== normalizeText(go.stderr, context)) differences.push("stderr differs");
  if (typescript.stdoutJSON !== null || go.stdoutJSON !== null) {
    if (canonicalJSON(typescript.stdoutJSON, context) !== canonicalJSON(go.stdoutJSON, context)) differences.push("stdout JSON differs");
  }
  if (typescript.stderrJSON !== null || go.stderrJSON !== null) {
    if (canonicalJSON(typescript.stderrJSON, context) !== canonicalJSON(go.stderrJSON, context)) differences.push("stderr JSON differs");
  }
  if (scenario.compareState !== false && canonicalJSON(typescript.state, context) !== canonicalJSON(go.state, context)) differences.push("state differs");
  return differences;
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

  async runScenario(cli: BuiltCLI, scenario: ConformanceScenario, workspace: string): Promise<ObservedCLIResult> {
		const repository = await freshRepository(path.join(workspace, cli.name), scenario.fixture);
    const environment = { ...process.env, ...(scenario.fixture?.env ?? {}) };
    const result = await run(cli.command, [...cli.args, ...scenario.args], { cwd: repository, env: environment, allowFailure: true });
    return observe(result, repository);
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
