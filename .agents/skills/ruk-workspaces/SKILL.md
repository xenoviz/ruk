---
name: ruk-workspaces
description: Manage Ruk workspaces for coding agents. Use when an agent must acquire, warm, run in, inspect, renew, release, or measure a dependency-ready Git workspace.
---

# Ruk Workspaces

Ruk 0.3 runs as one dependency-free native Go binary. The npm distribution
installs the matching platform package, while standalone downloads provide the
same command without Node.js or Bun. Package-manager runtimes may still be
needed to install or update the npm distribution; they are not Ruk's command
supervisor. On Windows, routine process liveness uses native APIs and does not
launch PowerShell.

## Workflow

1. Run `ruk acquire <branch> --owner <stable-agent-id> --json` from the source
   repository. Add `--from`, `--fetch`, `--ttl`, or named `--port` requests only
   when needed. Fetch is explicit because it performs network I/O; without
   `--from`, it uses the primary remote's advertised default branch. A fully
   qualified remote-tracking ref fails if its named remote is missing.
2. Parse and retain `path`, `assignmentId`, `expiresAt`, and `ports`. Treat the
   assignment ID as opaque; never derive it from the path.
3. Set the working directory to the returned path. Use `ruk run -- <command>`
   for agent processes and `ruk sync --json` after dependency inputs change.
4. Inspect with `ruk status --json`. Managed `run`, `exec`, `shell`, and
   assigned `sync` operations renew automatically while active. Use
   `ruk renew <assignmentId> --json` for long idle work outside those commands.
5. Finish with `ruk release <assignmentId> --json`, even when the workspace was
   reused. Report a release failure instead of substituting another ID.

For a short clean job, use `ruk exec <branch> -- <command>` to acquire, run,
and release automatically. If the command leaves changes or cleanup fails, Ruk
retains the assignment and prints the exact recovery ID. It also retains the
assignment when child identity cannot be established while descendants remain;
leaderless POSIX process groups retain the assignment rather than being signaled;
failed registration or heartbeat abort also retains an unverified detached
group instead of signaling a reusable numeric process-group ID. Any cleanup
refusal is final for that attempt; Ruk does not follow it with an unfenced PID signal.
Attached POSIX leaders and descendants are identity-fenced individually
immediately before they are signaled; PID reuse or an unreadable identity for a
still-live process retains the assignment instead of killing or overlooking it.
The workspace tree lock remains held until process registration succeeds or
failed-registration cleanup settles, so release cannot race that handoff.
If a reused acquisition fails after ownership is published, parse its exact
`assignmentId`, `path`, `expiresAt`, and `recovery`; Ruk keeps that slot assigned.
The original machine-readable category is preserved for dependency and port
failures, while otherwise unclassified retained failures use retryable
`RESOURCE_BUSY`.
The reported expiry is reloaded from retained state after keeper cleanup, so it
includes any heartbeat renewal completed during the failed acquisition.
The retained transition clears the incomplete handoff marker, so the returned
exact-ID release command can be used once the caller decides recovery is safe.
On Windows it terminates the new tree only with a verified leader identity and
otherwise retains the assignment while descendants remain or the leader PID is reused.
Ruk fences the assignment again immediately before launching a command.
Assigned `ruk sync` also rechecks the exact assignment from inside the dependency
lock before reading or changing projections, so a queued repair cannot cross a release.
Managed detached `run` and `exec` commands forward `SIGINT` and `SIGTERM` to
their POSIX process group and preserve conventional 130/143 exit codes. Ordinary
release is rejected until acquisition handoff finishes.
Inspect that process tree before forcing release. Use `ruk shell <branch>` for
an interactive, terminal-attached assigned shell. Ruk inherits the terminal and
uses a native POSIX process group or Windows job boundary to track descendants
after the shell leader exits. It does not launch `script`, PowerShell, or another
shell helper, and it does not claim to allocate a PTY or ConPTY. A leaderless or
unverifiable process record fails closed rather than signaling a reusable
numeric ID. Non-interactive shell input keeps EOF observable and forwards
interrupts to its tracked process group. Commit intended work before exit so
normal release can succeed. Release integrity-validates recorded dependency
projections inside the same workspace lock used by assigned synchronization:
unchanged projections stay warm, while modified projections are discarded and
rebuilt from the package store before the next assigned command. Warm capacity
counts only projections whose dependency inputs and integrity fingerprint still
validate, including linked package targets. `ruk status --json` reports
`projection-changed` and recommends `ruk sync` when integrity validation fails.
Status and list JSON also expose `lastActivityAt`, derived `autoRenewing`,
`primaryCheckout`, `managed`, and `activeAssignments`.
Windows state replacement receives bounded retries for transient file-sharing
violations. A persistent heartbeat renewal failure is a retryable
`RESOURCE_BUSY` error and stops the tracked command.

Automatic renewal is activity-based, not filesystem-based. Ruk records activity
for fenced operations it owns; edits made directly by an editor, build tool, or
another terminal do not renew the lease. Renew explicitly before the idle
window expires when work continues outside `run`, `exec`, `shell`, or assigned
`sync`. A current keeper can keep an expired timestamp from being collected,
but expiry alone never transfers ownership.

Treat the repository's primary checkout as a control location. When active
assignments exist, `ruk run` and `ruk sync` deny task work there by default.
Acquire a dedicated workspace. Use `--allow-shared-checkout` only for a single
intentional command, or follow the repository's `sharedCheckoutPolicy` when it
is explicitly set to `warn` or `allow`. The guard coordinates Ruk commands but
cannot block direct Git or filesystem writes. In default `deny` mode, Ruk
serializes primary-checkout task execution with assignment publication, so an
acquisition cannot appear after the task's safety check and race its work.

Use `ruk warm --count <n> --json` before a known burst of agents. The count is
the desired number of available prepared workspaces, not the number to add;
acquisition and warm-capacity reservations are serialized.
Use `ruk stats --json` for recorded reuse and preparation metrics; add `--disk`
only when an on-demand filesystem scan is acceptable. Available capacity omits
workspaces fenced by collection, both in statistics and warm-pool counts.

Named ports are cooperative host-local reservations. `--port app` returns an
`app` field and makes `RUK_PORT_APP` available to `ruk run`, `exec`, and
`shell`. The stable per-user registry is owner-only and fails closed on unsafe
or corrupt state, regardless of per-process temporary-directory settings. The
native runtime imports active Ruk 0.2 host reservations under the legacy lock,
so upgrading cannot make an in-use port appear free. Ruk does not hold the
socket against unrelated processes.
Allocation probes dual-stack IPv6 when the host supports it and falls back to IPv4.

Use `ruk gc --json` to preview collection. Apply only when requested with
`ruk gc --apply --json`. Add `--force-expired` only with explicit authority to
reclaim expired assignments; expiry alone does not make them safe to remove.
Forced collection revalidates expiry atomically before changing lifecycle state.
An expired timestamp is not collectible while a current fenced lease keeper
still reports `autoRenewing: true`; GC skips active managed work.
GC recovers interrupted preparations, pre-handoff acquisitions, and collections
after the age cutoff; it revalidates handoff state under the acquisition lock
and preserves recovery markers after failed cleanup. Workspace and warm locks
prevent recovery from racing live operations, including forced expiry cleanup.
Warm, reusable-slot acquisition, and GC share a pool-maintenance lock, so
reported capacity cannot be removed or claimed by an already-running operation.
Acquisition releases that pool lock before dependency preparation while keeping
the selected workspace fenced. The requested initial TTL starts after
preparation publishes the ready assignment, so installation time does not
consume the lease.
GC revalidates each candidate under its acquisition lock and carries that fence
through the lifecycle transition. A renewal made during acquisition handoff is preserved.
An unreadable identity for a live lock owner is treated as busy, never stale.
Initial keeper validity starts after the state lock is acquired, and heartbeat
updates are monotonic with explicit renewal. If heartbeat-triggered process
cleanup cannot verify the original detached leader or rule out surviving
descendants, Ruk reports retryable resource contention and retains the exact
assignment for recovery.
Managed dependency installers use the same native POSIX process-group or
Windows Job Object supervision and never launch PowerShell. If cancellation or
registration cleanup cannot prove that the exact installer tree is gone, keep
the returned workspace fenced. Applied GC drains only the persisted,
identity-matching installer record before collecting an unassigned failed or
abandoned preparation; it fails closed when identity or termination is
uncertain.
Completion timestamps are clamped to the latest renewal, and nested heartbeat
failures remain retryable `RESOURCE_BUSY` errors in JSON output.

Ruk coordinates one host and cleans only processes and workspaces it recorded.
Do not claim multi-host locking or arbitrary orphan discovery. Read
`docs/agent-interface.md` for exact JSON fields and stdout/stderr rules.
When a JSON command fails, parse the JSON error from stderr and use its stable
`code` and `retryable` fields; process-enumeration failures are retryable
`RESOURCE_BUSY` errors, and stdout contains no success record. JSON-mode
dependency installers have their output discarded to keep memory bounded.
Shared-backend version failures are retryable dependency-preparation errors.
Custom `installCommand` values default to managed mode because Ruk cannot infer
the underlying installer; select shared mode explicitly only when the custom
command implements a supported Bun or pnpm shared layout.
Active acquisition handoffs are retryable `RESOURCE_BUSY` errors; unknown
configuration keys, malformed `.rukrc.json`, and invalid TTL ranges are
non-retryable `INVALID_ARGUMENT` errors. Interactive Linux shells do not depend
on the util-linux `script` command.
Forced GC reports only expired assignments that remain active after collection.
Explicit shorthand `remote/branch` fetches reject missing remotes unless the
start point is an existing local branch.

For updates, stable installations select completed stable releases. A current
prerelease follows newer releases on that same prerelease channel automatically;
an explicit prerelease opt-in may change channels. Discovery follows GitHub
pagination, and Bun package updates trust the exact Ruk package so its native
postinstall can run. Package installations delegate the exact version to their
owning package manager; standalone installations verify the native asset and
replace it atomically. On Windows, package and standalone updates report a
scheduled handoff and replace locked native files only after the running Ruk
process exits. Never infer installer ownership from a path when the distribution
marker is available.
