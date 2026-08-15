import fs from "node:fs/promises";
import path from "node:path";
import type { ConformanceDomain, ConformanceScenario, RepositoryFixture } from "./types.js";

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
    if (!isRecord(entry) || typeof entry["name"] !== "string" || !Array.isArray(entry["args"]) || !entry["args"].every((arg) => typeof arg === "string")) {
      throw new Error(`Conformance scenario ${index} must contain a name and string args`);
    }
    const domains = entry["domains"];
    if (domains !== undefined && (!Array.isArray(domains) || !domains.every((domain) => ["core", "lifecycle", "dependencies", "ports"].includes(domain as string)))) {
      throw new Error(`Conformance scenario ${entry["name"]} has invalid domains`);
    }
    const compareState = entry["compareState"];
    if (compareState !== undefined && typeof compareState !== "boolean") throw new Error(`Conformance scenario ${entry["name"]} has invalid compareState`);
    const name = entry["name"] as string;
    const args = entry["args"] as string[];
    const normalizedDomains = domains as ConformanceDomain[] | undefined;
    const fixture = validateFixture(entry["fixture"], index);
    return {
      name,
      args,
      ...(normalizedDomains ? { domains: normalizedDomains } : {}),
      ...(fixture ? { fixture } : {}),
      ...(compareState === undefined ? {} : { compareState }),
      ...(isRecord(entry["metadata"]) ? { metadata: entry["metadata"] } : {}),
    };
  });
}

export async function loadScenarios(file: string): Promise<ConformanceScenario[]> {
  const value: unknown = JSON.parse(await fs.readFile(file, "utf8"));
  return validateScenarios(value);
}

export function defaultScenarioFile(root: string): string {
  return path.join(root, "test", "conformance", "fixtures", "core.json");
}
