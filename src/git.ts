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

export async function worktreeIsClean(cwd: string): Promise<boolean> {
  const result = await run("git", ["status", "--porcelain"], { cwd });
  return result.stdout.length === 0;
}

export async function assignWorktree({
  repository,
  workspace,
  branch,
  startPoint = "HEAD",
}: {
  repository: string;
  workspace: string;
  branch: string;
  startPoint?: string;
}): Promise<void> {
  if (await localBranchExists(repository, branch)) {
    await run("git", ["switch", branch], { cwd: workspace });
  } else {
    await run("git", ["switch", "-c", branch, startPoint], { cwd: workspace });
  }
}

export async function returnWorktree(cwd: string, force = false): Promise<void> {
  if (!force && !(await worktreeIsClean(cwd))) {
    throw new Error("Workspace has uncommitted changes. Commit them or retry release with --force.");
  }
  if (force) {
    await run("git", ["reset", "--hard", "HEAD"], { cwd });
  }
  await run("git", ["clean", "-ffdx"], { cwd });
  await run("git", ["switch", "--detach"], { cwd });
}

export async function lockWorktree(cwd: string, destination: string): Promise<void> {
  await run("git", ["worktree", "lock", "--reason", "ruk pool", destination], { cwd });
}

export async function unlockWorktree(cwd: string, destination: string): Promise<void> {
  const result = await run("git", ["worktree", "unlock", destination], { cwd, allowFailure: true });
  if (result.code !== 0 && !/not locked/i.test(`${result.stderr}\n${result.stdout}`)) {
    throw new Error(`Could not unlock worktree ${destination}: ${result.stderr.trim() || result.stdout.trim()}`);
  }
}

export async function listWorktrees(cwd: string): Promise<WorktreeRecord[]> {
  const result = await run("git", ["worktree", "list", "--porcelain", "-z"], { cwd });
  const records: WorktreeRecord[] = [];
  let current: WorktreeRecord | null = null;

  for (const field of result.stdout.split("\0")) {
    if (field.startsWith("worktree ")) {
      if (current) records.push(current);
      current = { path: path.resolve(field.slice(9)), branch: "(detached)", head: "" };
    } else if (current && field.startsWith("HEAD ")) {
      current.head = field.slice(5);
    } else if (current && field.startsWith("branch ")) {
      current.branch = field.slice(7).replace(/^refs\/heads\//, "");
    } else if (current && field === "detached") {
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
