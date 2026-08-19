import { spawn } from "node:child_process";
import type { StdioOptions } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";

export interface RunOptions {
  cwd?: string;
  env?: NodeJS.ProcessEnv;
  stdio?: StdioOptions;
  allowFailure?: boolean;
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
  return key === undefined ? undefined : environment[key];
}

async function windowsCommandShim(command: string, cwd: string, environment: NodeJS.ProcessEnv): Promise<string | null> {
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
        // Keep searching PATH.
      }
    }
  }
  return null;
}

function quoteWindowsArgument(value: string): string {
  if (/^[^\s"&|<>^()]+$/.test(value)) return value;
  return `"${value.replace(/(\\*)"/g, "$1$1\\\"").replace(/(\\+)$/g, "$1$1")}"`;
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
    ? environmentValue(environment, "COMSPEC") ?? path.join(environmentValue(environment, "SYSTEMROOT") ?? "C:\\Windows", "System32", "cmd.exe")
    : command;
  const spawnArgs = shim
    ? ["/d", "/s", "/c", `${quoteWindowsArgument(shim)} ${args.map(quoteWindowsArgument).join(" ")}`]
    : args;
  return await new Promise<RunResult>((resolve, reject) => {
    const child = spawn(executable, spawnArgs, {
      cwd,
      env: environment,
      stdio: options.stdio ?? ["ignore", "pipe", "pipe"],
      shell: false,
      windowsHide: true,
    });
    let stdout = "";
    let stderr = "";
    if (child.stdout) {
      child.stdout.setEncoding("utf8");
      child.stdout.on("data", (chunk: string) => { stdout += chunk; });
    }
    if (child.stderr) {
      child.stderr.setEncoding("utf8");
      child.stderr.on("data", (chunk: string) => { stderr += chunk; });
    }
    child.once("error", reject);
    child.once("close", (code, signal) => {
      const result: RunResult = {
        code: code ?? (signal === null ? 1 : 128 + (os.constants.signals[signal] ?? 0)),
        signal,
        stdout,
        stderr,
      };
      if (result.code === 0 || options.allowFailure) {
        resolve(result);
        return;
      }
      const detail = stderr.trim() || stdout.trim();
      reject(new Error(`${command} ${args.join(" ")} failed with exit code ${result.code}${detail ? `: ${detail}` : ""}`));
    });
  });
}
