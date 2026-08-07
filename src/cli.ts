import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { loadConfig, detectPackageManager } from "./config.js";
import {
  assertSharedBackendSupported,
  dependencyProjectionsAreValid,
  dependenciesPresent,
  ensureDependencies,
} from "./dependencies.js";
import { dependencyFingerprint } from "./fingerprint.js";
import {
  addWorktree,
  assignWorktree,
  fetchDefaultRemote,
  fetchRemote,
  getRepository,
  listWorktrees,
  lockWorktree,
  removeWorktree,
  returnWorktree,
  unlockWorktree,
} from "./git.js";
import {
  addAssignmentProcess,
  beginWorkspaceCollection,
  beginWorkspaceReturn,
  cancelWorkspaceCollection,
  cancelWorkspaceReturn,
  deleteWorkspaceRecord,
  findAssignments,
  finishWorkspaceReturn,
  identifyGcCandidates,
  markWorkspaceAssigned,
  markWorkspaceAvailable,
  markWorkspaceFailed,
  recordPreparingWorkspace,
  recordSuccessfulAcquisition,
  removeAssignmentProcess,
  renewAssignment,
  reserveAvailableWorkspace,
  allocateAssignmentPorts,
} from "./lifecycle.js";
import { withDirectoryLock } from "./lock.js";
import { portEnvironment } from "./ports.js";
import {
  ProcessIdentityUnavailableError,
  processIdentity,
  requireChildProcessSession,
  requireProcessIdentity,
  run,
  terminateTrackedProcess,
  trackedProcessExists,
} from "./process.js";
import { deleteTreeState, readState, storePaths, treeKey, treeLockPath } from "./state.js";
import { diskStatistics, usageStatistics } from "./statistics.js";
import type { CliIo, DependencyReporter, StorePaths, TrackedProcessRecord, WorkspaceRecord } from "./types.js";
import { formatUpdate, updateRuk } from "./update.js";
import type { Distribution } from "./update.js";
import { VERSION } from "./version.js";

const HELP = `Ruk — dependency-aware Git workspaces for parallel coding agents

Usage:
  ruk init [--json]
  ruk create <branch> [--path <directory>] [--from <ref>] [--fetch] [--detach] [--json]
  ruk acquire <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...] [--json]
  ruk renew <assignment-id> [--ttl <minutes>] [--json]
  ruk release <assignment-id> [--force] [--json]
  ruk sync [--json]
  ruk run -- <command> [args...]
  ruk exec <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...] -- <command> [args...]
  ruk warm --count <n> [--from <ref>] [--fetch] [--json]
  ruk shell <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...]
  ruk list [--json]
  ruk remove <path> [--force]
  ruk status [--explain] [--json]
  ruk stats [--disk] [--json]
  ruk gc [--max-age <minutes>] [--apply] [--force-expired] [--json]
  ruk update [--check] [--json]

Ruk shares immutable package content by default with supported Bun and pnpm
versions. Set dependencyMode to "managed" if a repository needs its normal layout.
`;

interface ParsedOptions {
  path?: string;
  from?: string;
  detach?: boolean;
  json?: boolean;
  force?: boolean;
  check?: boolean;
  ttl?: string;
  owner?: string;
  maxAge?: string;
  apply?: boolean;
  forceExpired?: boolean;
  fetch?: boolean;
  explain?: boolean;
  disk?: boolean;
  count?: string;
  ports?: string[];
}

function parseOptions(
  args: readonly string[],
  { values = [], flags = [] }: { values?: readonly string[]; flags?: readonly string[] } = {},
): { options: ParsedOptions; positional: string[] } {
  const valueOptions = new Set(values);
  const flagOptions = new Set(flags);
  const options: ParsedOptions = {};
  const positional: string[] = [];

  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index]!;
    if (!argument.startsWith("--")) {
      positional.push(argument);
      continue;
    }
    if (flagOptions.has(argument)) {
      const key = argument.slice(2).replace(/-([a-z])/g, (_, letter: string) => letter.toUpperCase());
      (options as Record<string, string | boolean | undefined>)[key] = true;
      continue;
    }
    if (valueOptions.has(argument)) {
      const value = args[index + 1];
      if (!value || value.startsWith("--")) throw new Error(`${argument} requires a value`);
      const key = argument.slice(2).replace(/-([a-z])/g, (_, letter: string) => letter.toUpperCase());
      if (argument === "--port") options.ports = [...(options.ports ?? []), value];
      else (options as Record<string, string | boolean | undefined>)[key] = value;
      index += 1;
      continue;
    }
    throw new Error(`Unknown option ${argument}`);
  }
  return { options, positional };
}

function requirePositionals(positional: readonly string[], count: number, usage: string): void {
  if (positional.length !== count) throw new Error(usage);
}

function slug(value: string): string {
  return value.replace(/[^a-zA-Z0-9._-]+/g, "-").replace(/^-+|-+$/g, "") || "workspace";
}

function jsonLine(value: unknown): string {
  return `${JSON.stringify(value)}\n`;
}

async function repositoryContext(cwd: string) {
  const repository = await getRepository(cwd);
  const config = await loadConfig(repository.root);
  return { repository, config };
}

async function context(cwd: string) {
  const { repository, config } = await repositoryContext(cwd);
  const manager = await detectPackageManager(repository.root, config);
  return { repository, config, manager };
}

function dependencyReporter(io: CliIo, json: boolean): DependencyReporter {
  return json
    ? {
        write: () => {},
        stdio: "ignore",
      }
    : {
        write: (message: string) => {
          io.stdout.write(message);
        },
        stdio: "inherit",
      };
}

async function sync(cwd: string, io: CliIo, json = false, emit = true) {
  const value = await context(cwd);
  const result = await ensureDependencies({
    ...value,
    reporter: dependencyReporter(io, json),
  });
  const output = {
    status: result.alreadyAttached ? "ready" : "prepared",
    fingerprint: result.fingerprint,
    mode: result.mode,
    path: value.repository.root,
  };
  if (emit) {
    io.stdout.write(
      json
        ? jsonLine(output)
        : `${result.alreadyAttached ? "Dependencies already ready" : "Dependencies prepared"} for ${result.fingerprint.slice(0, 12)} (${result.mode}).\n`,
    );
  }
  return { ...result, path: value.repository.root };
}

function minutes(value: string | undefined, fallback: number, name: string, allowZero = false): number {
  const parsed = value === undefined ? fallback : Number(value);
  if (!Number.isFinite(parsed) || (allowZero ? parsed < 0 : parsed <= 0)) {
    throw new Error(`${name} must be ${allowZero ? "a non-negative" : "a positive"} number of minutes`);
  }
  return parsed;
}

function expiresIn(durationMinutes: number): string {
  return new Date(Date.now() + durationMinutes * 60_000).toISOString();
}

async function fetchIfRequested(repository: string, startPoint: string, requested: boolean): Promise<void> {
  if (requested) await fetchRemote(repository, startPoint);
}

async function resolveStartPoint(
  repository: string,
  requested: string | undefined,
  fetch: boolean,
): Promise<string> {
  if (requested) {
    await fetchIfRequested(repository, requested, fetch);
    return requested;
  }
  return fetch ? fetchDefaultRemote(repository) : "HEAD";
}

async function canonicalPath(value: string): Promise<string> {
  try {
    return await fs.realpath(value);
  } catch {
    return path.resolve(value);
  }
}

async function acquire(args: readonly string[], cwd: string, io: CliIo, emit = true) {
  const { options, positional } = parseOptions(args, {
    values: ["--from", "--ttl", "--owner", "--port"],
    flags: ["--fetch", "--json"],
  });
  requirePositionals(positional, 1, "acquire requires exactly one branch name");
  const branch = positional[0]!;
  const { repository } = await repositoryContext(cwd);
  const startPoint = await resolveStartPoint(repository.root, options.from, options.fetch ?? false);
  const paths = storePaths(repository.commonDir);
  const ttl = minutes(options.ttl, 480, "--ttl");
  const assignmentBase = {
    owner: options.owner ?? process.env["RUK_AGENT_ID"] ?? `${os.hostname()}:${process.pid}`,
    hostname: os.hostname(),
    branch,
  };
  const finishAcquisition = async (workspace: WorkspaceRecord, reused: boolean) => {
    let operationId = reused ? null : workspace.operationId;
    let dependenciesReady = false;
    try {
      if (!reused) {
        await addWorktree({
          cwd: repository.root,
          destination: workspace.path,
          branch,
          startPoint,
          detach: true,
          stdio: options.json ? "pipe" : "inherit",
        });
        await lockWorktree(repository.root, workspace.path);
      }
      await assignWorktree({
        repository: repository.root,
        workspace: workspace.path,
        branch,
        startPoint,
      });
      const prepared = await sync(workspace.path, io, options.json ?? false, false);
      dependenciesReady = true;
      if (operationId) {
        workspace = await markWorkspaceAssigned(paths, workspace.path, operationId, {
          ...assignmentBase,
          expiresAt: expiresIn(ttl),
        });
        operationId = null;
      } else {
        workspace = await renewAssignment(paths, workspace.assignment!.id, expiresIn(ttl));
      }
      if (options.ports?.length) {
        workspace = await allocateAssignmentPorts(paths, workspace.assignment!.id, options.ports);
      }
      workspace = await recordSuccessfulAcquisition(
        paths,
        workspace.assignment!.id,
        workspace.operationId!,
        reused,
      );
      const result = {
        status: "assigned",
        assignmentId: workspace.assignment!.id,
        path: workspace.path,
        branch,
        expiresAt: workspace.assignment!.expiresAt,
        reused,
        fingerprint: prepared.fingerprint,
        mode: prepared.mode,
        ports: workspace.assignment!.ports,
      };
      if (emit && options.json) io.stdout.write(jsonLine(result));
      else if (emit) {
        io.stdout.write(`Assigned ${workspace.path}\nAssignment: ${result.assignmentId}\nExpires: ${result.expiresAt}\n`);
        for (const [name, port] of Object.entries(result.ports)) io.stdout.write(`${name}: ${port}\n`);
      }
      return result;
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      let returning = false;
      try {
        if (operationId) {
          await markWorkspaceFailed(paths, workspace.path, operationId, message);
        } else if (workspace.assignment) {
          await beginWorkspaceReturn(paths, workspace.assignment.id);
          returning = true;
          const projections = (await readState(paths)).trees[treeKey(workspace.path)]?.projections ?? [];
          await returnWorktree(workspace.path, true, dependenciesReady ? projections : []);
          if (!dependenciesReady) await deleteTreeState(paths, workspace.path);
          await finishWorkspaceReturn(paths, workspace.assignment.id);
        }
      } catch (cleanupError) {
        if (returning) {
          const cleanupMessage = cleanupError instanceof Error ? cleanupError.message : String(cleanupError);
          try { await cancelWorkspaceReturn(paths, workspace.assignment!.id, cleanupMessage); } catch { /* Preserve both failures. */ }
        }
        throw new AggregateError([error, cleanupError], `Workspace acquisition failed and cleanup also failed for ${workspace.path}`);
      }
      throw error;
    }
  };

  while (true) {
    const available = Object.values((await readState(paths)).workspaces)
      .filter((workspace) => workspace.lifecycle === "available" && workspace.operationId === null)
      .sort(
        (left, right) =>
          (left.availableAt ?? "").localeCompare(right.availableAt ?? "") || left.path.localeCompare(right.path),
      )[0];
    if (available) {
      const result = await withDirectoryLock(`${treeLockPath(paths, available.path)}.acquire`, async () => {
        const reserved = await reserveAvailableWorkspace(
          paths,
          { ...assignmentBase, expiresAt: expiresIn(ttl) },
          available.path,
        );
        return reserved ? finishAcquisition(reserved, true) : null;
      });
      if (result) return result;
      continue;
    }

    const destination = path.join(
      path.dirname(repository.root),
      `${path.basename(repository.root)}-ruk-${crypto.randomUUID().slice(0, 8)}`,
    );
    return withDirectoryLock(`${treeLockPath(paths, destination)}.acquire`, async () => {
      const workspace = await recordPreparingWorkspace(paths, { path: destination, branch });
      return finishAcquisition(workspace, false);
    });
  }
}

async function renew(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, {
    values: ["--ttl"],
    flags: ["--json"],
  });
  requirePositionals(positional, 1, "renew requires exactly one assignment ID");
  const { repository } = await repositoryContext(cwd);
  const workspace = await renewAssignment(
    storePaths(repository.commonDir),
    positional[0]!,
    expiresIn(minutes(options.ttl, 480, "--ttl")),
  );
  const result = {
    status: "renewed",
    assignmentId: workspace.assignment!.id,
    path: workspace.path,
    expiresAt: workspace.assignment!.expiresAt,
  };
  io.stdout.write(options.json ? jsonLine(result) : `Renewed ${result.assignmentId} until ${result.expiresAt}\n`);
  return result;
}

const delay = (milliseconds: number): Promise<void> =>
  new Promise((resolve) => setTimeout(resolve, milliseconds));

async function processExited(record: TrackedProcessRecord, timeoutMs: number): Promise<boolean> {
  const deadline = Date.now() + timeoutMs;
  do {
    if (!(await trackedProcessExists(record))) return true;
    await delay(50);
  } while (Date.now() < deadline);
  return false;
}

async function cleanTrackedProcesses(
  paths: StorePaths,
  assignmentId: string,
  processes: readonly TrackedProcessRecord[],
  force: boolean,
): Promise<number> {
  let cleaned = 0;
  for (const record of processes) {
    if (await trackedProcessExists(record)) {
      if (await terminateTrackedProcess(record)) cleaned += 1;
      if (!(await processExited(record, 1_500))) {
        if (!force) throw new Error(`Tracked process ${record.pid} survived graceful termination; retry with --force`);
        await terminateTrackedProcess(record, true);
        if (!(await processExited(record, 1_500))) throw new Error(`Could not terminate tracked process ${record.pid}`);
      }
    }
    try {
      await removeAssignmentProcess(paths, assignmentId, record.pid, record.startedAt);
    } catch (error) {
      const current = (await findAssignments(paths, { id: assignmentId }))[0];
      if (current?.workspace.processes.some((entry) => entry.pid === record.pid && entry.startedAt === record.startedAt)) {
        throw error;
      }
    }
  }
  return cleaned;
}

async function releaseAssignment(
  repository: Awaited<ReturnType<typeof getRepository>>,
  assignmentId: string,
  force: boolean,
  requireExpiredBy?: string,
): Promise<{ workspace: WorkspaceRecord; cleanedProcesses: number }> {
  const paths = storePaths(repository.commonDir);
  const matches = await findAssignments(paths, { id: assignmentId });
  if (matches.length !== 1) throw new Error(`Assignment ${assignmentId} does not exist`);
  const workspacePath = matches[0]!.workspace.path;
  return withDirectoryLock(treeLockPath(paths, workspacePath), async () => {
    let returning = false;
    try {
      const workspace = await beginWorkspaceReturn(paths, assignmentId, undefined, requireExpiredBy);
      returning = true;
      const cleanedProcesses = await cleanTrackedProcesses(paths, assignmentId, workspace.processes, force);
      const tree = (await readState(paths)).trees[treeKey(workspace.path)];
      const projections = tree && (await dependencyProjectionsAreValid(workspace.path, tree))
        ? tree.projections
        : [];
      await returnWorktree(workspace.path, force, projections);
      if (tree && projections.length === 0) await deleteTreeState(paths, workspace.path);
      const available = await finishWorkspaceReturn(paths, assignmentId);
      return { workspace: available, cleanedProcesses };
    } catch (error) {
      if (returning) {
        const message = error instanceof Error ? error.message : String(error);
        try { await cancelWorkspaceReturn(paths, assignmentId, message); } catch { /* Preserve the original failure. */ }
      }
      throw error;
    }
  });
}

async function release(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, { flags: ["--force", "--json"] });
  requirePositionals(positional, 1, "release requires exactly one assignment ID");
  const { repository } = await repositoryContext(cwd);
  const released = await releaseAssignment(repository, positional[0]!, options.force ?? false);
  const result = {
    status: "available",
    assignmentId: positional[0]!,
    path: released.workspace.path,
    cleanedProcesses: released.cleanedProcesses,
  };
  io.stdout.write(options.json ? jsonLine(result) : `Released ${result.path}\n`);
  return result;
}

async function create(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, {
    values: ["--path", "--from"],
    flags: ["--detach", "--fetch", "--json"],
  });
  requirePositionals(positional, 1, "create requires exactly one branch name");
  const branch = positional[0]!;
  const { repository, manager } = await context(cwd);
  const startPoint = await resolveStartPoint(repository.root, options.from, options.fetch ?? false);
  const backend = await dependencyFingerprint({ root: repository.root, manager });
  if (manager.dependencyMode === "shared") assertSharedBackendSupported(backend.manager);
  const destination = path.resolve(
    cwd,
    options.path ?? path.join(path.dirname(repository.root), `${path.basename(repository.root)}-${slug(branch)}`),
  );

  await addWorktree({
    cwd: repository.root,
    destination,
    branch,
    startPoint,
    detach: options.detach ?? false,
    stdio: options.json ? "pipe" : "inherit",
  });

  try {
    const result = await sync(destination, io, options.json ?? false);
    if (options.json) {
      // sync already emitted the machine-readable record with the path.
      return result;
    }
    io.stdout.write(`${destination}\n`);
    return result;
  } catch (error) {
    try {
      await removeWorktree({
        cwd: repository.root,
        destination,
        force: true,
        stdio: options.json ? "pipe" : "inherit",
      });
    } catch (cleanupError) {
      throw new AggregateError(
        [error, cleanupError],
        `Workspace preparation failed and cleanup also failed for ${destination}`,
      );
    }
    throw error;
  }
}

async function list(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, { flags: ["--json"] });
  requirePositionals(positional, 0, "list does not accept positional arguments");
  const { repository } = await repositoryContext(cwd);
  const paths = storePaths(repository.commonDir);
  const state = await readState(paths);
  const worktrees = await listWorktrees(repository.root);
  const records = worktrees.map((workspace) => {
    const record = state.trees[treeKey(workspace.path)];
    const lifecycle = state.workspaces[treeKey(workspace.path)];
    return {
      ...workspace,
      fingerprint: record?.fingerprint ?? null,
      mode: record?.mode ?? null,
      status: record ? "prepared" : "not-prepared",
      lifecycle: lifecycle?.lifecycle ?? null,
      assignmentId: lifecycle?.assignment?.id ?? null,
      expiresAt: lifecycle?.assignment?.expiresAt ?? null,
    };
  });
  if (options.json) {
    io.stdout.write(jsonLine(records));
    return records;
  }
  for (const workspace of records) {
    io.stdout.write(
      `${workspace.branch.padEnd(28)} ${(workspace.fingerprint?.slice(0, 12) ?? "not-prepared").padEnd(14)} ${(workspace.mode ?? "-").padEnd(20)} ${workspace.path}\n`,
    );
  }
  return records;
}

async function status(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, { flags: ["--explain", "--json"] });
  requirePositionals(positional, 0, "status does not accept positional arguments");
  const value = await context(cwd);
  const paths = storePaths(value.repository.commonDir);
  const state = await readState(paths);
  const record = state.trees[treeKey(value.repository.root)];
  const lifecycle = state.workspaces[treeKey(value.repository.root)];
  const current = await dependencyFingerprint({ root: value.repository.root, manager: value.manager });
  const modulesPresent = await dependenciesPresent(
    value.repository.root,
    record?.projections ?? ["node_modules"],
  );
  const projectionsValid = record ? await dependencyProjectionsAreValid(value.repository.root, record) : false;
  const ready = record?.fingerprint === current.fingerprint && projectionsValid;
  const reason = ready
    ? null
    : !record
      ? "not-prepared"
      : !modulesPresent
        ? "dependencies-missing"
        : record.fingerprint !== current.fingerprint
          ? "fingerprint-changed"
          : "projection-changed";
  const result = {
    path: value.repository.root,
    fingerprint: current.fingerprint,
    preparedFingerprint: record?.fingerprint ?? null,
    mode: record?.mode ?? null,
    nodeModulesPresent: modulesPresent,
    status: ready ? "ready" : "sync-required",
    reason,
    recovery: ready ? null : "ruk sync",
    lifecycle: lifecycle?.lifecycle ?? null,
    assignmentId: lifecycle?.assignment?.id ?? null,
    expiresAt: lifecycle?.assignment?.expiresAt ?? null,
  };
  if (options.json) {
    io.stdout.write(jsonLine(result));
  } else {
    io.stdout.write(`Workspace:   ${result.path}\n`);
    io.stdout.write(`Fingerprint: ${result.fingerprint}\n`);
    io.stdout.write(`Prepared:    ${result.preparedFingerprint ?? "not-prepared"}\n`);
    io.stdout.write(`Mode:        ${result.mode ?? "-"}\n`);
    io.stdout.write(`node_modules:${modulesPresent ? " present" : " missing"}\n`);
    io.stdout.write(`Status:      ${result.status}\n`);
    if (options.explain && reason) io.stdout.write(`Reason:      ${reason}\nRecovery:    ${result.recovery}\n`);
    io.stdout.write(`Lifecycle:   ${result.lifecycle ?? "unmanaged"}\n`);
    if (result.assignmentId) io.stdout.write(`Assignment:  ${result.assignmentId} (expires ${result.expiresAt})\n`);
    if (!ready && !options.explain) io.stdout.write("Next:        ruk sync\n");
  }
  return result;
}

async function remove(args: readonly string[], cwd: string) {
  const { options, positional } = parseOptions(args, { flags: ["--force"] });
  requirePositionals(positional, 1, "remove requires exactly one workspace path");
  const { repository } = await repositoryContext(cwd);
  const destination = await canonicalPath(path.resolve(cwd, positional[0]!));
  if (destination === await canonicalPath(repository.root)) throw new Error("Refusing to remove the current workspace");
  const paths = storePaths(repository.commonDir);
  const state = await readState(paths);
  const managed = state.workspaces[treeKey(destination)];
  if (managed) {
    throw new Error(
      managed.assignment
        ? `Workspace is managed by assignment ${managed.assignment.id}; use ruk release ${managed.assignment.id}`
        : "Workspace belongs to the managed pool; use ruk gc --apply",
    );
  }
  await withDirectoryLock(treeLockPath(paths, destination), async () => {
    await removeWorktree({ cwd: repository.root, destination, force: options.force ?? false });
    await deleteTreeState(paths, destination);
  });
}

async function collectWorkspaceWithAcquisitionLock(
  repository: Awaited<ReturnType<typeof getRepository>>,
  paths: StorePaths,
  workspace: WorkspaceRecord,
  stdio: "pipe" | "inherit" = "inherit",
): Promise<void> {
  await withDirectoryLock(treeLockPath(paths, workspace.path), async () => {
    const collecting = await beginWorkspaceCollection(paths, workspace.path, workspace.updatedAt);
    let gitRemoved = false;
    let unlocked = false;
    try {
      const worktrees = await listWorktrees(repository.root);
      const managedPath = await canonicalPath(workspace.path);
      if (await Promise.all(worktrees.map((entry) => canonicalPath(entry.path))).then((entries) => entries.includes(managedPath))) {
        await unlockWorktree(repository.root, workspace.path);
        unlocked = true;
        await removeWorktree({ cwd: repository.root, destination: workspace.path, force: true, stdio });
      }
      gitRemoved = true;
      await deleteWorkspaceRecord(paths, workspace.path, collecting.operationId!);
      await deleteTreeState(paths, workspace.path);
    } catch (error) {
      if (!gitRemoved) {
        let cleanupError: unknown;
        if (unlocked) {
          try { await lockWorktree(repository.root, workspace.path); } catch (failure) { cleanupError = failure; }
        }
        if (!cleanupError) {
          try {
            await cancelWorkspaceCollection(paths, workspace.path, collecting.operationId!);
          } catch (failure) {
            cleanupError = failure;
          }
        }
        if (cleanupError) {
          throw new AggregateError([error, cleanupError], `Workspace collection failed and recovery also failed for ${workspace.path}`);
        }
      }
      throw error;
    }
  }, { staleMs: 0 });
}

async function collectWorkspace(
  repository: Awaited<ReturnType<typeof getRepository>>,
  paths: StorePaths,
  workspace: WorkspaceRecord,
  stdio: "pipe" | "inherit" = "inherit",
): Promise<void> {
  await withDirectoryLock(
    `${treeLockPath(paths, workspace.path)}.acquire`,
    () => collectWorkspaceWithAcquisitionLock(repository, paths, workspace, stdio),
    { staleMs: 0 },
  );
}

async function garbageCollect(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, {
    values: ["--max-age"],
    flags: ["--apply", "--force-expired", "--json"],
  });
  requirePositionals(positional, 0, "gc does not accept positional arguments");
  if (options.forceExpired && !options.apply) throw new Error("--force-expired requires --apply");
  const { repository } = await repositoryContext(cwd);
  const paths = storePaths(repository.commonDir);
  const cutoff = new Date(Date.now() - minutes(options.maxAge, 1440, "--max-age", true) * 60_000).toISOString();
  const current = await canonicalPath(repository.root);
  const candidates = await withDirectoryLock(
    path.join(paths.root, "warm.lock"),
    () => identifyGcCandidates(paths, cutoff, undefined, true),
    { staleMs: 0 },
  );
  const expiredCandidates = candidates.filter((candidate) => candidate.requiresForce);
  const safeCandidates = candidates.filter((candidate) => !candidate.requiresForce);
  const removed: Array<{ path: string; lifecycle: string; reason: string }> = [];
  const candidateReason = (reason: (typeof safeCandidates)[number]["reason"]): string => {
    if (reason === "abandoned-preparation") return "abandoned preparation";
    if (reason === "abandoned-acquisition") return "abandoned acquisition";
    if (reason === "interrupted-collection") return "interrupted collection";
    return "older than max age";
  };

  if (options.apply) {
    for (const candidate of safeCandidates) {
      if (await canonicalPath(candidate.workspace.path) === current) continue;
      if (candidate.reason === "abandoned-acquisition") {
        const collected = await withDirectoryLock(
          `${treeLockPath(paths, candidate.workspace.path)}.acquire`,
          async () => {
            const currentCandidate = (await identifyGcCandidates(paths, cutoff, undefined, true)).find(
              (entry) =>
                entry.reason === "abandoned-acquisition" &&
                treeKey(entry.workspace.path) === treeKey(candidate.workspace.path) &&
                entry.workspace.operationId === candidate.workspace.operationId &&
                entry.workspace.assignment?.id === candidate.workspace.assignment?.id,
            );
            if (!currentCandidate) return false;
            const released = await releaseAssignment(repository, currentCandidate.workspace.assignment!.id, true);
            await collectWorkspaceWithAcquisitionLock(
              repository,
              paths,
              released.workspace,
              options.json ? "pipe" : "inherit",
            );
            return true;
          },
          { staleMs: 0 },
        );
        if (!collected) continue;
      } else {
        await collectWorkspace(repository, paths, candidate.workspace, options.json ? "pipe" : "inherit");
      }
      removed.push({
        path: candidate.workspace.path,
        lifecycle: candidate.workspace.lifecycle,
        reason: candidateReason(candidate.reason),
      });
    }
    if (options.forceExpired) {
      for (const candidate of expiredCandidates) {
        if (await canonicalPath(candidate.workspace.path) === current) continue;
        const collected = await withDirectoryLock(
          `${treeLockPath(paths, candidate.workspace.path)}.acquire`,
          async () => {
            const collectionTime = new Date().toISOString();
            const currentCandidate = (await identifyGcCandidates(paths, cutoff, collectionTime, true)).find(
              (entry) =>
                entry.reason === "expired-assignment" &&
                treeKey(entry.workspace.path) === treeKey(candidate.workspace.path) &&
                entry.workspace.assignment?.id === candidate.workspace.assignment?.id,
            );
            if (!currentCandidate) return false;
            const released = await releaseAssignment(
              repository,
              currentCandidate.workspace.assignment!.id,
              true,
              collectionTime,
            );
            await collectWorkspaceWithAcquisitionLock(
              repository,
              paths,
              released.workspace,
              options.json ? "pipe" : "inherit",
            );
            return true;
          },
          { staleMs: 0 },
        );
        if (!collected) continue;
        removed.push({ path: candidate.workspace.path, lifecycle: candidate.workspace.lifecycle, reason: "expired assignment (forced)" });
      }
    }
  } else {
    for (const candidate of safeCandidates) {
      if (await canonicalPath(candidate.workspace.path) === current) continue;
      removed.push({
        path: candidate.workspace.path,
        lifecycle: candidate.workspace.lifecycle,
        reason: candidateReason(candidate.reason),
      });
    }
  }

  const result = {
    status: options.apply ? "collected" : "planned",
    removed,
    expired: expiredCandidates.map(({ workspace }) => ({
      path: workspace.path,
      assignmentId: workspace.assignment!.id,
      expiresAt: workspace.assignment!.expiresAt,
    })),
  };
  if (options.json) io.stdout.write(jsonLine(result));
  else {
    io.stdout.write(`${options.apply ? "Collected" : "Would collect"}: ${removed.length} workspace(s)\n`);
    if (result.expired.length) io.stdout.write(`Expired assignments: ${result.expired.length}\n`);
  }
  return result;
}

async function execute(
  args: readonly string[],
  cwd: string,
  io: CliIo,
  detached = true,
  sessionMarker?: string,
  forwardInterrupt = false,
): Promise<number> {
  const separator = args.indexOf("--");
  const command = separator < 0 ? [...args] : args.slice(separator + 1);
  if (command.length === 0) throw new Error("run requires a command");
  if (separator > 0) throw new Error(`Unknown run option ${args[0]}`);
  const value = await context(cwd);
  const { repository } = value;
  const [program, ...programArgs] = command;
  const paths = storePaths(repository.commonDir);
  let state = await readState(paths);
  let lifecycle = state.workspaces[treeKey(repository.root)];
  if (lifecycle && (
    lifecycle.lifecycle !== "assigned" ||
    !lifecycle.assignment ||
    lifecycle.operationId !== null
  )) {
    throw new Error(`Workspace ${repository.root} is ${lifecycle.lifecycle}, expected assigned`);
  }
  const expectedAssignmentId = lifecycle?.assignment?.id;
  const tree = state.trees[treeKey(repository.root)];
  if (
    !lifecycle?.assignment ||
    !tree ||
    tree.fingerprint !== (await dependencyFingerprint({ root: repository.root, manager: value.manager })).fingerprint ||
    !(await dependencyProjectionsAreValid(repository.root, tree))
  ) {
    await sync(repository.root, io);
    state = await readState(paths);
    lifecycle = state.workspaces[treeKey(repository.root)];
    if (expectedAssignmentId && lifecycle?.assignment?.id !== expectedAssignmentId) {
      throw new Error(`Assignment ${expectedAssignmentId} no longer owns ${repository.root}`);
    }
    if (!expectedAssignmentId && lifecycle) {
      throw new Error(`Workspace ${repository.root} became managed during dependency synchronization`);
    }
  }
  if (!lifecycle) {
    const result = await run(program!, programArgs, {
      cwd: repository.root,
      env: process.env,
      stdio: "inherit",
      allowFailure: true,
    });
    return result.code;
  }
  if (lifecycle.lifecycle !== "assigned" || !lifecycle.assignment) {
    throw new Error(`Workspace ${repository.root} is ${lifecycle.lifecycle}, expected assigned`);
  }

  const assignmentId = expectedAssignmentId ?? lifecycle.assignment.id;
  const environment = { ...process.env, ...portEnvironment(lifecycle.assignment.ports) };
  const tracking: { record?: TrackedProcessRecord } = {};
  let interruptPending = false;
  const forwardSignal = () => {
    interruptPending = true;
    const groupId = tracking.record?.groupId;
    if (!groupId) return;
    interruptPending = false;
    try { process.kill(-groupId, "SIGINT"); } catch { /* The command may already have exited. */ }
  };
  let execution!: ReturnType<typeof run>;
  if (forwardInterrupt && process.platform !== "win32") process.on("SIGINT", forwardSignal);
  try {
    await withDirectoryLock(treeLockPath(paths, repository.root), async () => {
      const current = (await readState(paths)).workspaces[treeKey(repository.root)];
      if (
        current?.lifecycle !== "assigned" ||
        current.operationId !== null ||
        current.assignment?.id !== assignmentId
      ) {
        throw new Error(`Assignment ${assignmentId} does not exist or no longer owns ${repository.root}`);
      }
      let registered!: () => void;
      const registration = new Promise<void>((resolve) => { registered = resolve; });
      execution = run(program!, programArgs, {
        cwd: repository.root,
        env: environment,
        stdio: "inherit",
        allowFailure: true,
        detached,
        onSpawn: async (pid) => {
          const session = sessionMarker ? await requireChildProcessSession(pid, sessionMarker) : undefined;
          const startedAt = session?.startedAt ?? await requireProcessIdentity(pid);
          if (!startedAt) {
            registered();
            return;
          }
          const record: TrackedProcessRecord = {
            pid: session?.pid ?? pid,
            ...(process.platform === "win32" || !detached ? {} : { groupId: pid }),
            ...(session?.sessionId === undefined
              ? {}
              : { sessionId: session.sessionId, sessionStartedAt: session.sessionStartedAt }),
            ...(session?.terminalId === undefined ? {} : { terminalId: session.terminalId }),
            command: [...command],
            startedAt,
          };
          await addAssignmentProcess(paths, assignmentId, record);
          tracking.record = record;
          if (interruptPending) forwardSignal();
          registered();
        },
      });
      await Promise.race([registration, execution.then(() => undefined)]);
    });
    const result = await execution;
    if (tracking.record && !(await trackedProcessExists(tracking.record))) {
      try {
        await removeAssignmentProcess(paths, assignmentId, tracking.record.pid, tracking.record.startedAt);
      } catch {
        // Release may already have consumed the tracked record.
      }
    }
    return result.code;
  } finally {
    if (forwardInterrupt && process.platform !== "win32") process.off("SIGINT", forwardSignal);
  }
}

async function warm(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, {
    values: ["--count", "--from"],
    flags: ["--fetch", "--json"],
  });
  requirePositionals(positional, 0, "warm does not accept positional arguments");
  const count = Number(options.count);
  if (!Number.isSafeInteger(count) || count < 1) throw new Error("--count must be a positive integer");
  const { repository } = await repositoryContext(cwd);
  const startPoint = await resolveStartPoint(repository.root, options.from, options.fetch ?? false);
  const targetHead = (await run("git", ["rev-parse", startPoint], { cwd: repository.root })).stdout.trim();
  const paths = storePaths(repository.commonDir);
  // ponytail: one host-wide warm lock; reserve per-slot if concurrent warming needs higher throughput.
  return withDirectoryLock(path.join(paths.root, "warm.lock"), async () => {
    const initial = await readState(paths);
    const worktreeHeads = new Map(
      (await listWorktrees(repository.root)).map((worktree) => [treeKey(worktree.path), worktree.head]),
    );
    let available = 0;
    for (const workspace of Object.values(initial.workspaces)) {
      if (workspace.lifecycle !== "available" || workspace.operationId !== null) continue;
      if (worktreeHeads.get(treeKey(workspace.path)) !== targetHead) continue;
      const record = initial.trees[treeKey(workspace.path)];
      if (!record || !(await dependencyProjectionsAreValid(workspace.path, record))) continue;
      const value = await context(workspace.path);
      const current = await dependencyFingerprint({ root: value.repository.root, manager: value.manager });
      if (record.fingerprint === current.fingerprint) available += 1;
    }
    const created: string[] = [];

    for (let index = available; index < count; index += 1) {
      const destination = path.join(
        path.dirname(repository.root),
        `${path.basename(repository.root)}-ruk-${crypto.randomUUID().slice(0, 8)}`,
      );
      const workspace = await recordPreparingWorkspace(paths, { path: destination, branch: "(warm)" });
      try {
        await addWorktree({
          cwd: repository.root,
          destination,
          branch: "(warm)",
          startPoint,
          detach: true,
          stdio: options.json ? "pipe" : "inherit",
        });
        await lockWorktree(repository.root, destination);
        await sync(destination, io, options.json ?? false, false);
        await markWorkspaceAvailable(paths, destination, workspace.operationId!);
        created.push(destination);
      } catch (error) {
        await markWorkspaceFailed(
          paths,
          destination,
          workspace.operationId!,
          error instanceof Error ? error.message : String(error),
        );
        throw error;
      }
    }

    const result = { status: "warmed", requested: count, available: available + created.length, created };
    if (options.json) io.stdout.write(jsonLine(result));
    else io.stdout.write(`Available workspaces: ${result.available} (${created.length} created)\n`);
    return result;
  });
}

function splitExecArguments(args: readonly string[]): { acquireArgs: string[]; command: string[] } {
  const separator = args.indexOf("--");
  if (separator >= 0) {
    return { acquireArgs: args.slice(0, separator), command: args.slice(separator + 1) };
  }
  if (!args[0]) return { acquireArgs: [], command: [] };
  const acquireArgs = [args[0]];
  let index = 1;
  while (index < args.length) {
    const argument = args[index]!;
    if (["--fetch"].includes(argument)) {
      acquireArgs.push(argument);
      index += 1;
      continue;
    }
    if (["--from", "--ttl", "--owner", "--port"].includes(argument)) {
      if (!args[index + 1]) throw new Error(`${argument} requires a value`);
      acquireArgs.push(argument, args[index + 1]!);
      index += 2;
      continue;
    }
    break;
  }
  return { acquireArgs, command: args.slice(index) };
}

function retainedAssignment(io: CliIo, result: Awaited<ReturnType<typeof acquire>>, error: unknown): void {
  const message = error instanceof Error ? error.message : String(error);
  io.stderr.write(
    `Workspace retained: ${result.path}\nAssignment: ${result.assignmentId}\nExpires: ${result.expiresAt}\nReason: ${message}\nRelease: ruk release ${result.assignmentId}\n`,
  );
}

async function runAssignedAndRelease(
  assigned: Awaited<ReturnType<typeof acquire>>,
  command: readonly string[],
  sourceCwd: string,
  io: CliIo,
  detached = true,
  sessionMarker?: string,
  forwardInterrupt = false,
): Promise<number> {
  let code = 1;
  let commandError: unknown;
  try {
    code = await execute(["--", ...command], assigned.path, io, detached, sessionMarker, forwardInterrupt);
  } catch (error) {
    commandError = error;
  }
  if (commandError instanceof ProcessIdentityUnavailableError) {
    retainedAssignment(io, assigned, commandError);
    throw commandError;
  }
  const { repository } = await repositoryContext(sourceCwd);
  try {
    await releaseAssignment(repository, assigned.assignmentId, false);
    io.stderr.write(`Released ${assigned.path}\n`);
  } catch (releaseError) {
    retainedAssignment(io, assigned, releaseError);
    if (commandError) {
      throw new AggregateError([commandError, releaseError], "Command failed and its workspace could not be released");
    }
    return code === 0 ? 1 : code;
  }
  if (commandError) throw commandError;
  return code;
}

async function executeAssigned(args: readonly string[], cwd: string, io: CliIo): Promise<number> {
  const { acquireArgs, command } = splitExecArguments(args);
  if (acquireArgs.length === 0) throw new Error("exec requires a branch");
  if (command.length === 0) throw new Error("exec requires a command");
  const assigned = await acquire(acquireArgs, cwd, io, false);
  io.stderr.write(`Assigned ${assigned.path} (${assigned.assignmentId})\n`);
  return runAssignedAndRelease(assigned, command, cwd, io);
}

async function shell(args: readonly string[], cwd: string, io: CliIo): Promise<number> {
  const { options, positional } = parseOptions(args, {
    values: ["--from", "--ttl", "--owner", "--port"],
    flags: ["--fetch"],
  });
  requirePositionals(positional, 1, "shell requires exactly one branch name");
  const acquireArgs = [positional[0]!];
  if (options.from) acquireArgs.push("--from", options.from);
  if (options.ttl) acquireArgs.push("--ttl", options.ttl);
  if (options.owner) acquireArgs.push("--owner", options.owner);
  if (options.fetch) acquireArgs.push("--fetch");
  for (const name of options.ports ?? []) acquireArgs.push("--port", name);
  const assigned = await acquire(acquireArgs, cwd, io, false);
  const executable = process.env["RUK_SHELL"] ?? process.env["SHELL"] ??
    (process.platform === "win32" ? process.env["COMSPEC"] ?? "cmd.exe" : "/bin/sh");
  const sessionMarker = process.platform === "win32" || !process.stdin.isTTY
    ? undefined
    : path.join((await repositoryContext(assigned.path)).repository.commonDir, "ruk", `shell-${crypto.randomUUID()}`);
  const wrapper = process.platform === "darwin"
    ? `sleep 2147483647 </dev/null >/dev/null 2>&1 & sentinel=$!; started=$(ps -o lstart= -p "$sentinel"); terminal=$(ps -o tty= -p "$sentinel"); printf "%s\\n%s\\n%s\\n" "$sentinel" "$started" "$terminal" > "$RUK_SHELL_SESSION_FILE"; exec "$RUK_SHELL"`
    : `sid=$(ps -o sid= -p $$ | tr -d ' '); started=$(ps -o lstart= -p "$sid"); printf "%s\\n%s\\n" "$sid" "$started" > "$RUK_SHELL_SESSION_FILE"; exec "$RUK_SHELL"`;
  const command = !sessionMarker
    ? [executable]
    : process.platform === "darwin"
      ? ["/usr/bin/script", "-q", "/dev/null", "/bin/sh", "-c", wrapper]
      : ["/usr/bin/script", "-qec", wrapper, "/dev/null"];
  io.stderr.write(`Shell workspace: ${assigned.path}\nAssignment: ${assigned.assignmentId}\n`);
  const ignoreInterrupt = () => {};
  const previousShell = process.env["RUK_SHELL"];
  if (process.platform !== "win32" && sessionMarker) process.on("SIGINT", ignoreInterrupt);
  if (sessionMarker) {
    process.env["RUK_SHELL"] = executable;
    process.env["RUK_SHELL_SESSION_FILE"] = sessionMarker;
  }
  try {
    return await runAssignedAndRelease(
      assigned,
      command,
      cwd,
      io,
      process.platform !== "win32" && !sessionMarker,
      sessionMarker,
      process.platform !== "win32" && !sessionMarker,
    );
  } finally {
    if (process.platform !== "win32" && sessionMarker) process.off("SIGINT", ignoreInterrupt);
    if (sessionMarker) {
      if (previousShell === undefined) delete process.env["RUK_SHELL"];
      else process.env["RUK_SHELL"] = previousShell;
      delete process.env["RUK_SHELL_SESSION_FILE"];
      await fs.rm(sessionMarker, { force: true });
    }
  }
}

async function stats(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, { flags: ["--disk", "--json"] });
  requirePositionals(positional, 0, "stats does not accept positional arguments");
  const { repository } = await repositoryContext(cwd);
  const state = await readState(storePaths(repository.commonDir));
  const result = {
    ...usageStatistics(state),
    ...(options.disk ? { disk: await diskStatistics(state) } : {}),
  };
  if (options.json) io.stdout.write(jsonLine(result));
  else {
    io.stdout.write(`Acquisitions:       ${result.acquisitions}\n`);
    io.stdout.write(`Workspace reuses:   ${result.workspaceReuses}\n`);
    io.stdout.write(`Preparations:       ${result.preparations}\n`);
    io.stdout.write(`Preparation skips:  ${result.preparationSkips}\n`);
    io.stdout.write(`Preparation failures: ${result.preparationFailures}\n`);
    io.stdout.write(`Average prepare ms: ${result.averagePreparationMs}\n`);
    if ("disk" in result) io.stdout.write(`Estimated bytes avoided: ${result.disk.estimatedBytesAvoided}\n`);
  }
  return result;
}

export interface MainOptions {
  cwd?: string;
  stdout?: NodeJS.WriteStream;
  stderr?: NodeJS.WriteStream;
  distribution?: Distribution;
}

export async function main(argv: readonly string[], options: MainOptions = {}): Promise<number> {
  const cwd = options.cwd ?? process.cwd();
  const io = {
    stdout: options.stdout ?? process.stdout,
    stderr: options.stderr ?? process.stderr,
  };
  const [command = "help", ...args] = argv;
  if (command === "help" || command === "--help" || command === "-h") {
    io.stdout.write(HELP);
    return 0;
  }
  if (command === "--version" || command === "-v") {
    io.stdout.write(`${VERSION}\n`);
    return 0;
  }
  if (command === "init" || command === "sync") {
    const { options: parsed, positional } = parseOptions(args, { flags: ["--json"] });
    requirePositionals(positional, 0, `${command} does not accept positional arguments`);
    await sync(cwd, io, parsed.json ?? false);
    return 0;
  }
  if (command === "create") {
    await create(args, cwd, io);
    return 0;
  }
  if (command === "acquire") {
    await acquire(args, cwd, io);
    return 0;
  }
  if (command === "renew") {
    await renew(args, cwd, io);
    return 0;
  }
  if (command === "release") {
    await release(args, cwd, io);
    return 0;
  }
  if (command === "run") return execute(args, cwd, io);
  if (command === "exec") {
    if (args[0] === "--") return execute(args, cwd, io);
    return executeAssigned(args, cwd, io);
  }
  if (command === "warm") {
    await warm(args, cwd, io);
    return 0;
  }
  if (command === "shell") return shell(args, cwd, io);
  if (command === "list") {
    await list(args, cwd, io);
    return 0;
  }
  if (command === "status") {
    await status(args, cwd, io);
    return 0;
  }
  if (command === "stats") {
    await stats(args, cwd, io);
    return 0;
  }
  if (command === "remove") {
    await remove(args, cwd);
    return 0;
  }
  if (command === "gc") {
    await garbageCollect(args, cwd, io);
    return 0;
  }
  if (command === "update") {
    const { options: parsed, positional } = parseOptions(args, {
      flags: ["--check", "--json"],
    });
    requirePositionals(positional, 0, "update does not accept positional arguments");
    const result = await updateRuk({
      distribution: options.distribution ?? "package",
      checkOnly: parsed.check ?? false,
      reporter: dependencyReporter(io, parsed.json ?? false),
    });
    io.stdout.write(parsed.json ? jsonLine(result) : formatUpdate(result));
    return 0;
  }
  throw new Error(`Unknown command ${command}. Run ruk --help.`);
}
