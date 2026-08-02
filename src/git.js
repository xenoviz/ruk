import path from "node:path";
import { run } from "./process.js";

export async function getRepository(cwd = process.cwd()) {
  const [rootResult, commonResult] = await Promise.all([
    run("git", ["rev-parse", "--show-toplevel"], { cwd }),
    run("git", ["rev-parse", "--path-format=absolute", "--git-common-dir"], { cwd }),
  ]);

  return {
    root: path.resolve(rootResult.stdout.trim()),
    commonDir: path.resolve(commonResult.stdout.trim()),
  };
}

export async function currentBranch(cwd) {
  const result = await run("git", ["branch", "--show-current"], { cwd });
  return result.stdout.trim() || "(detached)";
}

export async function localBranchExists(cwd, branch) {
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
}) {
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

export async function removeWorktree({ cwd, destination, force = false, stdio = "inherit" }) {
  const args = ["worktree", "remove"];
  if (force) args.push("--force");
  args.push(destination);
  await run("git", args, { cwd, stdio });
}

export async function listWorktrees(cwd) {
  const result = await run("git", ["worktree", "list", "--porcelain"], { cwd });
  const records = [];
  let current = null;

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

export async function dependencyFiles(cwd) {
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
