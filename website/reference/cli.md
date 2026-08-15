# CLI reference

## Prepare workspaces

### `ruk init [--json]`

Prepare dependencies in the current worktree. `init` and `sync` have the same
dependency behavior, but `init` remains available when the primary checkout is
acting as a control location.

### `ruk sync [--allow-shared-checkout] [--json]`

Recompute the dependency fingerprint and prepare the current worktree when its
record or local dependency projection is stale.
Inside an assignment, sync renews the lease while preparation remains active.
In a shared primary checkout it follows `sharedCheckoutPolicy`.

### `ruk create <branch>`

```text
ruk create <branch> [--path <directory>] [--from <ref>] [--fetch] [--detach] [--json]
```

Create and prepare an ordinary Git worktree. This command does not create an
assignment or add the worktree to Ruk's reusable pool.

## Manage assignments

### `ruk acquire <branch>`

```text
ruk acquire <branch> [--from <ref>] [--fetch] [--ttl <minutes>] [--owner <id>] [--port <name>...] [--json]
```

Assign an available managed workspace or create one. The default TTL is 480
minutes. `--fetch` refreshes the remote used by `--from` before assignment.
Without `--from`, it resolves the primary remote's advertised default branch.
Repeated `--port` options reserve named cooperative host-local ports.
The TTL is the idle window; managed operations renew it while active.

### `ruk renew <assignment-id>`

```text
ruk renew <assignment-id> [--ttl <minutes>] [--json]
```

Extend the exact active assignment. The default renewal TTL is 480 minutes.

### `ruk release <assignment-id>`

```text
ruk release <assignment-id> [--force] [--json]
```

Return the assigned workspace to the pool. `--force` can kill surviving
recorded processes and discard worktree changes.

## Run and inspect

### `ruk run [--allow-shared-checkout] -- <command>`

Ensure dependencies are ready, launch the command, and return its exit code.
Inside an assigned workspace, Ruk records the child process for release.
It also renews the assignment while the command is active. In a shared primary
checkout it follows `sharedCheckoutPolicy`.
The separator keeps command boundaries unambiguous.

### `ruk exec <branch> -- <command>`

Acquire a workspace, run one command, and release it when cleanup is safe.
Acquire options include `--from`, `--fetch`, `--ttl`, `--owner`, and repeated
`--port`. A dirty tree or cleanup failure retains the assignment and prints its
recovery ID.

### `ruk shell <branch>`

Open a terminal-attached interactive assigned shell. Set `RUK_SHELL` to override
the platform default. Ruk inherits the terminal and tracks descendants through
a native POSIX process group or Windows job boundary without launching a helper
shell. This adapter does not allocate a PTY or ConPTY. Ruk releases a clean
workspace on shell exit and retains a dirty one.

### `ruk warm --count <n>`

```text
ruk warm --count <n> [--from <ref>] [--fetch] [--json]
```

Ensure that the pool contains the requested number of available prepared
workspaces. The count is a target, so an already-warm pool creates nothing.
Capacity checks are serialized with acquisition.

### `ruk status [--explain] [--json]`

Report dependency readiness and lifecycle state for the current worktree.
Readiness reasons distinguish an unprepared workspace, missing dependency
projection, and changed fingerprint. JSON output also reports observed activity,
automatic renewal, primary-checkout identity, management state, and the active
assignment count.

### `ruk list [--json]`

List Git worktrees with Ruk preparation, assignment, activity, and
primary-checkout information.

### `ruk stats [--disk] [--json]`

Report recorded acquisitions, reuse, preparation hits, failures, and timings.
`--disk` performs an on-demand scan and labels deduplication savings as an
estimate.

## Remove and collect

### `ruk remove <path> [--force]`

Remove an ordinary worktree. Ruk refuses to remove the current workspace or a
workspace owned by the managed pool.

### `ruk gc`

```text
ruk gc [--max-age <minutes>] [--apply] [--force-expired] [--json]
```

Preview or apply collection of old managed workspaces. The default maximum age
is 1,440 minutes. `--force-expired` requires `--apply`.

## Update

### `ruk update [--check] [--json]`

Check for or install a completed release. Stable installations select stable
versions. An installation whose current version contains a prerelease
identifier follows newer prereleases on that channel. Package installations
delegate the exact version to the owning npm, Bun, pnpm, or Yarn installation;
standalone binaries verify and replace their native executable. Ordinary
commands never contact GitHub for updates.

## Global output behavior

`--json` applies only where shown. Successful JSON commands write one value to
stdout and diagnostics to stderr. Failures exit nonzero and write one JSON
error with a stable `code` and `retryable` field to stderr.
