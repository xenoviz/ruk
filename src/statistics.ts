import fs from "node:fs/promises";
import type { Stats } from "node:fs";
import path from "node:path";
import { treeKey } from "./state.js";
import type { RukState } from "./types.js";

export interface DiskStatistics {
  projectionBytes: number;
  linkedTargetBytes: number;
  estimatedBytesAvoided: number;
}

async function entrySize(entry: string, visited: Set<string>): Promise<number> {
  let real: string;
  try {
    real = await fs.realpath(entry);
  } catch {
    return 0;
  }
  if (visited.has(real)) return 0;
  visited.add(real);
  const stat = await fs.stat(real);
  if (!stat.isDirectory()) return stat.size;
  const entries = await fs.readdir(real);
  const sizes = await Promise.all(entries.map((name) => entrySize(path.join(real, name), visited)));
  return sizes.reduce((total, size) => total + size, 0);
}

async function scanProjection(
  directory: string,
  targets: Map<string, { size: number; references: number }>,
): Promise<number> {
  let entries: string[];
  try {
    entries = await fs.readdir(directory);
  } catch {
    return 0;
  }
  let localBytes = 0;
  for (const name of entries) {
    const entry = path.join(directory, name);
    let stat: Stats;
    try {
      stat = await fs.lstat(entry);
    } catch {
      continue;
    }
    if (stat.isSymbolicLink()) {
      let real: string;
      try {
        real = await fs.realpath(entry);
      } catch {
        continue;
      }
      const target = targets.get(real);
      if (target) target.references += 1;
      else targets.set(real, { size: await entrySize(real, new Set()), references: 1 });
    } else if (stat.isDirectory()) {
      localBytes += await scanProjection(entry, targets);
    } else {
      localBytes += stat.size;
    }
  }
  return localBytes;
}

export async function diskStatistics(state: RukState): Promise<DiskStatistics> {
  const targets = new Map<string, { size: number; references: number }>();
  let projectionBytes = 0;
  for (const workspace of Object.values(state.workspaces)) {
    const tree = state.trees[treeKey(workspace.path)];
    for (const projection of tree?.projections ?? ["node_modules"]) {
      projectionBytes += await scanProjection(path.join(workspace.path, projection), targets);
    }
  }
  const linkedTargetBytes = [...targets.values()].reduce((total, target) => total + target.size, 0);
  const estimatedBytesAvoided = [...targets.values()].reduce(
    (total, target) => total + target.size * Math.max(0, target.references - 1),
    0,
  );
  return { projectionBytes, linkedTargetBytes, estimatedBytesAvoided };
}

export function usageStatistics(state: RukState) {
  const { metrics } = state;
  const attempts = metrics.preparations + metrics.preparationSkips + metrics.preparationFailures;
  return {
    ...metrics,
    averagePreparationMs: metrics.preparations === 0 ? 0 : Math.round(metrics.totalPreparationMs / metrics.preparations),
    reuseRate: metrics.acquisitions === 0 ? 0 : metrics.workspaceReuses / metrics.acquisitions,
    preparationHitRate: attempts === 0 ? 0 : metrics.preparationSkips / attempts,
    activeAssignments: Object.values(state.workspaces).filter((workspace) => workspace.lifecycle === "assigned").length,
    availableWorkspaces: Object.values(state.workspaces).filter((workspace) => workspace.lifecycle === "available").length,
    failedWorkspaces: Object.values(state.workspaces).filter((workspace) => workspace.lifecycle === "failed").length,
    reservedPorts: Object.values(state.workspaces).reduce(
      (total, workspace) => total + Object.keys(workspace.assignment?.ports ?? {}).length,
      0,
    ),
  };
}
