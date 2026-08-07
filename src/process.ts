import { spawn } from "node:child_process";
import type { StdioOptions } from "node:child_process";
import fs from "node:fs/promises";
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
    let spawnError: unknown;

    child.once("spawn", () => {
      if (!options.onSpawn) return;
      if (!child.pid) {
        spawnError = new Error(`Could not track ${command}: child PID is unavailable`);
        child.kill();
        return;
      }
      spawnHook = Promise.resolve().then(() => options.onSpawn!(child.pid!)).catch(async (error: unknown) => {
        spawnError = error;
        try {
          await killProcessTree(child.pid!, true);
        } catch {
          child.kill();
        }
      });
    });

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
      await spawnHook;
      if (spawnError) {
        reject(spawnError);
        return;
      }
      const result = { code: code ?? 1, signal, stdout, stderr };
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

export async function processIdentity(pid: number): Promise<string | null> {
  if (!Number.isInteger(pid) || pid <= 0) return null;
  if (process.platform === "win32") {
    const powershell = path.join(
      environmentValue(process.env, "SYSTEMROOT") ?? "C:\\Windows",
      "System32",
      "WindowsPowerShell",
      "v1.0",
      "powershell.exe",
    );
    const result = await run(
      powershell,
      [
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-Command",
        `$process=Get-Process -Id ${pid} -ErrorAction SilentlyContinue;if($process){$process.StartTime.ToUniversalTime().Ticks}`,
      ],
      { allowFailure: true },
    );
    return result.code === 0 && result.stdout.trim() ? result.stdout.trim() : null;
  }
  const result = await run("ps", ["-o", "lstart=", "-p", String(pid)], { allowFailure: true });
  return result.code === 0 && result.stdout.trim() ? result.stdout.trim().replace(/\s+/g, " ") : null;
}

export async function requireProcessIdentity(
  pid: number,
  identify: (pid: number) => Promise<string | null> = processIdentity,
  descendantsExist: (pid: number) => Promise<boolean> = processDescendantsExist,
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

export async function processDescendantsExist(pid: number): Promise<boolean> {
  if (!Number.isInteger(pid) || pid <= 0) return false;
  if (process.platform === "win32") {
    const powershell = path.join(
      environmentValue(process.env, "SYSTEMROOT") ?? "C:\\Windows",
      "System32",
      "WindowsPowerShell",
      "v1.0",
      "powershell.exe",
    );
    const result = await run(
      powershell,
      [
        "-NoLogo",
        "-NoProfile",
        "-NonInteractive",
        "-Command",
        `$pending=@(${pid});$found=$false;$all=@(Get-CimInstance Win32_Process);while($pending.Count){$children=@($all|Where-Object{$pending -contains [int]$_.ParentProcessId});if(!$children.Count){break};$found=$true;$pending=@($children|ForEach-Object{[int]$_.ProcessId})};if($found){'1'}`,
      ],
      { allowFailure: true },
    );
    return result.code !== 0 || result.stdout.trim() === "1";
  }
  const result = await run("ps", ["-eo", "pid=,ppid="], { allowFailure: true });
  if (result.code === 0) {
    const processes = result.stdout.split(/\r?\n/).map((line) => line.trim().split(/\s+/).map(Number));
    const parents = new Set([pid]);
    let found = false;
    while (true) {
      const children = processes.filter(([child, parent]) => child && parent && parents.has(parent) && !parents.has(child));
      if (children.length === 0) break;
      found = true;
      for (const [child] of children) parents.add(child!);
    }
    if (found) return true;
  }
  try {
    process.kill(-pid, 0);
    return true;
  } catch (error) {
    return !(isErrnoException(error) && error.code === "ESRCH");
  }
}

interface PosixProcess {
  pid: number;
  sessionId: number;
  state: string;
}

async function posixProcesses(): Promise<PosixProcess[]> {
  const result = await run("ps", ["-eo", "pid=,sid=,stat="], { allowFailure: true });
  if (result.code !== 0) return [];
  return result.stdout.split(/\r?\n/).map((line) => line.trim().split(/\s+/))
    .filter((entry) => entry.length === 3 && entry.slice(0, 2).map(Number).every(Number.isSafeInteger))
    .map(([pid, sessionId, state]) => ({
      pid: Number(pid),
      sessionId: Number(sessionId),
      state: state!,
    }));
}

export async function requireChildProcessSession(
  pid: number,
  marker: string,
): Promise<{ sessionId: number; sessionStartedAt: string }> {
  for (let attempt = 0; attempt < 40; attempt += 1) {
    try {
      const [rawSessionId, ...identity] = (await fs.readFile(marker, "utf8")).trim().split(/\r?\n/);
      const sessionId = Number(rawSessionId?.trim());
      const sessionStartedAt = identity.join(" ").trim().replace(/\s+/g, " ");
      if (Number.isSafeInteger(sessionId) && sessionId > 0 && sessionStartedAt) {
        return { sessionId, sessionStartedAt };
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

async function terminateProcessSession(
  sessionId: number,
  sessionStartedAt: string,
  force: boolean,
): Promise<boolean> {
  const leaderIdentity = await processIdentity(sessionId);
  if (leaderIdentity && leaderIdentity !== sessionStartedAt) {
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

export async function trackedProcessExists(record: TrackedProcessRecord): Promise<boolean> {
  const identity = await processIdentity(record.pid);
  if (identity) return identity === record.startedAt;
  if (record.sessionId !== undefined) return processSessionExists(record.sessionId);
  return processDescendantsExist(record.groupId ?? record.pid);
}

export async function terminateTrackedProcess(record: TrackedProcessRecord, force = false): Promise<boolean> {
  if (record.sessionId !== undefined && record.sessionStartedAt !== undefined) {
    return terminateProcessSession(record.sessionId, record.sessionStartedAt, force);
  }
  const identity = await processIdentity(record.pid);
  if (identity === record.startedAt) {
    return killProcessTree(record.groupId ?? record.pid, force, identity);
  }
  if (identity) return false;
  if (process.platform !== "win32" && record.groupId !== undefined) {
    try {
      process.kill(-record.groupId, force ? "SIGKILL" : "SIGTERM");
      return true;
    } catch (error) {
      if (isMissingProcess(error)) return false;
      throw error;
    }
  }
  if (await processDescendantsExist(record.pid)) throw new ProcessIdentityUnavailableError(record.pid);
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
  const identity = await processIdentity(pid);
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
