import path from "node:path";
import { loadConfig, detectPackageManager } from "./config.js";
import {
  assertSharedBackendSupported,
  dependenciesPresent,
  ensureDependencies,
} from "./dependencies.js";
import { dependencyFingerprint } from "./fingerprint.js";
import { addWorktree, getRepository, listWorktrees, removeWorktree } from "./git.js";
import { run } from "./process.js";
import { deleteTreeState, readState, storePaths, treeKey } from "./state.js";
import type { CliIo, DependencyReporter } from "./types.js";
import { formatUpdate, parseUpdateInstaller, updateRuk } from "./update.js";
import type { Distribution, UpdateInstaller } from "./update.js";
import { VERSION } from "./version.js";

const HELP = `Ruk — dependency-aware Git workspaces for parallel coding agents

Usage:
  ruk init [--json]
  ruk create <branch> [--path <directory>] [--from <ref>] [--detach] [--json]
  ruk sync [--json]
  ruk run -- <command> [args...]
  ruk list [--json]
  ruk remove <path> [--force]
  ruk status [--json]
  ruk update [--check] [--via <npm|bun|pnpm|yarn>] [--json]

Ruk uses safe managed installs by default. Set dependencyMode to "shared" in
.rukrc.json only after validating the repository with its normal CI suite.
`;

interface ParsedOptions {
  path?: string;
  from?: string;
  detach?: boolean;
  json?: boolean;
  force?: boolean;
  check?: boolean;
  via?: string;
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
      (options as Record<string, string | boolean | undefined>)[argument.slice(2)] = true;
      continue;
    }
    if (valueOptions.has(argument)) {
      const value = args[index + 1];
      if (!value || value.startsWith("--")) throw new Error(`${argument} requires a value`);
      (options as Record<string, string | boolean | undefined>)[argument.slice(2)] = value;
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

async function sync(cwd: string, io: CliIo, json = false) {
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
  io.stdout.write(
    json
      ? jsonLine(output)
      : `${result.alreadyAttached ? "Dependencies already ready" : "Dependencies prepared"} for ${result.fingerprint.slice(0, 12)} (${result.mode}).\n`,
  );
  return { ...result, path: value.repository.root };
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
    return {
      ...workspace,
      fingerprint: record?.fingerprint ?? null,
      mode: record?.mode ?? null,
      status: record ? "prepared" : "not-prepared",
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
  }
  return result;
}

async function remove(args: readonly string[], cwd: string) {
  const { options, positional } = parseOptions(args, { flags: ["--force"] });
  requirePositionals(positional, 1, "remove requires exactly one workspace path");
  const { repository } = await repositoryContext(cwd);
  const destination = path.resolve(cwd, positional[0]!);
  if (destination === repository.root) throw new Error("Refusing to remove the current workspace");
  await removeWorktree({ cwd: repository.root, destination, force: options.force ?? false });
  await deleteTreeState(storePaths(repository.commonDir), destination);
}

async function execute(args: readonly string[], cwd: string): Promise<number> {
  const separator = args.indexOf("--");
  if (separator < 0) throw new Error("run requires -- before the command");
  const command = args.slice(separator + 1);
  if (command.length === 0) throw new Error("run requires a command after --");
  if (separator > 0) throw new Error(`Unknown run option ${args[0]}`);
  const { repository } = await context(cwd);
  await sync(repository.root, { stdout: process.stdout, stderr: process.stderr });
  const [program, ...programArgs] = command;
  const result = await run(program!, programArgs, {
    cwd: repository.root,
    stdio: "inherit",
    allowFailure: true,
  });
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
  if (command === "run" || command === "exec") return execute(args, cwd);
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
  if (command === "update") {
    const { options: parsed, positional } = parseOptions(args, {
      values: ["--via"],
      flags: ["--check", "--json"],
    });
    requirePositionals(positional, 0, "update does not accept positional arguments");
    const installer: UpdateInstaller | undefined = parsed.via
      ? parseUpdateInstaller(parsed.via)
      : undefined;
    if ((options.distribution ?? "package") === "standalone" && installer) {
      throw new Error("--via is only valid for package-manager installations");
    }
    const result = await updateRuk({
      distribution: options.distribution ?? "package",
      checkOnly: parsed.check ?? false,
      reporter: dependencyReporter(io, parsed.json ?? false),
      ...(installer ? { installer } : {}),
    });
    io.stdout.write(parsed.json ? jsonLine(result) : formatUpdate(result));
    return 0;
  }
  throw new Error(`Unknown command ${command}. Run ruk --help.`);
}
