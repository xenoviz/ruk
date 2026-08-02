import path from "node:path";
import { run } from "./process.js";
import type { Repository } from "./types.js";

export interface WorktreeRecord {
  path: string;
  branch: string;
  head: string;
}

export async function getRepository(cwd = process.cwd()): Promise<Repository> {
  const [rootResult, commonResult] = await Promise.all([
    run("git", ["rev-parse", "--show-toplevel"], { cwd }),
    run("git", ["rev-parse", "--path-format=absolute", "--git-common-dir"], { cwd }),
  ]);

  return {
    root: path.resolve(rootResult.stdout.trim()),
    commonDir: path.resolve(commonResult.stdout.trim()),
  };
}

export async function currentBranch(cwd: string): Promise<string> {
  const result = await run("git", ["branch", "--show-current"], { cwd });
  return result.stdout.trim() || "(detached)";
}

export async function localBranchExists(cwd: string, branch: string): Promise<boolean> {
  const result = await run(
    "git",
    ["show-ref", "--verify", "--quiet", `refs/heads/${branch}`],
    { cwd, allowFailure: true },
  );
  return result.code === 0;
}

export async function addWorktree({
  cwd,
  destination,
  branch,
  startPoint = "HEAD",
  detach = false,
  stdio = "inherit",
}: {
  cwd: string;
  destination: string;
  branch: string;
  startPoint?: string;
  detach?: boolean;
  stdio?: import("node:child_process").StdioOptions;
}): Promise<void> {
  const args = ["worktree", "add"];
  if (detach) {
    args.push("--detach", destination, startPoint);
  } else if (await localBranchExists(cwd, branch)) {
    args.push(destination, branch);
  } else {
    args.push("-b", branch, destination, startPoint);
  }
  await run("git", args, { cwd, stdio });
}

export async function removeWorktree({
  cwd,
  destination,
  force = false,
  stdio = "inherit",
}: {
  cwd: string;
  destination: string;
  force?: boolean;
  stdio?: import("node:child_process").StdioOptions;
}): Promise<void> {
  const args = ["worktree", "remove"];
  if (force) args.push("--force");
  args.push(destination);
  await run("git", args, { cwd, stdio });
}

export async function listWorktrees(cwd: string): Promise<WorktreeRecord[]> {
  const result = await run("git", ["worktree", "list", "--porcelain"], { cwd });
  const records: WorktreeRecord[] = [];
  let current: WorktreeRecord | null = null;

  for (const line of result.stdout.split(/\r?\n/)) {
    if (line.startsWith("worktree ")) {
      if (current) records.push(current);
      current = { path: path.resolve(line.slice(9)), branch: "(detached)", head: "" };
    } else if (current && line.startsWith("HEAD ")) {
      current.head = line.slice(5);
    } else if (current && line.startsWith("branch ")) {
      current.branch = line.slice(7).replace(/^refs\/heads\//, "");
    } else if (current && line === "detached") {
      current.branch = "(detached)";
    }
  }
  if (current) records.push(current);
  return records;
}

export async function dependencyFiles(cwd: string): Promise<string[]> {
  const result = await run(
    "git",
    ["ls-files", "-z", "--cached", "--others", "--exclude-standard"],
    { cwd },
  );

  const dependencyNames = new Set([
    "package.json",
    "bun.lock",
    "bun.lockb",
    "bunfig.toml",
    "package-lock.json",
    "pnpm-lock.yaml",
    "pnpm-workspace.yaml",
    "yarn.lock",
    ".npmrc",
    ".yarnrc.yml",
    ".rukrc.json",
  ]);

  return [...new Set(result.stdout.split("\0").filter(Boolean))]
    .filter((file) => {
      const normalized = file.replaceAll("\\", "/");
      const name = path.posix.basename(normalized);
      return (
        dependencyNames.has(name) ||
        normalized.startsWith("patches/") ||
        normalized.includes("/patches/")
      );
    })
    .sort();
}
