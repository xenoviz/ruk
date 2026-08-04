import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { loadConfig, detectPackageManager } from "./config.js";
import {
  assertSharedBackendSupported,
  dependenciesPresent,
  ensureDependencies,
} from "./dependencies.js";
import { dependencyFingerprint } from "./fingerprint.js";
import {
  addWorktree,
  assignWorktree,
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
  markWorkspaceFailed,
  recordPreparingWorkspace,
  removeAssignmentProcess,
  renewAssignment,
  reserveAvailableWorkspace,
} from "./lifecycle.js";
import { withDirectoryLock } from "./lock.js";
import { killProcessTree, processIdentity, run } from "./process.js";
import { deleteTreeState, readState, storePaths, treeKey, treeLockPath } from "./state.js";
import type { CliIo, DependencyReporter, StorePaths, TrackedProcessRecord, WorkspaceRecord } from "./types.js";
import { formatUpdate, updateRuk } from "./update.js";
import type { Distribution } from "./update.js";
import { VERSION } from "./version.js";

const HELP = `Ruk — dependency-aware Git workspaces for parallel coding agents

Usage:
  ruk init [--json]
  ruk create <branch> [--path <directory>] [--from <ref>] [--detach] [--json]
  ruk acquire <branch> [--from <ref>] [--ttl <minutes>] [--owner <id>] [--json]
  ruk renew <assignment-id> [--ttl <minutes>] [--json]
  ruk release <assignment-id> [--force] [--json]
  ruk sync [--json]
  ruk run -- <command> [args...]
  ruk list [--json]
  ruk remove <path> [--force]
  ruk status [--json]
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
      (options as Record<string, string | boolean | undefined>)[key] = value;
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
        write: (message: string) => {
          io.stderr.write(message);
        },
        stdio: ["ignore", io.stderr, io.stderr],
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

async function canonicalPath(value: string): Promise<string> {
  try {
    return await fs.realpath(value);
  } catch {
    return path.resolve(value);
  }
}

async function acquire(args: readonly string[], cwd: string, io: CliIo) {
  const { options, positional } = parseOptions(args, {
    values: ["--from", "--ttl", "--owner"],
    flags: ["--json"],
  });
  requirePositionals(positional, 1, "acquire requires exactly one branch name");
  const branch = positional[0]!;
  const { repository } = await repositoryContext(cwd);
  const paths = storePaths(repository.commonDir);
  const ttl = minutes(options.ttl, 480, "--ttl");
  const assignmentBase = {
    owner: options.owner ?? process.env["RUK_AGENT_ID"] ?? `${os.hostname()}:${process.pid}`,
    hostname: os.hostname(),
    branch,
  };
  let workspace = await reserveAvailableWorkspace(paths, { ...assignmentBase, expiresAt: expiresIn(ttl) });
  const reused = workspace !== null;
  let operationId: string | null = null;

  if (!workspace) {
    const destination = path.join(
      path.dirname(repository.root),
      `${path.basename(repository.root)}-ruk-${crypto.randomUUID().slice(0, 8)}`,
    );
    workspace = await recordPreparingWorkspace(paths, { path: destination, branch });
    operationId = workspace.operationId;
  }

  try {
    if (!reused) {
      await addWorktree({
        cwd: repository.root,
        destination: workspace.path,
        branch,
        startPoint: options.from ?? "HEAD",
        detach: true,
        stdio: options.json ? ["ignore", io.stderr, io.stderr] : "inherit",
      });
      await lockWorktree(repository.root, workspace.path);
    }
    await assignWorktree({
      repository: repository.root,
      workspace: workspace.path,
      branch,
      startPoint: options.from ?? "HEAD",
    });
    const prepared = await sync(workspace.path, io, options.json ?? false, false);
    if (operationId) {
      workspace = await markWorkspaceAssigned(paths, workspace.path, operationId, {
        ...assignmentBase,
        expiresAt: expiresIn(ttl),
      });
    } else {
      workspace = await renewAssignment(paths, workspace.assignment!.id, expiresIn(ttl));
    }
    const result = {
      status: "assigned",
      assignmentId: workspace.assignment!.id,
      path: workspace.path,
      branch,
      expiresAt: workspace.assignment!.expiresAt,
      reused,
      fingerprint: prepared.fingerprint,
      mode: prepared.mode,
    };
    if (options.json) io.stdout.write(jsonLine(result));
    else io.stdout.write(`Assigned ${workspace.path}\nAssignment: ${result.assignmentId}\nExpires: ${result.expiresAt}\n`);
    return result;
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    try {
      if (operationId) {
        await markWorkspaceFailed(paths, workspace.path, operationId, message);
      } else if (workspace.assignment) {
        await beginWorkspaceReturn(paths, workspace.assignment.id);
        await returnWorktree(workspace.path, true);
        await finishWorkspaceReturn(paths, workspace.assignment.id);
      }
    } catch (cleanupError) {
      throw new AggregateError([error, cleanupError], `Workspace acquisition failed and cleanup also failed for ${workspace.path}`);
    }
    throw error;
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
    const identity = await processIdentity(record.pid);
    if (!identity || identity !== record.startedAt) return true;
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
    const identity = await processIdentity(record.pid);
    if (identity === record.startedAt) {
      await killProcessTree(record.groupId ?? record.pid, false, identity);
      cleaned += 1;
      if (!(await processExited(record, 1_500))) {
        if (!force) throw new Error(`Tracked process ${record.pid} survived graceful termination; retry with --force`);
        await killProcessTree(record.groupId ?? record.pid, true, identity);
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
): Promise<{ workspace: WorkspaceRecord; cleanedProcesses: number }> {
  const paths = storePaths(repository.commonDir);
  const matches = await findAssignments(paths, { id: assignmentId });
  if (matches.length !== 1) throw new Error(`Assignment ${assignmentId} does not exist`);
  const workspacePath = matches[0]!.workspace.path;
  return withDirectoryLock(treeLockPath(paths, workspacePath), async () => {
    let returning = false;
    try {
      const workspace = await beginWorkspaceReturn(paths, assignmentId);
      returning = true;
      const cleanedProcesses = await cleanTrackedProcesses(paths, assignmentId, workspace.processes, force);
      await returnWorktree(workspace.path, force);
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
    flags: ["--detach", "--json"],
  });
  requirePositionals(positional, 1, "create requires exactly one branch name");
  const branch = positional[0]!;
  const { repository, manager } = await context(cwd);
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
    startPoint: options.from ?? "HEAD",
    detach: options.detach ?? false,
    stdio: options.json ? ["ignore", io.stderr, io.stderr] : "inherit",
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
        stdio: options.json ? ["ignore", io.stderr, io.stderr] : "inherit",
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
  const { options, positional } = parseOptions(args, { flags: ["--json"] });
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
  const ready = record?.fingerprint === current.fingerprint && modulesPresent;
  const result = {
    path: value.repository.root,
    fingerprint: current.fingerprint,
    preparedFingerprint: record?.fingerprint ?? null,
    mode: record?.mode ?? null,
    nodeModulesPresent: modulesPresent,
    status: ready ? "ready" : "sync-required",
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
    io.stdout.write(`Lifecycle:   ${result.lifecycle ?? "unmanaged"}\n`);
    if (result.assignmentId) io.stdout.write(`Assignment:  ${result.assignmentId} (expires ${result.expiresAt})\n`);
    if (!ready) io.stdout.write("Next:        ruk sync\n");
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

async function collectWorkspace(
  repository: Awaited<ReturnType<typeof getRepository>>,
  paths: StorePaths,
  workspace: WorkspaceRecord,
): Promise<void> {
  await withDirectoryLock(treeLockPath(paths, workspace.path), async () => {
    const collecting = await beginWorkspaceCollection(paths, workspace.path, workspace.updatedAt);
    let gitRemoved = false;
    try {
      const worktrees = await listWorktrees(repository.root);
      const managedPath = await canonicalPath(workspace.path);
      if (await Promise.all(worktrees.map((entry) => canonicalPath(entry.path))).then((entries) => entries.includes(managedPath))) {
        await unlockWorktree(repository.root, workspace.path);
        await removeWorktree({ cwd: repository.root, destination: workspace.path, force: true });
      }
      gitRemoved = true;
      await deleteWorkspaceRecord(paths, workspace.path, collecting.operationId!);
      await deleteTreeState(paths, workspace.path);
    } catch (error) {
      if (!gitRemoved) {
        try { await cancelWorkspaceCollection(paths, workspace.path, collecting.operationId!); } catch { /* Preserve the original failure. */ }
      }
      throw error;
    }
  });
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
  const candidates = await identifyGcCandidates(paths, cutoff);
  const expiredCandidates = candidates.filter((candidate) => candidate.requiresForce);
  const safeCandidates = candidates.filter((candidate) => !candidate.requiresForce);
  const removed: Array<{ path: string; lifecycle: string; reason: string }> = [];

  if (options.apply) {
    for (const candidate of safeCandidates) {
      if (await canonicalPath(candidate.workspace.path) === current) continue;
      await collectWorkspace(repository, paths, candidate.workspace);
      removed.push({ path: candidate.workspace.path, lifecycle: candidate.workspace.lifecycle, reason: "older than max age" });
    }
    if (options.forceExpired) {
      for (const candidate of expiredCandidates) {
        if (await canonicalPath(candidate.workspace.path) === current) continue;
        const assignmentId = candidate.workspace.assignment!.id;
        const released = await releaseAssignment(repository, assignmentId, true);
        await collectWorkspace(repository, paths, released.workspace);
        removed.push({ path: candidate.workspace.path, lifecycle: candidate.workspace.lifecycle, reason: "expired assignment (forced)" });
      }
    }
  } else {
    for (const candidate of safeCandidates) {
      if (await canonicalPath(candidate.workspace.path) === current) continue;
      removed.push({ path: candidate.workspace.path, lifecycle: candidate.workspace.lifecycle, reason: "older than max age" });
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

async function execute(args: readonly string[], cwd: string, io: CliIo): Promise<number> {
  const separator = args.indexOf("--");
  if (separator < 0) throw new Error("run requires -- before the command");
  const command = args.slice(separator + 1);
  if (command.length === 0) throw new Error("run requires a command after --");
  if (separator > 0) throw new Error(`Unknown run option ${args[0]}`);
  const { repository } = await context(cwd);
  await sync(repository.root, io);
  const [program, ...programArgs] = command;
  const paths = storePaths(repository.commonDir);
  const lifecycle = (await readState(paths)).workspaces[treeKey(repository.root)];
  if (!lifecycle || lifecycle.lifecycle !== "assigned" || !lifecycle.assignment) {
    const result = await run(program!, programArgs, {
      cwd: repository.root,
      stdio: "inherit",
      allowFailure: true,
    });
    return result.code;
  }

  const assignmentId = lifecycle.assignment.id;
  const tracking: { record?: TrackedProcessRecord } = {};
  let execution!: ReturnType<typeof run>;
  await withDirectoryLock(treeLockPath(paths, repository.root), async () => {
    let registered!: () => void;
    const registration = new Promise<void>((resolve) => { registered = resolve; });
    execution = run(program!, programArgs, {
      cwd: repository.root,
      stdio: "inherit",
      allowFailure: true,
      detached: true,
      onSpawn: async (pid) => {
        const startedAt = await processIdentity(pid);
        if (!startedAt) throw new Error(`Could not identify process ${pid}`);
        const record: TrackedProcessRecord = {
          pid,
          ...(process.platform === "win32" ? {} : { groupId: pid }),
          command: [...command],
          startedAt,
        };
        await addAssignmentProcess(paths, assignmentId, record);
        tracking.record = record;
        registered();
      },
    });
    await Promise.race([registration, execution.then(() => undefined)]);
  });
  const result = await execution;
  if (tracking.record) {
    try {
      await removeAssignmentProcess(paths, assignmentId, tracking.record.pid, tracking.record.startedAt);
    } catch {
      // Release may already have consumed the tracked record.
    }
  }
  return result.code;
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
  if (command === "run" || command === "exec") return execute(args, cwd, io);
  if (command === "list") {
    await list(args, cwd, io);
    return 0;
  }
  if (command === "status") {
    await status(args, cwd, io);
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
