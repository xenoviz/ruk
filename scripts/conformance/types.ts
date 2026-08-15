import type { RunResult } from "../../src/process.js";

export type ConformanceDomain = "core" | "lifecycle" | "dependencies" | "ports";

export interface RepositoryFixture {
  files?: Readonly<Record<string, string>>;
  git?: boolean;
  env?: Readonly<Record<string, string>>;
  state?: unknown;
}

export interface ConformanceScenario {
  name: string;
  args: readonly string[];
  domains?: readonly ConformanceDomain[];
  fixture?: RepositoryFixture;
  compareState?: boolean;
  metadata?: Readonly<Record<string, unknown>>;
}

export interface ConformanceOptions {
  root?: string;
  keepTemporary?: boolean;
  typescriptEntry?: string;
  goPackage?: string;
}

export interface BuiltCLI {
  name: "typescript" | "go";
  command: string;
  args: readonly string[];
  environment?: Readonly<Record<string, string>>;
}

export interface ObservedCLIResult {
  exitCode: number;
  stdout: string;
  stderr: string;
  stdoutJSON: unknown | null;
  stderrJSON: unknown | null;
  state: unknown | null;
}

export interface ScenarioComparison {
  scenario: string;
  typescript: ObservedCLIResult;
  go: ObservedCLIResult;
  differences: readonly string[];
}

export type ProcessResult = Pick<RunResult, "code" | "stdout" | "stderr">;
