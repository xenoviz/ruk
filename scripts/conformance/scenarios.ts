import fs from "node:fs/promises";
import path from "node:path";
import type {
  ConformanceDomain,
  ConformanceScenario,
  ConformanceStep,
  RepositoryFixture,
} from "./types.js";

function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function validateFixture(value: unknown, index: number): RepositoryFixture | undefined {
  if (value === undefined) return undefined;
  if (!isRecord(value)) throw new Error(`Conformance fixture ${index} is invalid`);
  const files = value["files"];
  if (files !== undefined && (!isRecord(files) || !Object.values(files).every((file) => typeof file === "string"))) {
    throw new Error(`Conformance fixture ${index} files are invalid`);
  }
  const git = value["git"];
  if (git !== undefined && typeof git !== "boolean") throw new Error(`Conformance fixture ${index} git is invalid`);
  const env = value["env"];
  if (env !== undefined && (!isRecord(env) || !Object.values(env).every((entry) => typeof entry === "string"))) {
    throw new Error(`Conformance fixture ${index} environment is invalid`);
  }
  return {
    ...(files ? { files: files as Record<string, string> } : {}),
    ...(git === undefined ? {} : { git: git as boolean }),
    ...(env ? { env: env as Record<string, string> } : {}),
    ...(Object.hasOwn(value, "state") && value["state"] !== undefined ? { state: value["state"] } : {}),
  };
}

export function validateScenarios(value: unknown): ConformanceScenario[] {
  if (!Array.isArray(value)) throw new Error("Conformance scenario file must contain an array");
  return value.map((entry, index): ConformanceScenario => {
    if (!isRecord(entry) || typeof entry["name"] !== "string") {
      throw new Error(`Conformance scenario ${index} must contain a name`);
    }
    const name = entry["name"] as string;
    const rawArgs = entry["args"];
    const rawSteps = entry["steps"];
    if (rawArgs !== undefined && rawSteps !== undefined) {
      throw new Error(`Conformance scenario ${name} cannot contain both args and steps`);
    }
    if (
      rawArgs !== undefined &&
      (!Array.isArray(rawArgs) || !rawArgs.every((arg) => typeof arg === "string"))
    ) {
      throw new Error(`Conformance scenario ${index} must contain a name and string args`);
    }
    let steps: ConformanceStep[] | undefined;
    if (rawSteps !== undefined) {
      if (!Array.isArray(rawSteps) || rawSteps.length === 0) {
        throw new Error(`Conformance scenario ${name} steps must contain at least one step`);
      }
      steps = rawSteps.map((step, stepIndex): ConformanceStep => {
        if (
          !isRecord(step) ||
          typeof step["name"] !== "string" ||
          !Array.isArray(step["args"]) ||
          !step["args"].every((arg) => typeof arg === "string")
        ) {
          throw new Error(`Conformance scenario ${name} step ${stepIndex} must contain a name and string args`);
        }
        const compareState = step["compareState"];
        if (compareState !== undefined && typeof compareState !== "boolean") {
          throw new Error(`Conformance scenario ${name} step ${stepIndex} has invalid compareState`);
        }
        return {
          name: step["name"] as string,
          args: step["args"] as string[],
          ...(compareState === undefined ? {} : { compareState }),
        };
      });
    } else if (rawArgs === undefined) {
      throw new Error(`Conformance scenario ${name} must contain args or steps`);
    }
    const domains = entry["domains"];
    if (domains !== undefined && (!Array.isArray(domains) || !domains.every((domain) => ["core", "lifecycle", "dependencies", "ports"].includes(domain as string)))) {
      throw new Error(`Conformance scenario ${entry["name"]} has invalid domains`);
    }
    const compareState = entry["compareState"];
    if (compareState !== undefined && typeof compareState !== "boolean") throw new Error(`Conformance scenario ${entry["name"]} has invalid compareState`);
    const compareFinalState = entry["compareFinalState"];
    if (compareFinalState !== undefined && typeof compareFinalState !== "boolean") {
      throw new Error(`Conformance scenario ${entry["name"]} has invalid compareFinalState`);
    }
    const args = rawArgs as string[] | undefined;
    const normalizedDomains = domains as ConformanceDomain[] | undefined;
    const fixture = validateFixture(entry["fixture"], index);
    return {
      name,
      ...(args ? { args } : {}),
      ...(steps ? { steps } : {}),
      ...(normalizedDomains ? { domains: normalizedDomains } : {}),
      ...(fixture ? { fixture } : {}),
      ...(compareState === undefined ? {} : { compareState }),
      ...(compareFinalState === undefined ? {} : { compareFinalState }),
      ...(isRecord(entry["metadata"]) ? { metadata: entry["metadata"] } : {}),
    };
  });
}

export function scenarioSteps(scenario: ConformanceScenario): readonly ConformanceStep[] {
  if (scenario.steps) return scenario.steps;
  return [{ name: scenario.name, args: scenario.args ?? [] }];
}

export async function loadScenarios(file: string): Promise<ConformanceScenario[]> {
  const value: unknown = JSON.parse(await fs.readFile(file, "utf8"));
  return validateScenarios(value);
}

export function defaultScenarioFile(root: string): string {
  return path.join(root, "test", "conformance", "fixtures", "core.json");
}

export function defaultScenarioFiles(root: string): readonly string[] {
  const fixtures = path.join(root, "test", "conformance", "fixtures");
  return [
    path.join(fixtures, "core.json"),
    path.join(fixtures, "lifecycle.json"),
    path.join(fixtures, "state-migrations.json"),
    path.join(fixtures, "configuration.json"),
    path.join(fixtures, "dependencies.json"),
  ];
}
