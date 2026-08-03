import { spawn } from "node:child_process";
import type { StdioOptions } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";

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
