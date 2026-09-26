# Workspace benchmark

## Question

How long does it take to get a ready-to-run agent workspace in a popular
repository with Ruk, compared with a plain `git worktree add` followed by an
install?

## Method

`scripts/benchmark/workspaces.sh` runs these steps for each repository:

1. **Warm the package cache.** One `install --frozen-lockfile` runs in the
   primary checkout, so the package manager's global cache is warm, as on a
   developer machine. Network time is excluded from every measurement below.
2. **Baseline.** Three workspaces are created, each with
   `git worktree add` followed by the same `install --frozen-lockfile`
   command Ruk runs.
3. **Ruk, first acquire.** `ruk init` runs, then three `ruk acquire` commands
   each create and prepare a new workspace.
4. **Ruk, reuse.** The three assignments are released, then three more
   `ruk acquire` commands reuse the prepared pool slots.

After every step the script checks that the root manifest's first dependency
is installed in the workspace. All 45 checks passed. Times are medians of
three. Disk is the filesystem's used space before and after creating the
three workspaces, divided by three.

### Environment

- 4 vCPU, 15 GB RAM Linux cloud VM with a local ext4 disk.
- Node.js 22.22.2, Git 2.43.0, pnpm 12.6.0 (Svelte pins and uses 10.33.4),
  and Bun 1.4.2.
- Ruk 0.5.1, plus the same commit with the two fixes described below. Every
  repository ran in Ruk's shared dependency mode.
- Shallow clones: vuejs/core `4ab865a848`, vitejs/vite `bc598a6a8a`,
  sveltejs/svelte `f908535659`, honojs/hono `ee0622e144`, and
  elysiajs/elysia `e037eca710`.

## Results

| Repository | Package manager | Baseline | Ruk first acquire | Ruk reuse, 0.5.1 | Ruk reuse, fixed | Reuse vs baseline |
| ---------- | --------------- | -------: | ----------------: | ---------------: | ---------------: | ----------------: |
| honojs/hono | pnpm | 0.85 s | 1.29 s | 1.22 s | 0.53 s | 1.6× faster |
| vuejs/core | pnpm | 1.35 s | 1.70 s | 1.50 s | 0.38 s | 3.5× faster |
| elysiajs/elysia | Bun | 0.57 s | 0.73 s | 0.25 s | 0.18 s | 3.2× faster |
| sveltejs/svelte | pnpm (monorepo) | 3.27 s | 5.59 s | 2.74 s | 0.69 s | 4.7× faster |
| vitejs/vite | pnpm (monorepo) | 1.98 s | 3.33 s | 2.80 s | 0.70 s | 2.8× faster |

Ruk's counters confirm the change. With 0.5.1, every repository recorded
seven preparations and zero preparation skips. With the fixes, every
repository recorded four preparations (`init` plus three first acquires) and
three skips (every reuse).

Disk per workspace was about the same for both approaches: 29 MB (Hono),
34–41 MB (Vue), 45–50 MB (Elysia), and 68 MB (Svelte and Vite). pnpm already
hard-links package files from its store in both cases. Ruk's shared mode used
10% less on Elysia and 17% less on Vue.

## Findings

**Reuse never skipped installation in 0.5.1.** Two faults in the
dependency-projection integrity check made every released workspace look
modified, so release discarded its `node_modules` and the next acquisition
reinstalled:

1. The fingerprint hashed each file's change time while following links into
   the shared package store. pnpm and Bun hard-link one store file into every
   workspace that uses it, and each new link updates the file's change time.
   Any sibling workspace's install therefore invalidated every other
   workspace.
2. The fingerprint followed links from `node_modules` back into the
   repository's own packages, such as `node_modules/svelte → packages/svelte`.
   Source edits, and the ignored-file clean that release performs, then looked
   like dependency corruption.

Both are fixed. The fingerprint now uses mode, size, and modification time,
and it records links into workspace source by their target path without
hashing the source itself.

**Reuse is where Ruk wins.** With the fixes, taking a prepared workspace from
the pool is 1.6–4.7× faster than creating a new worktree and installing, and
the gap grows with the size of the dependency tree.

**A first acquire costs more than a plain install.** It is 1.3–1.7× slower
than the baseline. The extra time goes to lifecycle bookkeeping and to hashing
the dependency projection, which walks the package store. Warming the pool
ahead of an agent burst (`ruk warm --count <n>`) moves this cost off the
agent's critical path.

**An intermittent retained assignment.** In one of three runs, a reuse failed
with `RESOURCE_BUSY` because a short-lived `node --version` probe exited before
Ruk recorded its identity. Ruk then kept the workspace fenced, which is the
safe outcome. The failure did not recur in two later runs and is tracked
separately.

## Limits

- One machine, a warm package cache, and three samples per scenario.
- Shallow clones on local disk. Network filesystems and cold caches behave
  differently.
- Reuse helps only while the lockfile and package-manager inputs are
  unchanged. Otherwise Ruk correctly reinstalls.

## Reproducing

```sh
go build -o /tmp/ruk ./cmd/ruk
RUK=/tmp/ruk scripts/benchmark/workspaces.sh /tmp/ruk-bench \
  vuejs/core vitejs/vite sveltejs/svelte honojs/hono elysiajs/elysia
```

pnpm 10.12.1 or newer and Bun 1.3.14 or newer must be on `PATH` for Ruk's
shared mode. Results are written to `/tmp/ruk-bench/results.jsonl`.
