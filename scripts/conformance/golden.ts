import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";

export const GOLDEN_SCHEMA_VERSION = 1 as const;
export const GOLDEN_NORMALIZER_VERSION = 3 as const;
export const DEFAULT_SCENARIO_COUNT = 19;

export type GoldenStreamKind = "json" | "text";

export interface GoldenStream {
  readonly kind: GoldenStreamKind;
  readonly value: string;
}

export interface GoldenStep {
  readonly name: string;
  readonly exitCode: number;
  readonly stdout: GoldenStream;
  readonly stderr: GoldenStream;
  readonly state?: string;
}

export interface GoldenScenario {
  readonly name: string;
  readonly steps: readonly GoldenStep[];
  readonly finalState?: string;
}

export interface GoldenScenarioDescriptor {
  readonly name: string;
  readonly stepNames: readonly string[];
}

export interface GoldenFixtureManifest {
  readonly path: string;
  readonly sha256: string;
  readonly scenarios: readonly GoldenScenarioDescriptor[];
}

export interface GoldenOracle {
  readonly runtime: "typescript";
  readonly commit: string;
  readonly version: string;
}

export interface GoldenFile {
  readonly schemaVersion: typeof GOLDEN_SCHEMA_VERSION;
  readonly normalizerVersion: typeof GOLDEN_NORMALIZER_VERSION;
  readonly oracle: GoldenOracle;
  readonly scenarioCount: number;
  readonly scenarioFiles: readonly GoldenFixtureManifest[];
  readonly scenarios: readonly GoldenScenario[];
}

export interface GoldenValidationOptions {
  readonly expectedScenarioCount?: number;
}

export interface CurrentFixtureManifest extends GoldenFixtureManifest {}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function requireRecord(value: unknown, label: string): Record<string, unknown> {
  if (!isRecord(value)) throw new Error(`${label} must be an object`);
  return value;
}

function requireKeys(
  value: Record<string, unknown>,
  required: readonly string[],
  optional: readonly string[],
  label: string,
): void {
  const allowed = new Set([...required, ...optional]);
  for (const key of Object.keys(value)) {
    if (!allowed.has(key)) throw new Error(`${label} contains unknown field ${key}`);
  }
  for (const key of required) {
    if (!Object.hasOwn(value, key)) throw new Error(`${label} is missing ${key}`);
  }
}

function requireString(value: unknown, label: string, allowEmpty = false): string {
  if (typeof value !== "string" || (!allowEmpty && value.length === 0)) {
    throw new Error(`${label} must be a${allowEmpty ? "" : " non-empty"} string`);
  }
  return value;
}

function requireInteger(value: unknown, label: string): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value)) {
    throw new Error(`${label} must be a safe integer`);
  }
  return value;
}

function requireArray(value: unknown, label: string): readonly unknown[] {
  if (!Array.isArray(value)) throw new Error(`${label} must be an array`);
  return value;
}

function assertUnique(values: readonly string[], label: string): void {
  const seen = new Set<string>();
  for (const value of values) {
    if (seen.has(value)) throw new Error(`${label} contains duplicate ${value}`);
    seen.add(value);
  }
}

function canonicalValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalValue);
  if (isRecord(value)) {
    return Object.fromEntries(
      Object.entries(value)
        .sort(([left], [right]) => left.localeCompare(right))
        .map(([key, entry]) => [key, canonicalValue(entry)]),
    );
  }
  return value;
}

function requireCanonicalJSON(value: unknown, label: string): string {
  const text = requireString(value, label);
  let parsed: unknown;
  try {
    parsed = JSON.parse(text) as unknown;
  } catch {
    throw new Error(`${label} must contain valid JSON`);
  }
  const canonical = JSON.stringify(canonicalValue(parsed));
  if (canonical !== text) throw new Error(`${label} must contain canonical JSON`);
  return canonical;
}

function validateStream(value: unknown, label: string): GoldenStream {
  const stream = requireRecord(value, label);
  requireKeys(stream, ["kind", "value"], [], label);
  const kind = requireString(stream["kind"], `${label}.kind`);
  if (kind !== "json" && kind !== "text") throw new Error(`${label}.kind must be json or text`);
  const streamValue = kind === "json"
    ? requireCanonicalJSON(stream["value"], `${label}.value`)
    : requireString(stream["value"], `${label}.value`, true);
  return { kind, value: streamValue };
}

function validateStep(value: unknown, scenarioLabel: string, index: number): GoldenStep {
  const label = `${scenarioLabel}.steps[${index}]`;
  const step = requireRecord(value, label);
  requireKeys(step, ["name", "exitCode", "stdout", "stderr"], ["state"], label);
  const state = step["state"] === undefined
    ? undefined
    : requireCanonicalJSON(step["state"], `${label}.state`);
  return {
    name: requireString(step["name"], `${label}.name`),
    exitCode: requireInteger(step["exitCode"], `${label}.exitCode`),
    stdout: validateStream(step["stdout"], `${label}.stdout`),
    stderr: validateStream(step["stderr"], `${label}.stderr`),
    ...(state === undefined ? {} : { state }),
  };
}

function validateScenario(value: unknown, index: number): GoldenScenario {
  const label = `scenarios[${index}]`;
  const scenario = requireRecord(value, label);
  requireKeys(scenario, ["name", "steps"], ["finalState"], label);
  const rawSteps = requireArray(scenario["steps"], `${label}.steps`);
  if (rawSteps.length === 0) throw new Error(`${label}.steps must not be empty`);
  const steps = rawSteps.map((step, stepIndex) => validateStep(step, label, stepIndex));
  assertUnique(steps.map((step) => step.name), `${label}.steps`);
  const finalState = scenario["finalState"] === undefined
    ? undefined
    : requireCanonicalJSON(scenario["finalState"], `${label}.finalState`);
  return {
    name: requireString(scenario["name"], `${label}.name`),
    steps,
    ...(finalState === undefined ? {} : { finalState }),
  };
}

function validateDescriptor(value: unknown, fileLabel: string, index: number): GoldenScenarioDescriptor {
  const label = `${fileLabel}.scenarios[${index}]`;
  const descriptor = requireRecord(value, label);
  requireKeys(descriptor, ["name", "stepNames"], [], label);
  const rawSteps = requireArray(descriptor["stepNames"], `${label}.stepNames`);
  if (rawSteps.length === 0) throw new Error(`${label}.stepNames must not be empty`);
  const stepNames = rawSteps.map((step, stepIndex) => requireString(step, `${label}.stepNames[${stepIndex}]`));
  assertUnique(stepNames, `${label}.stepNames`);
  return { name: requireString(descriptor["name"], `${label}.name`), stepNames };
}

function validateFixtureManifest(value: unknown, index: number): GoldenFixtureManifest {
  const label = `scenarioFiles[${index}]`;
  const manifest = requireRecord(value, label);
  requireKeys(manifest, ["path", "sha256", "scenarios"], [], label);
  const relativePath = requireString(manifest["path"], `${label}.path`);
  if (path.isAbsolute(relativePath) || relativePath.split(/[\\/]+/).includes("..")) {
    throw new Error(`${label}.path must be a relative path inside the repository`);
  }
  const sha256 = requireString(manifest["sha256"], `${label}.sha256`);
  if (!/^[a-f0-9]{64}$/.test(sha256)) throw new Error(`${label}.sha256 must be a lowercase SHA-256 digest`);
  const scenarios = requireArray(manifest["scenarios"], `${label}.scenarios`)
    .map((scenario, scenarioIndex) => validateDescriptor(scenario, label, scenarioIndex));
  if (scenarios.length === 0) throw new Error(`${label}.scenarios must not be empty`);
  assertUnique(scenarios.map((scenario) => scenario.name), `${label}.scenarios`);
  return { path: relativePath, sha256, scenarios };
}

function validateOracle(value: unknown): GoldenOracle {
  const oracle = requireRecord(value, "oracle");
  requireKeys(oracle, ["runtime", "commit", "version"], [], "oracle");
  if (oracle["runtime"] !== "typescript") throw new Error("oracle.runtime must be typescript");
  return {
    runtime: "typescript",
    commit: requireString(oracle["commit"], "oracle.commit"),
    version: requireString(oracle["version"], "oracle.version"),
  };
}

/** Validate and type-check one frozen conformance golden document. */
export function validateGolden(value: unknown, options: GoldenValidationOptions = {}): GoldenFile {
  const expectedCount = options.expectedScenarioCount ?? DEFAULT_SCENARIO_COUNT;
  if (!Number.isSafeInteger(expectedCount) || expectedCount < 1) {
    throw new Error("expectedScenarioCount must be a positive safe integer");
  }
  const golden = requireRecord(value, "golden document");
  requireKeys(
    golden,
    ["schemaVersion", "normalizerVersion", "oracle", "scenarioCount", "scenarioFiles", "scenarios"],
    [],
    "golden document",
  );
  if (golden["schemaVersion"] !== GOLDEN_SCHEMA_VERSION) throw new Error("unsupported golden schemaVersion");
  if (golden["normalizerVersion"] !== GOLDEN_NORMALIZER_VERSION) throw new Error("unsupported golden normalizerVersion");
  const scenarioCount = requireInteger(golden["scenarioCount"], "scenarioCount");
  if (scenarioCount !== expectedCount) throw new Error(`scenarioCount must be ${expectedCount}`);
  const scenarioFiles = requireArray(golden["scenarioFiles"], "scenarioFiles")
    .map((file, index) => validateFixtureManifest(file, index));
  if (scenarioFiles.length === 0) throw new Error("scenarioFiles must not be empty");
  const scenarios = requireArray(golden["scenarios"], "scenarios")
    .map((scenario, index) => validateScenario(scenario, index));
  if (scenarios.length !== scenarioCount) throw new Error("scenarioCount does not match scenarios length");
  assertUnique(scenarios.map((scenario) => scenario.name), "scenarios");

  const manifestScenarios = scenarioFiles.flatMap((file) => file.scenarios);
  if (manifestScenarios.length !== scenarioCount) throw new Error("scenarioCount does not match fixture manifest scenarios");
  for (let index = 0; index < scenarios.length; index += 1) {
    const scenario = scenarios[index]!;
    const descriptor = manifestScenarios[index]!;
    if (scenario.name !== descriptor.name) throw new Error(`scenario order differs at index ${index}`);
    const stepNames = scenario.steps.map((step) => step.name);
    if (JSON.stringify(stepNames) !== JSON.stringify(descriptor.stepNames)) {
      throw new Error(`step order differs for scenario ${scenario.name}`);
    }
  }
  return {
    schemaVersion: GOLDEN_SCHEMA_VERSION,
    normalizerVersion: GOLDEN_NORMALIZER_VERSION,
    oracle: validateOracle(golden["oracle"]),
    scenarioCount,
    scenarioFiles,
    scenarios,
  };
}

/** Return a lowercase SHA-256 digest for one fixture file. */
export async function sha256File(file: string): Promise<string> {
  const digest = crypto.createHash("sha256").update(await fs.readFile(file)).digest("hex");
  return digest;
}

/** Validate that checked-in fixture files still match the frozen manifest. */
export async function validateFixtureHashes(golden: GoldenFile, repositoryRoot: string): Promise<void> {
  for (const fixture of golden.scenarioFiles) {
    const resolvedRoot = path.resolve(repositoryRoot);
    const file = path.resolve(resolvedRoot, fixture.path);
    if (file !== resolvedRoot && !file.startsWith(`${resolvedRoot}${path.sep}`)) {
      throw new Error(`Fixture manifest path escapes repository: ${fixture.path}`);
    }
    const actual = await sha256File(file);
    if (actual !== fixture.sha256) {
      throw new Error(`Fixture manifest digest changed for ${fixture.path}`);
    }
  }
}

/** Validate fixture paths, hashes, scenario order, and ordered step names. */
export function validateScenarioManifest(
  golden: GoldenFile,
  current: readonly CurrentFixtureManifest[],
  expectedScenarioCount = DEFAULT_SCENARIO_COUNT,
): void {
  if (golden.scenarioCount !== expectedScenarioCount) {
    throw new Error(`golden scenarioCount must be ${expectedScenarioCount}`);
  }
  if (current.length !== golden.scenarioFiles.length) throw new Error("fixture manifest file count differs");
  for (let index = 0; index < current.length; index += 1) {
    const expected = golden.scenarioFiles[index]!;
    const actual = current[index]!;
    if (expected.path !== actual.path || expected.sha256 !== actual.sha256) {
      throw new Error(`fixture manifest differs at index ${index}`);
    }
    if (expected.scenarios.length !== actual.scenarios.length) {
      throw new Error(`scenario manifest differs for ${expected.path}`);
    }
    for (let scenarioIndex = 0; scenarioIndex < actual.scenarios.length; scenarioIndex += 1) {
      const expectedScenario = expected.scenarios[scenarioIndex]!;
      const actualScenario = actual.scenarios[scenarioIndex]!;
      if (expectedScenario.name !== actualScenario.name || JSON.stringify(expectedScenario.stepNames) !== JSON.stringify(actualScenario.stepNames)) {
        throw new Error(`scenario order differs for ${expected.path} at index ${scenarioIndex}`);
      }
    }
  }
}

/** Read and strictly validate one golden document. */
export async function loadGoldenFile(file: string, options: GoldenValidationOptions = {}): Promise<GoldenFile> {
  return validateGolden(JSON.parse(await fs.readFile(file, "utf8")) as unknown, options);
}

/** Atomically write one strictly validated golden document. */
export async function writeGoldenFile(
  file: string,
  value: GoldenFile,
  options: GoldenValidationOptions = {},
): Promise<void> {
  const golden = validateGolden(value, options);
  const target = path.resolve(file);
  await fs.mkdir(path.dirname(target), { recursive: true });
  const temporary = `${target}.${process.pid}.${crypto.randomUUID()}.tmp`;
  try {
    await fs.writeFile(temporary, `${JSON.stringify(golden, null, 2)}\n`, { flag: "wx", mode: 0o644 });
    await fs.rename(temporary, target);
  } finally {
    await fs.rm(temporary, { force: true });
  }
}
