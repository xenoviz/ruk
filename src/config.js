import fs from "node:fs/promises";
import path from "node:path";
import { commandExists, run } from "./process.js";

const DEPENDENCY_MODES = new Set(["managed", "shared"]);

async function readJson(file) {
  try {
    return JSON.parse(await fs.readFile(file, "utf8"));
  } catch (error) {
    if (error?.code === "ENOENT") return null;
    throw new Error(`Cannot read ${file}: ${error.message}`);
  }
}

function validateCommand(value, source) {
  if (value == null) return null;
  if (!Array.isArray(value) || value.length === 0 || value.some((part) => typeof part !== "string" || !part)) {
    throw new Error(`${source} must be a non-empty string array`);
  }
  return [...value];
}

function parseEnvironmentCommand(value) {
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

function dependencyMode(value, source) {
  const mode = value ?? "managed";
  if (!DEPENDENCY_MODES.has(mode)) {
    throw new Error(`${source} must be either \"managed\" or \"shared\"`);
  }
  return mode;
}

export async function loadConfig(root) {
  const fileConfig = (await readJson(path.join(root, ".rukrc.json"))) ?? {};
  if (typeof fileConfig !== "object" || Array.isArray(fileConfig)) {
    throw new Error(".rukrc.json must contain a JSON object");
  }
  const unknown = Object.keys(fileConfig).filter(
    (key) => key !== "installCommand" && key !== "dependencyMode",
  );
  if (unknown.length > 0) {
    throw new Error(`Unknown .rukrc.json option${unknown.length === 1 ? "" : "s"}: ${unknown.join(", ")}`);
  }
  const environmentCommand = parseEnvironmentCommand(process.env.RUK_INSTALL_COMMAND);
  return {
    installCommand:
      environmentCommand ?? validateCommand(fileConfig.installCommand, ".rukrc.json installCommand"),
    dependencyMode: dependencyMode(
      process.env.RUK_DEPENDENCY_MODE ?? fileConfig.dependencyMode,
      process.env.RUK_DEPENDENCY_MODE ? "RUK_DEPENDENCY_MODE" : ".rukrc.json dependencyMode",
    ),
  };
}

async function packageManagerFromPackageJson(root) {
  const pkg = await readJson(path.join(root, "package.json"));
  const value = pkg?.packageManager;
  if (typeof value !== "string") return null;
  const separator = value.lastIndexOf("@");
  return separator > 0 ? value.slice(0, separator) : value;
}

async function firstExisting(root, names) {
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

export async function detectPackageManager(root, config) {
  if (config.installCommand) {
    return {
      name: path.basename(config.installCommand[0]).replace(/\.exe$/i, ""),
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
    command = [name, "install", "--immutable"];
  } else {
    command = [name, (await firstExisting(root, ["package-lock.json"])) ? "ci" : "install"];
  }
  return { name, command, dependencyMode: config.dependencyMode };
}

export async function packageManagerVersion(manager, root) {
  const result = await run(manager.command[0], ["--version"], {
    cwd: root,
    allowFailure: true,
  });
  return result.code === 0 ? result.stdout.trim() : "unknown";
}
