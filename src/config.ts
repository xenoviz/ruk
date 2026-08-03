import fs from "node:fs/promises";
import path from "node:path";
import { commandExists, run } from "./process.js";
import type { DependencyMode, PackageManager, RukConfig } from "./types.js";
import { isErrnoException, isRecord } from "./types.js";

const DEPENDENCY_MODES = new Set<DependencyMode>(["managed", "shared"]);

async function readJson(file: string): Promise<unknown | null> {
  try {
    return JSON.parse(await fs.readFile(file, "utf8")) as unknown;
  } catch (error) {
    if (isErrnoException(error) && error.code === "ENOENT") return null;
    const message = error instanceof Error ? error.message : String(error);
    throw new Error(`Cannot read ${file}: ${message}`);
  }
}

function validateCommand(value: unknown, source: string): string[] | null {
  if (value == null) return null;
  if (!Array.isArray(value) || value.length === 0 || value.some((part) => typeof part !== "string" || !part)) {
    throw new Error(`${source} must be a non-empty string array`);
  }
  return [...value];
}

function parseEnvironmentCommand(value: string | undefined): string[] | null {
  if (!value) return null;
  try {
    return validateCommand(JSON.parse(value), "RUK_INSTALL_COMMAND");
  } catch (error) {
    if (error instanceof SyntaxError) {
      throw new Error("RUK_INSTALL_COMMAND must be a JSON string array");
    }
    throw error;
  }
}

function dependencyMode(value: unknown, source: string): DependencyMode {
  const mode = value ?? "managed";
  if (typeof mode !== "string" || !DEPENDENCY_MODES.has(mode as DependencyMode)) {
    throw new Error(`${source} must be either \"managed\" or \"shared\"`);
  }
  return mode as DependencyMode;
}

export async function loadConfig(root: string): Promise<RukConfig> {
  const fileConfig = (await readJson(path.join(root, ".rukrc.json"))) ?? {};
  if (!isRecord(fileConfig)) {
    throw new Error(".rukrc.json must contain a JSON object");
  }
  const unknown = Object.keys(fileConfig).filter(
    (key) => key !== "installCommand" && key !== "dependencyMode",
  );
  if (unknown.length > 0) {
    throw new Error(`Unknown .rukrc.json option${unknown.length === 1 ? "" : "s"}: ${unknown.join(", ")}`);
  }
  const environmentCommand = parseEnvironmentCommand(process.env["RUK_INSTALL_COMMAND"]);
  return {
    installCommand:
      environmentCommand ?? validateCommand(fileConfig["installCommand"], ".rukrc.json installCommand"),
    dependencyMode: dependencyMode(
      process.env["RUK_DEPENDENCY_MODE"] ?? fileConfig["dependencyMode"],
      process.env["RUK_DEPENDENCY_MODE"] ? "RUK_DEPENDENCY_MODE" : ".rukrc.json dependencyMode",
    ),
  };
}

async function packageManagerFromPackageJson(root: string): Promise<string | null> {
  const pkg = await readJson(path.join(root, "package.json"));
  const value = isRecord(pkg) ? pkg["packageManager"] : null;
  if (typeof value !== "string") return null;
  const separator = value.lastIndexOf("@");
  return separator > 0 ? value.slice(0, separator) : value;
}

async function firstExisting(root: string, names: readonly string[]): Promise<string | null> {
  for (const name of names) {
    try {
      await fs.access(path.join(root, name));
      return name;
    } catch {
      // Continue.
    }
  }
  return null;
}

export async function detectPackageManager(root: string, config: RukConfig): Promise<PackageManager> {
  if (config.installCommand) {
    const executable = config.installCommand[0];
    if (!executable) throw new Error("installCommand cannot be empty");
    return {
      name: path.basename(executable).replace(/\.exe$/i, ""),
      command: config.installCommand,
      dependencyMode: config.dependencyMode,
    };
  }

  let name = await packageManagerFromPackageJson(root);
  if (!name) {
    const lockfile = await firstExisting(root, ["bun.lock", "bun.lockb", "pnpm-lock.yaml", "yarn.lock", "package-lock.json"]);
    name = lockfile?.startsWith("bun.") ? "bun" : lockfile === "pnpm-lock.yaml" ? "pnpm" : lockfile === "yarn.lock" ? "yarn" : "npm";
  }

  if (!(await commandExists(name))) {
    throw new Error(`${name} is required but was not found on PATH`);
  }

  let command;
  if (name === "bun" || name === "pnpm") {
    command = [name, "install", "--frozen-lockfile"];
  } else if (name === "yarn") {
    command = [name, "install", "--frozen-lockfile"];
  } else {
    command = [name, (await firstExisting(root, ["package-lock.json"])) ? "ci" : "install"];
  }
  return { name, command, dependencyMode: config.dependencyMode };
}

export async function packageManagerVersion(manager: PackageManager, root: string): Promise<string> {
  const executable = manager.command[0];
  if (!executable) throw new Error("Package manager command cannot be empty");
  const result = await run(executable, ["--version"], {
    cwd: root,
    allowFailure: true,
  });
  return result.code === 0 ? result.stdout.trim() : "unknown";
}
