import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { withDirectoryLock } from "./lock.js";
import type {
  AssignmentRecord,
  RukState,
  StorePaths,
  TrackedProcessRecord,
  TreeRecord,
  UsageMetrics,
  WorkspaceRecord,
} from "./types.js";
import { isErrnoException, isRecord } from "./types.js";

export function storePaths(commonDir: string): StorePaths {
  const root = path.join(commonDir, "ruk");
  return {
    root,
    locks: path.join(root, "locks"),
    state: path.join(root, "state.json"),
    stateLock: path.join(root, "locks", "state.lock"),
  };
}

export function treeLockPath(paths: StorePaths, treePath: string): string {
  return path.join(paths.locks, `workspace-${treeKey(treePath)}.lock`);
}

export function treeKey(treePath: string): string {
  return crypto.createHash("sha256").update(path.resolve(treePath)).digest("hex").slice(0, 20);
}

export function emptyMetrics(): UsageMetrics {
  return {
    acquisitions: 0,
    workspaceReuses: 0,
    preparations: 0,
    preparationSkips: 0,
    preparationFailures: 0,
    totalPreparationMs: 0,
    lastPreparationMs: null,
  };
}

function isTreeRecord(value: unknown): value is TreeRecord {
  return (
    isRecord(value) &&
    typeof value["path"] === "string" &&
    typeof value["fingerprint"] === "string" &&
    typeof value["mode"] === "string" &&
    Array.isArray(value["projections"]) &&
    value["projections"].every((entry) => typeof entry === "string") &&
    typeof value["branch"] === "string" &&
    typeof value["updatedAt"] === "string"
  );
}

function isTimestamp(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const parsed = new Date(value);
  return !Number.isNaN(parsed.valueOf()) && parsed.toISOString() === value;
}

function isUuid(value: unknown): value is string {
  return (
    typeof value === "string" &&
    /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value)
  );
}

function isAssignmentRecord(value: unknown): value is AssignmentRecord {
  return (
    isRecord(value) &&
    isUuid(value["id"]) &&
    typeof value["owner"] === "string" &&
    value["owner"].length > 0 &&
    typeof value["hostname"] === "string" &&
    value["hostname"].length > 0 &&
    isTimestamp(value["assignedAt"]) &&
    isTimestamp(value["renewedAt"]) &&
    isTimestamp(value["expiresAt"]) &&
    Date.parse(value["assignedAt"]) <= Date.parse(value["renewedAt"]) &&
    Date.parse(value["renewedAt"]) < Date.parse(value["expiresAt"]) &&
    isRecord(value["ports"]) &&
    Object.entries(value["ports"]).every(
      ([name, port]) => name.length > 0 && Number.isSafeInteger(port) && (port as number) >= 1 && (port as number) <= 65_535,
    )
  );
}

function isUsageMetrics(value: unknown): value is UsageMetrics {
  if (!isRecord(value)) return false;
  const counters = [
    "acquisitions",
    "workspaceReuses",
    "preparations",
    "preparationSkips",
    "preparationFailures",
    "totalPreparationMs",
  ];
  return (
    counters.every((name) => Number.isSafeInteger(value[name]) && (value[name] as number) >= 0) &&
    (value["lastPreparationMs"] === null ||
      (Number.isSafeInteger(value["lastPreparationMs"]) && (value["lastPreparationMs"] as number) >= 0))
  );
}

function isProcessRecord(value: unknown): value is TrackedProcessRecord {
  return (
    isRecord(value) &&
    Number.isSafeInteger(value["pid"]) &&
    (value["pid"] as number) > 0 &&
    (value["groupId"] === undefined ||
      (Number.isSafeInteger(value["groupId"]) && (value["groupId"] as number) > 0)) &&
    (value["command"] === undefined ||
      (Array.isArray(value["command"]) &&
        value["command"].length > 0 &&
        value["command"].every((entry) => typeof entry === "string"))) &&
    typeof value["startedAt"] === "string" &&
    value["startedAt"].length > 0
  );
}

function isWorkspaceRecord(value: unknown): value is WorkspaceRecord {
  if (
    !isRecord(value) ||
    typeof value["path"] !== "string" ||
    !path.isAbsolute(value["path"]) ||
    value["managed"] !== true ||
    typeof value["branch"] !== "string" ||
    value["branch"].length === 0 ||
    !["available", "preparing", "assigned", "returning", "failed"].includes(
      value["lifecycle"] as string,
    ) ||
    !Array.isArray(value["processes"]) ||
    !value["processes"].every(isProcessRecord) ||
    new Set(value["processes"].map((entry) => entry.pid)).size !== value["processes"].length ||
    !isTimestamp(value["createdAt"]) ||
    !isTimestamp(value["updatedAt"]) ||
    !(value["availableAt"] === null || isTimestamp(value["availableAt"])) ||
    !(value["failure"] === null || typeof value["failure"] === "string")
  ) {
    return false;
  }
  const assigned = value["lifecycle"] === "assigned" || value["lifecycle"] === "returning";
  const operationAllowed =
    value["lifecycle"] === "preparing" ||
    value["lifecycle"] === "available" ||
    value["lifecycle"] === "failed";
  return (
    (operationAllowed
      ? value["operationId"] === null || isUuid(value["operationId"])
      : value["operationId"] === null) &&
    (value["lifecycle"] !== "preparing" || isUuid(value["operationId"])) &&
    (assigned ? isAssignmentRecord(value["assignment"]) : value["assignment"] === null) &&
    (value["lifecycle"] === "available" ? isTimestamp(value["availableAt"]) : value["availableAt"] === null) &&
    (value["lifecycle"] === "failed"
      ? typeof value["failure"] === "string" && value["failure"].length > 0
      : value["lifecycle"] === "assigned"
        ? value["failure"] === null ||
          (typeof value["failure"] === "string" && value["failure"].length > 0)
        : value["failure"] === null) &&
    (assigned || value["processes"].length === 0)
  );
}

function parseState(value: unknown, file: string): RukState {
  if (!isRecord(value) || !isRecord(value["trees"])) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  const trees = value["trees"];
  if (!Object.values(trees).every(isTreeRecord)) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  if (value["version"] === 1) {
    return { version: 3, trees: trees as Record<string, TreeRecord>, workspaces: {}, metrics: emptyMetrics() };
  }
  if ((value["version"] !== 2 && value["version"] !== 3) || !isRecord(value["workspaces"])) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  const workspaces = Object.fromEntries(
    Object.entries(value["workspaces"]).map(([key, workspace]) => {
      if (value["version"] !== 2 || !isRecord(workspace) || !isRecord(workspace["assignment"])) {
        return [key, workspace];
      }
      return [key, { ...workspace, assignment: { ...workspace["assignment"], ports: {} } }];
    }),
  );
  const metrics = value["version"] === 2 ? emptyMetrics() : value["metrics"];
  if (!isUsageMetrics(metrics)) throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  if (
    !Object.entries(workspaces).every(
      ([key, workspace]) => isWorkspaceRecord(workspace) && key === treeKey(workspace.path),
    )
  ) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  const assignmentIds = Object.values(workspaces)
    .filter(isWorkspaceRecord)
    .flatMap((workspace) => (workspace.assignment ? [workspace.assignment.id] : []));
  if (new Set(assignmentIds).size !== assignmentIds.length) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  const operationIds = Object.values(workspaces)
    .filter(isWorkspaceRecord)
    .flatMap((workspace) => (workspace.operationId ? [workspace.operationId] : []));
  if (new Set(operationIds).size !== operationIds.length) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  const ports = Object.values(workspaces)
    .filter(isWorkspaceRecord)
    .flatMap((workspace) => Object.values(workspace.assignment?.ports ?? {}));
  if (new Set(ports).size !== ports.length) {
    throw new Error(`Unsupported or invalid Ruk state in ${file}`);
  }
  return {
    version: 3,
    trees: trees as Record<string, TreeRecord>,
    workspaces: workspaces as Record<string, WorkspaceRecord>,
    metrics,
  };
}

export async function readState(paths: StorePaths): Promise<RukState> {
  try {
    return parseState(JSON.parse(await fs.readFile(paths.state, "utf8")) as unknown, paths.state);
  } catch (error) {
    if (isErrnoException(error) && error.code === "ENOENT") {
      return { version: 3, trees: {}, workspaces: {}, metrics: emptyMetrics() };
    }
    if (error instanceof SyntaxError) {
      throw new Error(`Cannot parse Ruk state in ${paths.state}: ${error.message}`);
    }
    throw error;
  }
}

export async function recordPreparationMetric(
  paths: StorePaths,
  outcome: "prepared" | "skipped" | "failed",
  durationMs: number,
): Promise<void> {
  const elapsed = Math.max(0, Math.round(durationMs));
  await updateState(paths, (state) => {
    state.metrics.lastPreparationMs = elapsed;
    if (outcome === "prepared") {
      state.metrics.preparations += 1;
      state.metrics.totalPreparationMs += elapsed;
    } else if (outcome === "skipped") {
      state.metrics.preparationSkips += 1;
    } else {
      state.metrics.preparationFailures += 1;
    }
  });
}

export async function updateState<T>(
  paths: StorePaths,
  mutate: (state: RukState) => T | Promise<T>,
): Promise<T> {
  return withDirectoryLock(paths.stateLock, async () => {
    await fs.mkdir(paths.root, { recursive: true });
    const state = await readState(paths);
    const result = await mutate(state);
    parseState(state, paths.state);
    const temporary = `${paths.state}.${process.pid}.tmp`;
    await fs.writeFile(temporary, `${JSON.stringify(state, null, 2)}\n`, { mode: 0o600 });
    await fs.rename(temporary, paths.state);
    return result;
  });
}

export async function setTreeState(
  paths: StorePaths,
  treePath: string,
  value: Omit<TreeRecord, "path">,
): Promise<void> {
  return updateState(paths, (state) => {
    state.trees[treeKey(treePath)] = { path: path.resolve(treePath), ...value };
  });
}

export async function deleteTreeState(paths: StorePaths, treePath: string): Promise<void> {
  return updateState(paths, (state) => {
    delete state.trees[treeKey(treePath)];
  });
}
