import type { StdioOptions } from "node:child_process";

export type DependencyMode = "managed" | "shared";
export type PackageManagerName = "bun" | "pnpm" | "npm" | "yarn" | string;

export interface RukConfig {
  installCommand: string[] | null;
  dependencyMode: DependencyMode | null;
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
  projectionFingerprint?: string;
  mode: string;
  projections: string[];
  branch: string;
  updatedAt: string;
}

export type WorkspaceLifecycle = "available" | "preparing" | "assigned" | "returning" | "failed";

export interface LeaseKeeperRecord {
  id: string;
  heartbeatAt: string;
  validUntil: string;
}

export interface AssignmentRecord {
  id: string;
  owner: string;
  hostname: string;
  assignedAt: string;
  renewedAt: string;
  expiresAt: string;
  leaseDurationMinutes: number;
  lastActivityAt: string;
  leaseKeepers: LeaseKeeperRecord[];
  ports: Record<string, number>;
}

export interface UsageMetrics {
  acquisitions: number;
  workspaceReuses: number;
  preparations: number;
  preparationSkips: number;
  preparationFailures: number;
  totalPreparationMs: number;
  lastPreparationMs: number | null;
}

export interface TrackedProcessRecord {
  pid: number;
  groupId?: number;
  sessionId?: number;
  sessionStartedAt?: string;
  terminalId?: string;
  command?: string[];
  startedAt: string;
}

export interface WorkspaceRecord {
  path: string;
  managed: true;
  branch: string;
  lifecycle: WorkspaceLifecycle;
  operationId: string | null;
  assignment: AssignmentRecord | null;
  processes: TrackedProcessRecord[];
  createdAt: string;
  updatedAt: string;
  availableAt: string | null;
  failure: string | null;
}

export interface RukState {
  version: 4;
  trees: Record<string, TreeRecord>;
  workspaces: Record<string, WorkspaceRecord>;
  metrics: UsageMetrics;
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
