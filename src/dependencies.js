import fs from "node:fs/promises";
import path from "node:path";
import { currentBranch } from "./git.js";
import { dependencyFingerprint } from "./fingerprint.js";
import { withDirectoryLock } from "./lock.js";
import { run } from "./process.js";
import { readState, setTreeState, storePaths, treeKey, treeLockPath } from "./state.js";

async function exists(file) {
  try {
    await fs.access(file);
    return true;
  } catch {
    return false;
  }
}

const MINIMUM_BACKEND_VERSIONS = {
  bun: [1, 3, 14],
  pnpm: [10, 12, 1],
};

function numericVersion(value) {
  const match = String(value).match(/(?:^|\s|v)(\d+)\.(\d+)\.(\d+)/);
  return match ? match.slice(1, 4).map(Number) : null;
}

function compareVersions(left, right) {
  for (let index = 0; index < 3; index += 1) {
    if (left[index] !== right[index]) return left[index] - right[index];
  }
  return 0;
}

export function assertSharedBackendSupported({ name, version }) {
  const minimum = MINIMUM_BACKEND_VERSIONS[name];
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

async function projectionPaths(root, dependencyFiles) {
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

export async function dependenciesPresent(root, projections = ["node_modules"]) {
  if (!Array.isArray(projections) || projections.length === 0) return false;
  const present = await Promise.all(projections.map((relative) => exists(path.join(root, relative))));
  return present.every(Boolean);
}

const DEFAULT_REPORTER = {
  write: (message) => process.stdout.write(message),
  stdio: "inherit",
};

async function installDependencies(root, manager, reporter) {
  const [command, ...configuredArgs] = manager.command;
  const args = [...configuredArgs];
  const environment = { ...process.env };

  if (manager.dependencyMode === "shared" && manager.name === "bun") {
    environment.BUN_INSTALL_GLOBAL_STORE = "1";
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
  await run(command, args, { cwd: root, env: environment, stdio: reporter.stdio });
}

async function ensureDependenciesUnlocked({ repository, manager, reporter = DEFAULT_REPORTER }) {
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

export async function ensureDependencies(value) {
  const paths = storePaths(value.repository.commonDir);
  return withDirectoryLock(treeLockPath(paths, value.repository.root), () =>
    ensureDependenciesUnlocked(value),
  );
}
