import fs from "node:fs/promises";
import path from "node:path";
import { currentBranch } from "./git.js";
import { DependencyPreparationError } from "./errors.js";
import { dependencyFingerprint } from "./fingerprint.js";
import { withDirectoryLock } from "./lock.js";
import { run } from "./process.js";
import { readState, recordPreparationMetric, setTreeState, storePaths, treeKey, treeLockPath } from "./state.js";
import type { DependencyReporter, PackageManager, Repository } from "./types.js";

async function exists(file: string): Promise<boolean> {
  try {
    await fs.access(file);
    return true;
  } catch {
    return false;
  }
}

const MINIMUM_BACKEND_VERSIONS: Readonly<Record<"bun" | "pnpm", readonly [number, number, number]>> = {
  bun: [1, 3, 14],
  pnpm: [10, 12, 1],
};

function numericVersion(value: string): [number, number, number] | null {
  const match = String(value).match(/(?:^|\s|v)(\d+)\.(\d+)\.(\d+)/);
  return match ? [Number(match[1]), Number(match[2]), Number(match[3])] : null;
}

function compareVersions(
  left: readonly [number, number, number],
  right: readonly [number, number, number],
): number {
  for (let index = 0; index < 3; index += 1) {
    const difference = left[index]! - right[index]!;
    if (difference !== 0) return difference;
  }
  return 0;
}

export function assertSharedBackendSupported({ name, version }: { name: string; version: string }): void {
  const minimum = name === "bun" || name === "pnpm" ? MINIMUM_BACKEND_VERSIONS[name] : undefined;
  if (!minimum) {
    throw new Error(`Ruk's shared dependency backend does not support ${name}`);
  }
  const current = numericVersion(version);
  if (!current || compareVersions(current, minimum) < 0) {
    throw new Error(
      `${name} ${minimum.join(".")} or newer is required for Ruk's shared dependency backend (found ${version})`,
    );
  }
}

async function projectionPaths(root: string, dependencyFiles: readonly string[]): Promise<string[]> {
  const candidates = new Set(["node_modules"]);
  for (const file of dependencyFiles) {
    if (file === "package.json" || file.endsWith("/package.json")) {
      candidates.add(path.posix.join(path.posix.dirname(file.replaceAll("\\", "/")), "node_modules"));
    }
  }
  const existing = [];
  for (const relative of candidates) {
    if (await exists(path.join(root, relative))) existing.push(relative);
  }
  return existing.sort();
}

export async function dependenciesPresent(
  root: string,
  projections: readonly string[] = ["node_modules"],
): Promise<boolean> {
  if (!Array.isArray(projections) || projections.length === 0) return false;
  const present = await Promise.all(projections.map((relative) => exists(path.join(root, relative))));
  return present.every(Boolean);
}

const DEFAULT_REPORTER: DependencyReporter = {
  write: (message) => process.stdout.write(message),
  stdio: "inherit",
};

async function installDependencies(
  root: string,
  manager: PackageManager,
  reporter: DependencyReporter,
): Promise<void> {
  const [command, ...configuredArgs] = manager.command;
  if (!command) throw new Error("Package manager command cannot be empty");
  const args = [...configuredArgs];
  const environment = { ...process.env };

  if (manager.dependencyMode === "shared" && manager.name === "bun") {
    environment["BUN_INSTALL_GLOBAL_STORE"] = "1";
    const linkerArgument = args.find((argument) => argument.startsWith("--linker="));
    const linkerIndex = args.indexOf("--linker");
    const configuredLinker = linkerArgument?.slice("--linker=".length) ?? (linkerIndex >= 0 ? args[linkerIndex + 1] : null);
    if (configuredLinker && configuredLinker !== "isolated") {
      throw new Error("Bun's global virtual store requires the isolated linker");
    }
    if (!configuredLinker) args.push("--linker", "isolated");
  } else if (manager.dependencyMode === "shared" && manager.name === "pnpm") {
    const globalStoreArgument = args.find(
      (argument) =>
        argument.startsWith("--config.enable-global-virtual-store=") ||
        argument.startsWith("--enable-global-virtual-store="),
    );
    const configuredGlobalStore = globalStoreArgument?.slice(globalStoreArgument.indexOf("=") + 1);
    if (configuredGlobalStore && configuredGlobalStore !== "true") {
      throw new Error("pnpm's shared dependency backend requires the global virtual store");
    }
    if (!configuredGlobalStore) args.push("--config.enable-global-virtual-store=true");
  } else if (manager.dependencyMode === "shared") {
    throw new Error(
      `The shared dependency backend does not support ${manager.name}; use dependencyMode \"managed\"`,
    );
  }

  reporter.write(`Installing dependencies with ${[command, ...args].join(" ")}...\n`);
  try {
    await run(command, args, { cwd: root, env: environment, stdio: reporter.stdio });
  } catch (error) {
    const message = error instanceof Error ? error.message : String(error);
    throw new DependencyPreparationError(`Dependency installation failed: ${message}`, { cause: error });
  }
}

export interface EnsureDependenciesInput {
  repository: Repository;
  manager: PackageManager;
  reporter?: DependencyReporter;
}

export interface EnsureDependenciesResult {
  fingerprint: string;
  mode: string;
  reused: boolean;
  alreadyAttached: boolean;
}

async function ensureDependenciesUnlocked({
  repository,
  manager,
  reporter = DEFAULT_REPORTER,
}: EnsureDependenciesInput): Promise<EnsureDependenciesResult> {
  const { root, commonDir } = repository;
  const paths = storePaths(commonDir);
  let details = await dependencyFingerprint({ root, manager });
  if (manager.dependencyMode === "shared") assertSharedBackendSupported(details.manager);
  let fingerprint = details.fingerprint;

  const state = await readState(paths);
  const currentTree = state.trees[treeKey(root)];
  if (
    currentTree?.fingerprint === fingerprint &&
    (await dependenciesPresent(root, currentTree.projections))
  ) {
    return {
      fingerprint,
      mode: currentTree.mode,
      reused: true,
      alreadyAttached: true,
    };
  }

  await installDependencies(root, manager, reporter);
  details = await dependencyFingerprint({ root, manager });
  fingerprint = details.fingerprint;
  const mode = manager.dependencyMode === "shared" ? `${manager.name}-global-store` : "managed-install";
  const projections = await projectionPaths(root, details.files);
  if (projections.length === 0) {
    throw new Error("Dependency installation completed without creating a node_modules projection");
  }
  await setTreeState(paths, root, {
    fingerprint,
    mode,
    projections,
    branch: await currentBranch(root),
    updatedAt: new Date().toISOString(),
  });
  return { fingerprint, mode, reused: false, alreadyAttached: false };
}

export async function ensureDependencies(value: EnsureDependenciesInput): Promise<EnsureDependenciesResult> {
  const paths = storePaths(value.repository.commonDir);
  const startedAt = Date.now();
  try {
    const result = await withDirectoryLock(treeLockPath(paths, value.repository.root), () =>
      ensureDependenciesUnlocked(value),
    );
    await recordPreparationMetric(paths, result.alreadyAttached ? "skipped" : "prepared", Date.now() - startedAt);
    return result;
  } catch (error) {
    try {
      await recordPreparationMetric(paths, "failed", Date.now() - startedAt);
    } catch {
      // Preserve the dependency failure; metrics must not hide it.
    }
    throw error;
  }
}
