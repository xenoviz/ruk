import crypto from "node:crypto";
import path from "node:path";
import { withDirectoryLock } from "./lock.js";
import { availablePort, portEnvironmentName, releaseHostPorts, withHostPortRegistry } from "./ports.js";
import { primaryCheckoutLockPath, readState, treeKey, updateState } from "./state.js";
import type {
  AssignmentRecord,
  StorePaths,
  TrackedProcessRecord,
  WorkspaceRecord,
} from "./types.js";

export interface AssignmentInput {
  owner: string;
  hostname: string;
  expiresAt: string;
  branch?: string;
  now?: string;
}

export interface PreparingWorkspaceInput {
  path: string;
  branch: string;
  now?: string;
}

export interface AssignmentMatch {
  workspace: WorkspaceRecord;
  assignment: AssignmentRecord;
  expired: boolean;
}

export interface AssignmentQuery {
  id?: string;
  owner?: string;
  hostname?: string;
  now?: string;
}

export interface AssignmentActivityInput {
  keeperId: string;
  validUntil: string;
  now?: string;
  lockTimeoutMs?: number;
}

export type GcCandidateReason =
  | "available"
  | "failed"
  | "expired-assignment"
  | "abandoned-preparation"
  | "abandoned-acquisition"
  | "interrupted-collection";

export interface GcCandidate {
  workspace: WorkspaceRecord;
  reason: GcCandidateReason;
  requiresForce: boolean;
}

function timestamp(value: string | undefined, name: string): string {
  const result = new Date(value ?? Date.now());
  if (Number.isNaN(result.valueOf())) throw new Error(`${name} must be a valid timestamp`);
  return result.toISOString();
}

function nonempty(value: string, name: string): string {
  if (value.length === 0) throw new Error(`${name} must not be empty`);
  return value;
}

function uuid(value: string, name: string): string {
  if (!/^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i.test(value)) {
    throw new Error(`${name} must be a UUID`);
  }
  return value;
}

function recordActivity(workspace: WorkspaceRecord, now: string): void {
  const current = workspace.assignment!;
  const expiresAt = new Date(
    Date.parse(now) + current.leaseDurationMinutes * 60_000,
  ).toISOString();
  current.renewedAt = now;
  current.expiresAt = expiresAt;
  current.lastActivityAt = now;
  workspace.updatedAt = now;
}

export function assignmentIsAutoRenewing(
  assignmentRecord: AssignmentRecord,
  now?: string,
): boolean {
  const observedAt = Date.parse(timestamp(now, "now"));
  return assignmentRecord.leaseKeepers.some(
    ({ validUntil }) => Date.parse(validUntil) > observedAt,
  );
}

function assignment(input: AssignmentInput): AssignmentRecord {
  const now = timestamp(input.now, "now");
  const expiresAt = timestamp(input.expiresAt, "expiresAt");
  if (Date.parse(expiresAt) <= Date.parse(now)) throw new Error("expiresAt must be after now");
  return {
    id: crypto.randomUUID(),
    owner: nonempty(input.owner, "owner"),
    hostname: nonempty(input.hostname, "hostname"),
    assignedAt: now,
    renewedAt: now,
    expiresAt,
    leaseDurationMinutes: (Date.parse(expiresAt) - Date.parse(now)) / 60_000,
    lastActivityAt: now,
    leaseKeepers: [],
    ports: {},
  };
}

function findByAssignment(
  workspaces: Record<string, WorkspaceRecord>,
  assignmentId: string,
): WorkspaceRecord {
  const workspace = Object.values(workspaces).find((entry) => entry.assignment?.id === assignmentId);
  if (!workspace) throw new Error(`Assignment ${assignmentId} does not exist`);
  return workspace;
}

function requireLifecycle(
  workspace: WorkspaceRecord,
  expected: WorkspaceRecord["lifecycle"],
): void {
  if (workspace.lifecycle !== expected) {
    throw new Error(`Workspace ${workspace.path} is ${workspace.lifecycle}, expected ${expected}`);
  }
}

function assign(workspace: WorkspaceRecord, input: AssignmentInput, operationId: string | null = null): WorkspaceRecord {
  const nextAssignment = assignment(input);
  if (input.branch !== undefined) workspace.branch = nonempty(input.branch, "branch");
  workspace.lifecycle = "assigned";
  workspace.operationId = operationId;
  workspace.assignment = nextAssignment;
  workspace.updatedAt = nextAssignment.assignedAt;
  workspace.availableAt = null;
  workspace.failure = null;
  return workspace;
}

export async function recordPreparingWorkspace(
  paths: StorePaths,
  input: PreparingWorkspaceInput,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspacePath = path.resolve(input.path);
    const key = treeKey(workspacePath);
    if (state.workspaces[key]) throw new Error(`Workspace ${workspacePath} is already managed`);
    const now = timestamp(input.now, "now");
    const workspace: WorkspaceRecord = {
      path: workspacePath,
      managed: true,
      branch: nonempty(input.branch, "branch"),
      lifecycle: "preparing",
      operationId: crypto.randomUUID(),
      assignment: null,
      processes: [],
      createdAt: now,
      updatedAt: now,
      availableAt: null,
      failure: null,
    };
    state.workspaces[key] = workspace;
    return workspace;
  });
}

export async function markWorkspaceAvailable(
  paths: StorePaths,
  workspacePath: string,
  operationId: string,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const resolved = path.resolve(workspacePath);
    const workspace = state.workspaces[treeKey(resolved)];
    if (!workspace) throw new Error(`Workspace ${resolved} is not managed`);
    requireLifecycle(workspace, "preparing");
    if (workspace.operationId !== operationId) throw new Error("Preparation operation does not match");
    const availableAt = timestamp(now, "now");
    workspace.lifecycle = "available";
    workspace.operationId = null;
    workspace.updatedAt = availableAt;
    workspace.availableAt = availableAt;
    return workspace;
  });
}

export async function reserveAvailableWorkspace(
  paths: StorePaths,
  input: AssignmentInput,
  workspacePath?: string,
): Promise<WorkspaceRecord | null> {
  const requestedKey = workspacePath === undefined ? undefined : treeKey(path.resolve(workspacePath));
  return withDirectoryLock(path.join(paths.root, "warm.lock"), () =>
    withDirectoryLock(primaryCheckoutLockPath(paths), () => updateState(paths, (state) => {
      const workspace = Object.values(state.workspaces)
        .filter(
          (entry) =>
            entry.lifecycle === "available" &&
            entry.operationId === null &&
            (requestedKey === undefined || treeKey(entry.path) === requestedKey),
        )
        .sort(
          (left, right) =>
            (left.availableAt ?? "").localeCompare(right.availableAt ?? "") ||
            left.path.localeCompare(right.path),
        )[0];
      if (!workspace) return null;
      return assign(workspace, input, crypto.randomUUID());
    })));
}

export async function markWorkspaceAssigned(
  paths: StorePaths,
  workspacePath: string,
  operationId: string,
  input: AssignmentInput,
): Promise<WorkspaceRecord> {
  return withDirectoryLock(primaryCheckoutLockPath(paths), () => updateState(paths, (state) => {
    const resolved = path.resolve(workspacePath);
    const workspace = state.workspaces[treeKey(resolved)];
    if (!workspace) throw new Error(`Workspace ${resolved} is not managed`);
    requireLifecycle(workspace, "preparing");
    if (workspace.operationId !== operationId) throw new Error("Preparation operation does not match");
    return assign(workspace, input, crypto.randomUUID());
  }));
}

export async function recordSuccessfulAcquisition(
  paths: StorePaths,
  assignmentId: string,
  operationId: string,
  reused: boolean,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "assigned");
    if (workspace.operationId !== operationId) throw new Error("Acquisition operation does not match");
    workspace.operationId = null;
    workspace.updatedAt = timestamp(now, "now");
    state.metrics.acquisitions += 1;
    if (reused) state.metrics.workspaceReuses += 1;
    return workspace;
  });
}

export async function allocateAssignmentPorts(
  paths: StorePaths,
  assignmentId: string,
  names: readonly string[],
  allocate: (excluded: ReadonlySet<number>) => Promise<number> = availablePort,
): Promise<WorkspaceRecord> {
  const environmentNames = names.map(portEnvironmentName);
  if (new Set(environmentNames).size !== environmentNames.length) {
    throw new Error("Port names must be unique after normalization");
  }
  return withHostPortRegistry((host) => updateState(paths, async (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "assigned");
    const excluded = new Set([
      ...host.reserved,
      ...Object.values(state.workspaces).flatMap((entry) => Object.values(entry.assignment?.ports ?? {})),
    ]);
    const ports: Record<string, number> = Object.create(null);
    for (const name of names) {
      const port = await allocate(excluded);
      if (!Number.isSafeInteger(port) || port < 1 || port > 65_535 || excluded.has(port)) {
        throw new Error(`Port allocator returned unavailable port ${port}`);
      }
      excluded.add(port);
      ports[name] = port;
      host.reserve(port, assignmentId, paths.state);
    }
    await host.commit();
    workspace.assignment!.ports = ports;
    return workspace;
  }));
}

export async function markWorkspaceFailed(
  paths: StorePaths,
  workspacePath: string,
  operationId: string,
  failure: string,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const resolved = path.resolve(workspacePath);
    const workspace = state.workspaces[treeKey(resolved)];
    if (!workspace) throw new Error(`Workspace ${resolved} is not managed`);
    requireLifecycle(workspace, "preparing");
    if (workspace.operationId !== operationId) throw new Error("Preparation operation does not match");
    workspace.lifecycle = "failed";
    workspace.operationId = null;
    workspace.failure = nonempty(failure, "failure");
    workspace.updatedAt = timestamp(now, "now");
    return workspace;
  });
}

export async function renewAssignment(
  paths: StorePaths,
  assignmentId: string,
  expiresAt: string,
  now?: string,
  expectedRenewedAt?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "assigned");
    if (expectedRenewedAt && workspace.assignment!.renewedAt !== expectedRenewedAt) return workspace;
    const renewedAt = timestamp(now, "now");
    const nextExpiry = timestamp(expiresAt, "expiresAt");
    if (Date.parse(nextExpiry) <= Date.parse(renewedAt)) {
      throw new Error("expiresAt must be after now");
    }
    workspace.assignment!.renewedAt = renewedAt;
    workspace.assignment!.expiresAt = nextExpiry;
    workspace.assignment!.leaseDurationMinutes =
      (Date.parse(nextExpiry) - Date.parse(renewedAt)) / 60_000;
    workspace.assignment!.lastActivityAt = renewedAt;
    workspace.updatedAt = renewedAt;
    return workspace;
  });
}

export async function beginAssignmentActivity(
  paths: StorePaths,
  assignmentId: string,
  input: AssignmentActivityInput,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "assigned");
    const now = timestamp(input.now, "now");
    const validUntil = timestamp(input.validUntil, "validUntil");
    if (Date.parse(validUntil) <= Date.parse(now)) {
      throw new Error("validUntil must be after now");
    }
    const keeperId = uuid(input.keeperId, "keeperId");
    if (workspace.assignment!.leaseKeepers.some(({ id }) => id === keeperId)) {
      throw new Error(`Lease keeper ${keeperId} is already active`);
    }
    workspace.assignment!.leaseKeepers = workspace.assignment!.leaseKeepers.filter(
      (keeper) => Date.parse(keeper.validUntil) > Date.parse(now),
    );
    workspace.assignment!.leaseKeepers.push({ id: keeperId, heartbeatAt: now, validUntil });
    recordActivity(workspace, now);
    return workspace;
  });
}

export async function refreshAssignmentActivity(
  paths: StorePaths,
  assignmentId: string,
  input: AssignmentActivityInput,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "assigned");
    const now = timestamp(input.now, "now");
    const validUntil = timestamp(input.validUntil, "validUntil");
    if (Date.parse(validUntil) <= Date.parse(now)) {
      throw new Error("validUntil must be after now");
    }
    const keeperId = uuid(input.keeperId, "keeperId");
    const keeper = workspace.assignment!.leaseKeepers.find(({ id }) => id === keeperId);
    if (!keeper) throw new Error(`Lease keeper ${keeperId} is not active`);
    keeper.heartbeatAt = now;
    keeper.validUntil = validUntil;
    workspace.assignment!.leaseKeepers = workspace.assignment!.leaseKeepers.filter(
      (candidate) => candidate.id === keeperId || Date.parse(candidate.validUntil) > Date.parse(now),
    );
    recordActivity(workspace, now);
    return workspace;
  }, input.lockTimeoutMs === undefined ? undefined : { timeoutMs: input.lockTimeoutMs });
}

export async function finishAssignmentActivity(
  paths: StorePaths,
  assignmentId: string,
  keeperId: string,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "assigned");
    const completedAt = timestamp(now, "now");
    const expectedId = uuid(keeperId, "keeperId");
    const index = workspace.assignment!.leaseKeepers.findIndex(({ id }) => id === expectedId);
    if (index < 0) throw new Error(`Lease keeper ${expectedId} is not active`);
    workspace.assignment!.leaseKeepers.splice(index, 1);
    workspace.assignment!.leaseKeepers = workspace.assignment!.leaseKeepers.filter(
      (keeper) => Date.parse(keeper.validUntil) > Date.parse(completedAt),
    );
    recordActivity(workspace, completedAt);
    return workspace;
  });
}

export async function beginWorkspaceReturn(
  paths: StorePaths,
  assignmentId: string,
  now?: string,
  requireExpiredBy?: string,
  acquisitionOperationId?: string,
  expectedUpdatedAt?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    if (workspace.lifecycle === "returning") return workspace;
    requireLifecycle(workspace, "assigned");
    if (workspace.operationId !== null && workspace.operationId !== acquisitionOperationId) {
      throw new Error(`Assignment ${assignmentId} acquisition is still in progress`);
    }
    if (acquisitionOperationId !== undefined && workspace.operationId !== acquisitionOperationId) {
      throw new Error(`Assignment ${assignmentId} acquisition operation does not match`);
    }
    if (expectedUpdatedAt !== undefined && workspace.updatedAt !== expectedUpdatedAt) {
      throw new Error(`Assignment ${assignmentId} changed before collection`);
    }
    if (
      requireExpiredBy &&
      Date.parse(workspace.assignment!.expiresAt) > Date.parse(timestamp(requireExpiredBy, "requireExpiredBy"))
    ) {
      throw new Error(`Assignment ${assignmentId} was renewed before collection`);
    }
    workspace.lifecycle = "returning";
    workspace.failure = null;
    workspace.updatedAt = timestamp(now, "now");
    return workspace;
  });
}

export async function finishWorkspaceReturn(
  paths: StorePaths,
  assignmentId: string,
  now?: string,
): Promise<WorkspaceRecord> {
  let hasPorts = false;
  const workspace = await updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "returning");
    if (workspace.processes.length > 0) {
      throw new Error(`Workspace ${workspace.path} still has tracked processes`);
    }
    hasPorts = Object.keys(workspace.assignment!.ports).length > 0;
    const returnedAt = timestamp(now, "now");
    workspace.lifecycle = "available";
    workspace.operationId = null;
    workspace.assignment = null;
    workspace.updatedAt = returnedAt;
    workspace.availableAt = returnedAt;
    return workspace;
  });
  if (hasPorts) {
    try {
      await releaseHostPorts(assignmentId);
    } catch {
      // The registry prunes this inactive assignment on its next successful update.
    }
  }
  return workspace;
}

export async function cancelWorkspaceReturn(
  paths: StorePaths,
  assignmentId: string,
  failure: string,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "returning");
    workspace.lifecycle = "assigned";
    workspace.failure = nonempty(failure, "failure");
    workspace.updatedAt = timestamp(now, "now");
    return workspace;
  });
}

export async function addAssignmentProcess(
  paths: StorePaths,
  assignmentId: string,
  processRecord: TrackedProcessRecord,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    requireLifecycle(workspace, "assigned");
    if (!Number.isSafeInteger(processRecord.pid) || processRecord.pid <= 0) {
      throw new Error("pid must be a positive safe integer");
    }
    if (processRecord.command?.length === 0) throw new Error("command must not be empty");
    if (processRecord.command?.some((part) => typeof part !== "string")) {
      throw new Error("command must contain only strings");
    }
    if (processRecord.startedAt.length === 0) throw new Error("startedAt must not be empty");
    if (
      processRecord.groupId !== undefined &&
      (!Number.isSafeInteger(processRecord.groupId) || processRecord.groupId <= 0)
    ) {
      throw new Error("groupId must be a positive safe integer");
    }
    if (
      processRecord.sessionId !== undefined &&
      (!Number.isSafeInteger(processRecord.sessionId) || processRecord.sessionId <= 0)
    ) {
      throw new Error("sessionId must be a positive safe integer");
    }
    if ((processRecord.sessionId === undefined) !== (processRecord.sessionStartedAt === undefined)) {
      throw new Error("sessionId and sessionStartedAt must be provided together");
    }
    if (processRecord.terminalId !== undefined && (!processRecord.terminalId || processRecord.terminalId === "??")) {
      throw new Error("terminalId must identify a controlling terminal");
    }
    if (workspace.processes.some((entry) => entry.pid === processRecord.pid)) {
      throw new Error(`Process ${processRecord.pid} is already tracked`);
    }
    workspace.processes.push({
      ...processRecord,
      ...(processRecord.command ? { command: [...processRecord.command] } : {}),
    });
    workspace.updatedAt = timestamp(now, "now");
    return workspace;
  });
}

export async function removeAssignmentProcess(
  paths: StorePaths,
  assignmentId: string,
  pid: number,
  startedAt: string,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const workspace = findByAssignment(state.workspaces, assignmentId);
    if (workspace.lifecycle !== "assigned" && workspace.lifecycle !== "returning") {
      throw new Error(`Workspace ${workspace.path} is not assigned or returning`);
    }
    const index = workspace.processes.findIndex(
      (entry) => entry.pid === pid && entry.startedAt === startedAt,
    );
    if (index < 0) throw new Error(`Process ${pid} with identity ${startedAt} is not tracked`);
    workspace.processes.splice(index, 1);
    workspace.updatedAt = timestamp(now, "now");
    return workspace;
  });
}

export async function findAssignments(
  paths: StorePaths,
  query: AssignmentQuery = {},
): Promise<AssignmentMatch[]> {
  const state = await readState(paths);
  const now = Date.parse(timestamp(query.now, "now"));
  return Object.values(state.workspaces)
    .filter((workspace): workspace is WorkspaceRecord & { assignment: AssignmentRecord } => {
      const current = workspace.assignment;
      return (
        current !== null &&
        (query.id === undefined || current.id === query.id) &&
        (query.owner === undefined || current.owner === query.owner) &&
        (query.hostname === undefined || current.hostname === query.hostname)
      );
    })
    .map((workspace) => ({
      workspace,
      assignment: workspace.assignment,
      expired: Date.parse(workspace.assignment.expiresAt) <= now,
    }))
    .sort((left, right) => left.assignment.assignedAt.localeCompare(right.assignment.assignedAt));
}

export async function identifyGcCandidates(
  paths: StorePaths,
  olderThan: string,
  now?: string,
  includeAbandonedPreparations = false,
): Promise<GcCandidate[]> {
  const state = await readState(paths);
  const cutoff = Date.parse(timestamp(olderThan, "olderThan"));
  const current = timestamp(now, "now");
  const currentTime = Date.parse(current);
  const candidates: GcCandidate[] = [];
  for (const workspace of Object.values(state.workspaces)) {
    if (
      includeAbandonedPreparations &&
      workspace.lifecycle === "preparing" &&
      workspace.operationId !== null &&
      Date.parse(workspace.updatedAt) <= cutoff
    ) {
      candidates.push({ workspace, reason: "abandoned-preparation", requiresForce: false });
    } else if (
      includeAbandonedPreparations &&
      workspace.lifecycle === "assigned" &&
      workspace.operationId !== null &&
      Date.parse(workspace.updatedAt) <= cutoff
    ) {
      candidates.push({ workspace, reason: "abandoned-acquisition", requiresForce: false });
    } else if (
      includeAbandonedPreparations &&
      (workspace.lifecycle === "available" || workspace.lifecycle === "failed") &&
      workspace.operationId !== null &&
      Date.parse(workspace.updatedAt) <= cutoff
    ) {
      candidates.push({ workspace, reason: "interrupted-collection", requiresForce: false });
    } else if (
      workspace.operationId === null &&
      workspace.lifecycle === "available" &&
      Date.parse(workspace.availableAt!) <= cutoff
    ) {
      candidates.push({ workspace, reason: "available", requiresForce: false });
    } else if (
      workspace.operationId === null &&
      workspace.lifecycle === "failed" &&
      Date.parse(workspace.updatedAt) <= cutoff
    ) {
      candidates.push({ workspace, reason: "failed", requiresForce: false });
    } else if (
      workspace.assignment &&
      Date.parse(workspace.assignment.expiresAt) <= currentTime &&
      !assignmentIsAutoRenewing(workspace.assignment, current)
    ) {
      candidates.push({ workspace, reason: "expired-assignment", requiresForce: true });
    }
  }
  return candidates.sort((left, right) => left.workspace.path.localeCompare(right.workspace.path));
}

export async function beginWorkspaceCollection(
  paths: StorePaths,
  workspacePath: string,
  expectedUpdatedAt: string,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const resolved = path.resolve(workspacePath);
    const workspace = state.workspaces[treeKey(resolved)];
    if (!workspace) throw new Error(`Workspace ${resolved} is not managed`);
    const abandonedPreparation = workspace.lifecycle === "preparing";
    if (!abandonedPreparation && workspace.lifecycle !== "available" && workspace.lifecycle !== "failed") {
      throw new Error(`Workspace ${resolved} is not safe to collect`);
    }
    if (workspace.updatedAt !== expectedUpdatedAt) {
      throw new Error(`Workspace ${resolved} changed before collection`);
    }
    if (!abandonedPreparation && workspace.operationId !== null) return workspace;
    if (abandonedPreparation) {
      workspace.lifecycle = "failed";
      workspace.failure = "Workspace preparation was abandoned";
    }
    workspace.operationId = crypto.randomUUID();
    workspace.updatedAt = timestamp(now, "now");
    return workspace;
  });
}

export async function cancelWorkspaceCollection(
  paths: StorePaths,
  workspacePath: string,
  operationId: string,
  now?: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const resolved = path.resolve(workspacePath);
    const workspace = state.workspaces[treeKey(resolved)];
    if (!workspace) throw new Error(`Workspace ${resolved} is not managed`);
    if (workspace.operationId !== operationId) throw new Error("Collection operation does not match");
    workspace.operationId = null;
    const cancelledAt = timestamp(now, "now");
    workspace.updatedAt = cancelledAt;
    if (workspace.lifecycle === "available") workspace.availableAt = cancelledAt;
    return workspace;
  });
}

export async function deleteWorkspaceRecord(
  paths: StorePaths,
  workspacePath: string,
  operationId: string,
): Promise<WorkspaceRecord> {
  return updateState(paths, (state) => {
    const resolved = path.resolve(workspacePath);
    const key = treeKey(resolved);
    const workspace = state.workspaces[key];
    if (!workspace) throw new Error(`Workspace ${resolved} is not managed`);
    if (workspace.lifecycle !== "available" && workspace.lifecycle !== "failed") {
      throw new Error(`Workspace ${resolved} is not safe to collect`);
    }
    if (workspace.assignment || workspace.processes.length > 0) {
      throw new Error(`Workspace ${resolved} is still owned`);
    }
    if (workspace.operationId !== operationId) throw new Error("Collection operation does not match");
    delete state.workspaces[key];
    return workspace;
  });
}
