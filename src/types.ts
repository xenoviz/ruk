import type { StdioOptions } from "node:child_process";

export type DependencyMode = "managed" | "shared";
export type PackageManagerName = "bun" | "pnpm" | "npm" | "yarn" | string;

export interface RukConfig {
  installCommand: string[] | null;
  dependencyMode: DependencyMode;
}

export interface PackageManager {
  name: PackageManagerName;
  command: string[];
  dependencyMode: DependencyMode;
  version?: string;
}

export interface Repository {
  root: string;
  commonDir: string;
}

export interface DependencyReporter {
  write(message: string): void;
  stdio: StdioOptions;
}

export interface FingerprintDetails {
  fingerprint: string;
  files: string[];
  manager: Required<Pick<PackageManager, "name" | "command" | "dependencyMode" | "version">>;
}

export interface TreeRecord {
  path: string;
  fingerprint: string;
  mode: string;
  projections: string[];
  branch: string;
  updatedAt: string;
}

export interface RukState {
  version: 1;
  trees: Record<string, TreeRecord>;
}

export interface StorePaths {
  root: string;
  locks: string;
  state: string;
  stateLock: string;
}

export interface CliIo {
  stdout: NodeJS.WriteStream;
  stderr: NodeJS.WriteStream;
}

export function isErrnoException(error: unknown): error is NodeJS.ErrnoException {
  return error instanceof Error && "code" in error;
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
