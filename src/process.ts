import { spawn } from "node:child_process";
import type { StdioOptions } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { isErrnoException } from "./types.js";
import type { TrackedProcessRecord } from "./types.js";

export interface RunOptions {
  cwd?: string;
  env?: NodeJS.ProcessEnv;
  stdio?: StdioOptions;
  allowFailure?: boolean;
  detached?: boolean;
  onSpawn?: (pid: number) => void | Promise<void>;
  signal?: AbortSignal;
}

export interface RunResult {
  code: number;
  signal: NodeJS.Signals | null;
  stdout: string;
  stderr: string;
}

export class ProcessIdentityUnavailableError extends Error {
  readonly pid: number;

  constructor(pid: number) {
    super(`Process ${pid} could not be identified, so its workspace cannot be released safely`);
    this.name = "ProcessIdentityUnavailableError";
    this.pid = pid;
  }
}

interface IdentityProbeRequest {
  resolve: (identity: string | null) => void;
  reject: (error: unknown) => void;
}

interface IdentityCacheEntry {
  identity: string | null;
  observedAt: number;
}

export interface BoundedIdentityProbeOptions {
  cacheDurationMs?: number;
  cacheNull?: boolean;
  maxCacheEntries?: number;
  maxBatchSize?: number;
}

export function createBoundedIdentityProbe(
  probe: (pids: readonly number[]) => Promise<ReadonlyMap<number, string | null>>,
  options: BoundedIdentityProbeOptions = {},
): (pid: number, fresh?: boolean) => Promise<string | null> {
  const cacheDurationMs = options.cacheDurationMs ?? 250;
  const cacheNull = options.cacheNull ?? true;
  const maxCacheEntries = options.maxCacheEntries ?? 256;
  const maxBatchSize = Math.max(1, Math.floor(options.maxBatchSize ?? 128));
  const cache = new Map<number, IdentityCacheEntry>();
  const pending = new Map<number, IdentityProbeRequest[]>();
  let drainScheduled = false;
  let draining = false;

  const remember = (pid: number, identity: string | null): void => {
    cache.delete(pid);
    if (identity === null && !cacheNull) return;
    cache.set(pid, { identity, observedAt: Date.now() });
    while (cache.size > maxCacheEntries) {
      const oldest = cache.keys().next().value as number | undefined;
      if (oldest === undefined) break;
      cache.delete(oldest);
    }
  };

  const drain = async (): Promise<void> => {
    if (draining) return;
    draining = true;
    drainScheduled = false;
    try {
      while (pending.size > 0) {
        const batch = new Map([...pending].slice(0, maxBatchSize));
        for (const pid of batch.keys()) pending.delete(pid);
        const pids = [...batch.keys()];
        try {
          const identities = await probe(pids);
          for (const pid of pids) {
            if (!identities.has(pid)) throw new Error(`Process identity probe omitted PID ${pid}`);
          }
          for (const [pid, requests] of batch) {
            const identity = identities.get(pid) ?? null;
            remember(pid, identity);
            for (const request of requests) request.resolve(identity);
          }
        } catch (error) {
          for (const requests of batch.values()) {
            for (const request of requests) request.reject(error);
          }
        }
      }
    } finally {
      draining = false;
      if (pending.size > 0 && !drainScheduled) {
        drainScheduled = true;
        queueMicrotask(() => void drain());
      }
    }
  };

  return (pid: number, fresh = false): Promise<string | null> => {
    if (!fresh) {
      const cached = cache.get(pid);
      if (cached && Date.now() - cached.observedAt < cacheDurationMs) return Promise.resolve(cached.identity);
    }
    return new Promise((resolve, reject) => {
      const requests = pending.get(pid) ?? [];
      requests.push({ resolve, reject });
      pending.set(pid, requests);
      if (!draining && !drainScheduled) {
        drainScheduled = true;
        queueMicrotask(() => void drain());
      }
    });
  };
}

function environmentValue(environment: NodeJS.ProcessEnv, name: string): string | undefined {
  if (environment[name] !== undefined) return environment[name];
  const key = Object.keys(environment).find((candidate) => candidate.toUpperCase() === name);
  return key ? environment[key] : undefined;
}

async function windowsCommandShim(
  command: string,
  cwd: string,
  environment: NodeJS.ProcessEnv,
): Promise<string | null> {
  if (process.platform !== "win32") return null;
  const extensions = (environmentValue(environment, "PATHEXT") ?? ".COM;.EXE;.BAT;.CMD").split(";");
  const hasPath = path.isAbsolute(command) || command.includes("/") || command.includes("\\");
  const directories = hasPath
    ? [""]
    : [cwd, ...(environmentValue(environment, "PATH") ?? "").split(path.delimiter).filter(Boolean)];
  const base = hasPath ? path.resolve(cwd, command) : command;
  const suffixes = path.extname(base) ? [""] : extensions;

  for (const directory of directories) {
    for (const suffix of suffixes) {
      const candidate = path.resolve(directory || ".", `${base}${suffix}`);
      try {
        await fs.access(candidate);
        return /\.(?:cmd|bat)$/i.test(candidate) ? candidate : null;
      } catch {
        // Continue searching PATH.
      }
    }
  }
  return null;
}

export async function run(
  command: string,
  args: readonly string[] = [],
  options: RunOptions = {},
): Promise<RunResult> {
  const environment = options.env ?? process.env;
  const cwd = options.cwd ?? process.cwd();
  const shim = await windowsCommandShim(command, cwd, environment);
  const executable = shim
    ? path.join(environmentValue(environment, "SYSTEMROOT") ?? "C:\\Windows", "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
    : command;
  const spawnArgs = shim
    ? [
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-Command",
        "$command=$env:RUK_CHILD_COMMAND;$arguments=@($env:RUK_CHILD_ARGS|ConvertFrom-Json);& $command @arguments;exit $LASTEXITCODE",
      ]
    : args;
  const childEnvironment = shim
    ? { ...environment, RUK_CHILD_COMMAND: shim, RUK_CHILD_ARGS: JSON.stringify(args) }
    : environment;
  return new Promise((resolve, reject) => {
    const child = spawn(executable, spawnArgs, {
      cwd,
      env: childEnvironment,
      stdio: options.stdio ?? ["ignore", "pipe", "pipe"],
      shell: false,
      windowsHide: true,
      detached: options.detached ?? false,
    });

    let stdout = "";
    let stderr = "";
    let spawnHook = Promise.resolve();
    let abortCleanup = Promise.resolve();
    let spawnError: unknown;
    let spawnedIdentity: Promise<string | null> | undefined;
    let aborting = false;

    const abort = () => {
      if (aborting) return;
      spawnError = options.signal?.reason ?? new Error(`${command} was aborted`);
      if (!child.pid) return;
      aborting = true;
      spawnedIdentity ??= freshProcessIdentity(child.pid).catch(() => null);
      abortCleanup = spawnedIdentity.then(async (expectedIdentity) => {
        try {
          const killed = await terminateSpawnedProcess(child.pid!, options.detached ?? false, expectedIdentity);
          if (!killed) child.kill("SIGKILL");
        } catch (cleanupError) {
          child.kill("SIGKILL");
          if (cleanupError instanceof ProcessIdentityUnavailableError) spawnError = cleanupError;
        }
      });
    };

    child.once("spawn", () => {
      if (child.pid && (options.signal || options.onSpawn)) {
        // Identity probes call run() without cancellation or a spawn hook. Keep
        // this gate so a PowerShell probe never recursively identifies itself.
        spawnedIdentity ??= freshProcessIdentity(child.pid).catch(() => null);
      }
      if (options.signal?.aborted) abort();
      if (!options.onSpawn) return;
      if (!child.pid) {
        spawnError = new Error(`Could not track ${command}: child PID is unavailable`);
        child.kill();
        return;
      }
      spawnHook = Promise.resolve().then(() => options.onSpawn!(child.pid!)).catch(async (error: unknown) => {
        spawnError = error;
        try {
          const expectedIdentity = await spawnedIdentity!;
          const killed = await terminateSpawnedProcess(child.pid!, options.detached ?? false, expectedIdentity);
          if (!killed) child.kill("SIGKILL");
        } catch (cleanupError) {
          child.kill("SIGKILL");
          if (cleanupError instanceof ProcessIdentityUnavailableError) spawnError = cleanupError;
        }
      });
    });

    if (options.signal?.aborted) abort();
    else options.signal?.addEventListener("abort", abort, { once: true });

    if (child.stdout) {
      child.stdout.setEncoding("utf8");
      child.stdout.on("data", (chunk) => {
        stdout += chunk;
      });
    }
    if (child.stderr) {
      child.stderr.setEncoding("utf8");
      child.stderr.on("data", (chunk) => {
        stderr += chunk;
      });
    }

    child.on("error", reject);
    child.on("close", async (code, signal) => {
      options.signal?.removeEventListener("abort", abort);
      await Promise.all([spawnHook, abortCleanup]);
      if (spawnError) {
        reject(spawnError);
        return;
      }
      const result = { code: code ?? (signal ? 128 + os.constants.signals[signal] : 1), signal, stdout, stderr };
      if (result.code === 0 || options.allowFailure) {
        resolve(result);
        return;
      }
      const detail = stderr.trim() || stdout.trim();
      reject(
        new Error(
          `${command} ${args.join(" ")} failed with exit code ${result.code}${
            detail ? `: ${detail}` : ""
          }`,
        ),
      );
    });
  });
}

async function probeWindowsProcessIdentities(pids: readonly number[]): Promise<ReadonlyMap<number, string | null>> {
  const powershell = path.join(
    environmentValue(process.env, "SYSTEMROOT") ?? "C:\\Windows",
    "System32",
    "WindowsPowerShell",
    "v1.0",
    "powershell.exe",
  );
  const result = await serializedWindowsProcessProbe(() => run(
    powershell,
    [
      "-NoLogo",
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      "$processIds=@($env:RUK_PROCESS_IDS|ConvertFrom-Json);foreach($processId in $processIds){$item=Get-Process -Id $processId -ErrorAction SilentlyContinue;if(-not $item){Write-Output \"$processId|\";continue};try{Write-Output \"$processId|$($item.StartTime.ToUniversalTime().Ticks)\"}catch{Write-Error $_;exit 1}}",
    ],
    { allowFailure: true, env: { ...process.env, RUK_PROCESS_IDS: JSON.stringify(pids) } },
  ));
  if (result.code !== 0) throw new ProcessIdentityUnavailableError(pids[0] ?? 0);
  const identities = new Map<number, string | null>();
  for (const line of result.stdout.trim().split(/\r?\n/).filter(Boolean)) {
    const match = /^(\d+)\|(\d*)$/.exec(line.trim());
    if (!match) throw new ProcessIdentityUnavailableError(pids[0] ?? 0);
    identities.set(Number(match[1]), match[2] || null);
  }
  return identities;
}

const windowsProcessIdentity = createBoundedIdentityProbe(probeWindowsProcessIdentities);

let windowsProcessProbeTail = Promise.resolve();

async function serializedWindowsProcessProbe<T>(probe: () => Promise<T>): Promise<T> {
  const previous = windowsProcessProbeTail;
  let release!: () => void;
  windowsProcessProbeTail = new Promise<void>((resolve) => { release = resolve; });
  await previous;
  try {
    return await probe();
  } finally {
    release();
  }
}

async function probeWindowsProcessDescendants(pids: readonly number[]): Promise<ReadonlyMap<number, string | null>> {
  const powershell = path.join(
    environmentValue(process.env, "SYSTEMROOT") ?? "C:\\Windows",
    "System32",
    "WindowsPowerShell",
    "v1.0",
    "powershell.exe",
  );
  const result = await serializedWindowsProcessProbe(() => run(
    powershell,
    [
      "-NoLogo",
      "-NoProfile",
      "-NonInteractive",
      "-Command",
      "$ErrorActionPreference='Stop';$roots=@($env:RUK_PROCESS_IDS|ConvertFrom-Json);$all=@(Get-CimInstance Win32_Process);foreach($root in $roots){$pending=@([int]$root);$found=$false;while($pending.Count){$children=@($all|Where-Object{$pending -contains [int]$_.ParentProcessId});if(!$children.Count){break};$found=$true;$pending=@($children|ForEach-Object{[int]$_.ProcessId})};if($found){Write-Output \"$root|1\"}else{Write-Output \"$root|0\"}}",
    ],
    { allowFailure: true, env: { ...process.env, RUK_PROCESS_IDS: JSON.stringify(pids) } },
  ));
  if (result.code !== 0) throw new Error(`Could not enumerate Windows processes: ${result.stderr.trim()}`);
  const descendants = new Map<number, string | null>();
  for (const line of result.stdout.trim().split(/\r?\n/).filter(Boolean)) {
    const match = /^(\d+)\|([01])$/.exec(line.trim());
    if (!match) throw new Error("Could not parse Windows process enumeration");
    descendants.set(Number(match[1]), match[2] === "1" ? "1" : null);
  }
  return descendants;
}

const windowsProcessDescendants = createBoundedIdentityProbe(probeWindowsProcessDescendants, { cacheNull: false });

async function readProcessIdentity(pid: number, fresh: boolean): Promise<string | null> {
  if (!Number.isInteger(pid) || pid <= 0) return null;
  if (process.platform === "win32") return windowsProcessIdentity(pid, fresh);
  const result = await run("ps", ["-o", "lstart=", "-p", String(pid)], { allowFailure: true });
  return result.code === 0 && result.stdout.trim() ? result.stdout.trim().replace(/\s+/g, " ") : null;
}

export async function processIdentity(pid: number): Promise<string | null> {
  return readProcessIdentity(pid, false);
}

async function freshProcessIdentity(pid: number): Promise<string | null> {
  return readProcessIdentity(pid, true);
}

export async function requireProcessIdentity(
  pid: number,
  identify: (pid: number) => Promise<string | null> = processIdentity,
  descendantsExist: (pid: number) => Promise<boolean> = freshProcessDescendantsExist,
): Promise<string | null> {
  const identity = await identify(pid);
  if (!identity) {
    let hasDescendants: boolean;
    try {
      hasDescendants = await descendantsExist(pid);
    } catch {
      throw new ProcessIdentityUnavailableError(pid);
    }
    if (hasDescendants) throw new ProcessIdentityUnavailableError(pid);
  }
  return identity;
}

async function readProcessDescendantsExist(pid: number, fresh: boolean): Promise<boolean> {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  if (process.platform === "win32") {
    return await windowsProcessDescendants(pid, fresh) === "1";
  }
  const result = await run("ps", ["-e", "-o", "pid=", "-o", "ppid=", "-o", "pgid=", "-o", "stat="], {
    allowFailure: true,
  });
  if (result.code !== 0) throw new Error(`Could not enumerate POSIX processes: ${result.stderr.trim()}`);
  const processes = result.stdout.split(/\r?\n/).map((line) => line.trim().split(/\s+/))
    .filter((entry) => entry.length === 4 && !entry[3]!.startsWith("Z"))
    .map(([child, parent, group]) => ({ pid: Number(child), parent: Number(parent), group: Number(group) }))
    .filter((entry) => [entry.pid, entry.parent, entry.group].every(Number.isSafeInteger));
  const parents = new Set([pid]);
  while (true) {
    const children = processes.filter((entry) => parents.has(entry.parent) && !parents.has(entry.pid));
    if (children.length === 0) break;
    for (const child of children) parents.add(child.pid);
  }
  return parents.size > 1 || processes.some((entry) => entry.group === pid);
}

export async function processDescendantsExist(pid: number): Promise<boolean> {
  return readProcessDescendantsExist(pid, false);
}

async function freshProcessDescendantsExist(pid: number): Promise<boolean> {
  return readProcessDescendantsExist(pid, true);
}

interface PosixProcess {
  pid: number;
  sessionId?: number;
  terminalId?: string;
  state: string;
}

async function posixProcesses(): Promise<PosixProcess[]> {
  const scopeField = process.platform === "darwin" ? "tty" : "sid";
  const result = await run("ps", ["-e", "-o", "pid=", "-o", `${scopeField}=`, "-o", "stat="], {
    allowFailure: true,
  });
  if (result.code !== 0) throw new Error(`Could not enumerate POSIX processes: ${result.stderr.trim()}`);
  return result.stdout.split(/\r?\n/).map((line) => line.trim().split(/\s+/))
    .filter((entry) => entry.length === 3 && Number.isSafeInteger(Number(entry[0])))
    .flatMap(([rawPid, scope, state]): PosixProcess[] => {
      const pid = Number(rawPid);
      if (process.platform === "darwin") return [{ pid, terminalId: scope!, state: state! }];
      const sessionId = Number(scope);
      return Number.isSafeInteger(sessionId) ? [{ pid, sessionId, state: state! }] : [];
    });
}

export async function requireChildProcessSession(
  pid: number,
  marker: string,
): Promise<{
  pid: number;
  startedAt: string;
  sessionId?: number;
  sessionStartedAt?: string;
  terminalId?: string;
}> {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    try {
      const [rawSessionId, rawIdentity, rawTerminalId] = (await fs.readFile(marker, "utf8")).trim().split(/\r?\n/);
      const sessionId = Number(rawSessionId?.trim());
      const sessionStartedAt = rawIdentity?.trim().replace(/\s+/g, " ") ?? "";
      if (Number.isSafeInteger(sessionId) && sessionId > 0 && sessionStartedAt) {
        const terminalId = rawTerminalId?.trim();
        if (process.platform === "darwin") {
          if (terminalId && terminalId !== "??") return { pid: sessionId, startedAt: sessionStartedAt, terminalId };
        } else {
          return { pid: sessionId, startedAt: sessionStartedAt, sessionId, sessionStartedAt };
        }
      }
    } catch (error) {
      if (!isErrnoException(error) || error.code !== "ENOENT") throw error;
    }
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
  throw new ProcessIdentityUnavailableError(pid);
}

async function processSessionExists(sessionId: number): Promise<boolean> {
  return (await posixProcesses()).some((process) => process.sessionId === sessionId && !process.state.startsWith("Z"));
}

async function processTerminalExists(terminalId: string): Promise<boolean> {
  return (await posixProcesses()).some((process) =>
    process.terminalId === terminalId && !process.state.startsWith("Z")
  );
}

async function terminateProcessSession(
  sessionId: number,
  sessionStartedAt: string,
  force: boolean,
): Promise<boolean> {
  const leaderIdentity = await processIdentity(sessionId);
  if (!leaderIdentity && await processSessionExists(sessionId)) throw new ProcessIdentityUnavailableError(sessionId);
  if (!leaderIdentity) return false;
  if (leaderIdentity !== sessionStartedAt) {
    throw new Error(`Refusing to terminate reused process session ${sessionId}`);
  }
  const members = (await posixProcesses()).filter((process) =>
    process.sessionId === sessionId && !process.state.startsWith("Z")
  );
  for (const member of members) {
    if (member.pid === process.pid) throw new Error(`Refusing to terminate current process session ${sessionId}`);
    try {
      process.kill(member.pid, force ? "SIGKILL" : "SIGTERM");
    } catch (error) {
      if (!isMissingProcess(error)) throw error;
    }
  }
  return members.length > 0;
}

async function terminateProcessTerminal(
  terminalId: string,
  leaderPid: number,
  leaderStartedAt: string,
  force: boolean,
): Promise<boolean> {
  const leaderIdentity = await processIdentity(leaderPid);
  if (!leaderIdentity) {
    if (await processTerminalExists(terminalId)) throw new ProcessIdentityUnavailableError(leaderPid);
    return false;
  }
  if (leaderIdentity !== leaderStartedAt) {
    throw new Error(`Refusing to terminate reused process ${leaderPid}`);
  }
  const members = (await posixProcesses()).filter((process) =>
    process.terminalId === terminalId && !process.state.startsWith("Z")
  );
  for (const member of members) {
    if (member.pid === process.pid) throw new Error(`Refusing to terminate current terminal ${terminalId}`);
    try {
      process.kill(member.pid, force ? "SIGKILL" : "SIGTERM");
    } catch (error) {
      if (!isMissingProcess(error)) throw error;
    }
  }
  return members.length > 0;
}

export async function trackedProcessExists(
  record: TrackedProcessRecord,
  identify: (pid: number) => Promise<string | null> = processIdentity,
  descendantsExist: (pid: number) => Promise<boolean> = processDescendantsExist,
): Promise<boolean> {
  if (record.terminalId !== undefined) return processTerminalExists(record.terminalId);
  if (record.sessionId !== undefined) return processSessionExists(record.sessionId);
  if (record.groupId !== undefined) return descendantsExist(record.groupId);
  const identity = await identify(record.pid);
  if (identity === record.startedAt) return true;
  if (await descendantsExist(record.pid)) throw new ProcessIdentityUnavailableError(record.pid);
  return false;
}

export async function terminateTrackedProcess(record: TrackedProcessRecord, force = false): Promise<boolean> {
  if (record.terminalId !== undefined) {
    return terminateProcessTerminal(record.terminalId, record.pid, record.startedAt, force);
  }
  if (record.sessionId !== undefined && record.sessionStartedAt !== undefined) {
    return terminateProcessSession(record.sessionId, record.sessionStartedAt, force);
  }
  const identity = await freshProcessIdentity(record.pid);
  if (identity === record.startedAt) {
    return killProcessTree(record.groupId ?? record.pid, force, identity);
  }
  if (identity) return false;
  if (await freshProcessDescendantsExist(record.groupId ?? record.pid)) {
    throw new ProcessIdentityUnavailableError(record.pid);
  }
  return false;
}

export async function killProcessTree(
  pid: number,
  force = false,
  expectedIdentity?: string,
): Promise<boolean> {
  if (!Number.isInteger(pid) || pid <= 0 || pid === process.pid) {
    throw new Error(`Refusing to terminate invalid process ${pid}`);
  }
  const identity = await freshProcessIdentity(pid);
  if (!identity) return false;
  if (expectedIdentity && identity !== expectedIdentity) {
    throw new Error(`Refusing to terminate reused process ID ${pid}`);
  }
  if (process.platform === "win32") {
    const result = await run("taskkill", ["/PID", String(pid), "/T", ...(force ? ["/F"] : [])], {
      allowFailure: true,
    });
    if (result.code !== 0 && !/not found|no running instance/i.test(`${result.stderr}\n${result.stdout}`)) {
      if (!force) return true;
      throw new Error(`Could not terminate process tree ${pid}: ${result.stderr.trim() || result.stdout.trim()}`);
    }
    return true;
  }
  try {
    process.kill(-pid, force ? "SIGKILL" : "SIGTERM");
  } catch (error) {
    if (isMissingProcess(error)) return false;
    throw error;
  }
  return true;
}

async function terminateSpawnedProcess(pid: number, detached: boolean, expectedIdentity: string | null): Promise<boolean> {
  if (process.platform === "win32") {
    if (expectedIdentity && await killProcessTree(pid, true, expectedIdentity)) return true;
    throw new ProcessIdentityUnavailableError(pid);
  }
  if (detached) {
    const identity = await freshProcessIdentity(pid);
    if (identity && expectedIdentity && identity !== expectedIdentity) throw new Error(`Refusing to terminate reused process ID ${pid}`);
    try { process.kill(-pid, "SIGKILL"); return true; } catch (error) { if (!isMissingProcess(error)) throw error; }
    return false;
  }
  if (!expectedIdentity) return false;
  const identity = await freshProcessIdentity(pid);
  if (!identity) return false;
  if (identity !== expectedIdentity) throw new Error(`Refusing to terminate reused process ID ${pid}`);
  const result = await run("ps", ["-e", "-o", "pid=", "-o", "ppid=", "-o", "stat="], { allowFailure: true });
  if (result.code !== 0) throw new Error(`Could not enumerate POSIX processes: ${result.stderr.trim()}`);
  const processes = result.stdout.split(/\r?\n/).map((line) => line.trim().split(/\s+/))
    .filter((entry) => entry.length === 3 && !entry[2]!.startsWith("Z"))
    .map(([rawPid, rawParent]) => ({ pid: Number(rawPid), parent: Number(rawParent) }))
    .filter((entry) => Number.isSafeInteger(entry.pid) && Number.isSafeInteger(entry.parent));
  const descendants: number[] = [];
  const parents = new Set([pid]);
  while (true) {
    const children = processes.filter((entry) => parents.has(entry.parent) && !parents.has(entry.pid));
    if (children.length === 0) break;
    for (const child of children) {
      parents.add(child.pid);
      descendants.push(child.pid);
    }
  }
  for (const childPid of descendants.reverse()) {
    try { process.kill(childPid, "SIGKILL"); } catch (error) { if (!isMissingProcess(error)) throw error; }
  }
  try {
    process.kill(pid, "SIGKILL");
    return true;
  } catch (error) {
    if (isMissingProcess(error)) return descendants.length > 0;
    throw error;
  }
}

function isMissingProcess(error: unknown): boolean {
  return error instanceof Error && "code" in error && error.code === "ESRCH";
}

export async function commandExists(command: string): Promise<boolean> {
  if (path.isAbsolute(command) || command.includes("/") || command.includes("\\")) {
    try {
      await fs.access(command);
      return true;
    } catch {
      return false;
    }
  }
  const locator = process.platform === "win32" ? "where" : "sh";
  const args = process.platform === "win32" ? [command] : ["-c", "command -v \"$1\" >/dev/null 2>&1", "sh", command];
  const result = await run(locator, args, { allowFailure: true });
  return result.code === 0;
}
