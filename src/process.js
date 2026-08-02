import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import path from "node:path";

export function run(command, args = [], options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env ?? process.env,
      stdio: options.stdio ?? ["ignore", "pipe", "pipe"],
      shell: false,
      windowsHide: true,
    });

    let stdout = "";
    let stderr = "";

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
    child.on("close", (code, signal) => {
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

export async function commandExists(command) {
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
