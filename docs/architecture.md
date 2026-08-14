# Architecture

## Toolchain and distributions

Ruk is authored in strict TypeScript and uses the pinned Bun toolchain for
dependency installation, repository scripts, and standalone executable builds.
TypeScript performs static checking and emits the Node-compatible npm package.

The core uses portable `node:*` APIs and must not depend on `Bun.*` APIs. This
keeps one source tree valid for both distributions:

- compiled JavaScript for Node.js 22.14 and newer, published to npm without
  runtime dependencies;
- self-contained Bun executables for Linux, macOS, and Windows.

Both distributions must exercise real Git subprocess behavior in CI. A binary
that only starts or prints its version is not considered verified.

## Updates and release trust

Self-update is an explicit operation; ordinary commands never contact GitHub.
The package distribution delegates an exact version to the package manager that
owns the installation. Standalone builds download only release assets from the
canonical repository, enforce a size limit, verify the SHA-256 digest committed
by the readiness manifest, and replace the executable through a same-directory
staged file.

GitHub release visibility is not update readiness. Protected version tags are
immutable after creation, and the release workflow rejects a triggering commit
that is not reachable from protected `main`. Every job checks out the immutable
triggering SHA. The workflow publishes npm and builds and attests all executable
assets. A final job creates a mutable draft release, verifies the staged
checksums, uploads the assets, uploads `ruk-release.json` last, and only then
publishes the immutable release.
Update discovery considers only stable releases with a valid readiness manifest
and falls back to the previous ready release while publication is incomplete.

POSIX replacement retains a rollback copy until the new executable reports the
expected version. Windows defers replacement to a detached operating-system
helper because a running executable may be locked. Release CI generates signed
GitHub build-provenance attestations in addition to checksums. Checksums protect
download integrity; provenance provides an independently verifiable record of
which repository workflow produced an executable.

Path-based package-manager detection is a convenience, not an authority. Users
can override it for unusual installation layouts or automation with
`RUK_UPDATE_INSTALLER`. Starting with the second ready release, release CI
downloads the prior Windows executable, runs its updater against the newly
finalized release, and verifies the executable version after deferred
replacement.

## Boundaries

Ruk has five deliberately separate concerns:

1. `git.js` discovers repositories and performs Git worktree operations.
2. `fingerprint.js` identifies dependency inputs without interpreting source.
3. `dependencies.js` prepares one local projection through a selected backend.
4. `state.js` records preparation metadata under the common Git directory.
5. `update.js` owns release discovery, installer delegation, integrity checks,
   and executable replacement.
6. `ports.js` performs short-lived OS port probes, coordinates a host-level
   reservation registry, and maps names into environment variables.
7. `statistics.js` derives aggregate and optional on-demand disk measurements.
8. `activity.js` owns assignment heartbeat timing and keeper cleanup.
9. `checkout.js` owns primary-checkout sharing policy and diagnostics.

`cli.js` composes these modules. Business rules should remain in the closest
module instead of accumulating in the CLI.

## Safety invariants

- Never share one writable `node_modules` directory between workspaces.
- Shared immutable package storage is the default for supported Bun and pnpm
  versions; unsupported managers retain their managed layout.
- Shared mode must fail when the package manager/backend is unsupported.
- A workspace is recorded as prepared only after installation succeeds.
- Recorded dependency projections are integrity-validated before reuse; modified
  projections are discarded instead of entering the pool.
- Preparation of the same workspace is serialized.
- State replacement is atomic and state files are owner-readable only.
- A stale lock owned by a live local process is never removed by age alone.
- The current workspace cannot remove itself.
- Machine-readable output contains one JSON value on stdout; diagnostics go to
  stderr, while suppressed installer streams are discarded rather than buffered.
- Named ports are serialized through a stable per-user host registry and unique
  among active recorded assignments. They are cooperative reservations, not
  held sockets.
- Metrics are bounded counters; ordinary commands never append an event log or
  scan workspace disk usage.
- Only observed Ruk operations renew leases. Ruk does not infer activity from
  filesystem timestamps or keep an always-running daemon.
- Task commands refuse a shared primary checkout while assignments are active
  unless repository policy or an explicit command override permits it. A
  repository-wide primary-checkout fence serializes deny-mode task execution
  with assignment publication, so the guard cannot pass on a stale snapshot.

## State

Version 4 state is stored in `<git-common-dir>/ruk/state.json`, so linked worktrees share
metadata without committing it. Per-workspace preparation locks and the state
lock live beside it.

Loading migrates version 1 through version 3 records in memory. Existing
assignment IDs, ownership, expiry, and process records stay intact. Version 4
adds the lease duration, last observed activity, and fenced lease keepers needed
for automatic renewal.

State is an optimization, not source of truth. Git and the dependency
fingerprint remain authoritative. Invalid state fails visibly rather than being
silently replaced.

## Workspace lifecycle

Ruk manages reusable agent workspaces with this lifecycle:

```text
absent -> preparing -> assigned -> returning -> available
             |           ^                        |
             v           +------------------------+
           failed
```

Each assignment has an immutable assignment ID so delayed automation cannot
return a workspace that has since been reassigned. Leases expire for reporting;
reclaiming expired assignments requires an explicit forced GC operation.
Managed `run`, `exec`, `shell`, and assigned `sync` operations register a
short-lived keeper and renew from the assignment's stored lease duration while
work continues. Multiple keepers can coexist, and each removes only its own
fenced record. A lost keeper stops its tracked command rather than allowing
cleanup to race an unowned process. Initial keeper time is captured only after
the state lock is acquired, and heartbeat timestamps are monotonic inside the
transaction, so lock contention or a delayed refresh cannot publish an expired
keeper, shorten expiry, or overwrite a newer explicit renewal.
Process cleanup is limited to children recorded through `ruk run` for the
assignment. If identity lookup fails while descendants remain, automatic
release stops and retains the assignment. Leaderless POSIX process groups fail
closed because their numeric IDs can be reused.
Command completion bypasses the short-lived identity cache before removing its
process record, so a recently exited child cannot remain falsely active.
Windows registration cleanup terminates the new process tree only with a verified
leader identity and otherwise retains ownership while descendants remain or the
leader PID is reused.
Abort cleanup follows the same fail-closed rule for attached children and
detached groups. If the original leader identity or surviving descendants
cannot be verified, or any termination safety check refuses the signal, the
process is retained without a fallback PID signal. The cleanup error is
preserved with the heartbeat failure, and automatic release retains the assignment.
Interactive shells use their isolated session ID on Linux and controlling
terminal on macOS, where `ps` does not expose the POSIX session ID. A live
identity-fenced sentinel prevents macOS terminal-name reuse from authorizing
cleanup, and a leaderless Linux session fails closed. Detached managed commands
explicitly forward wrapper interrupt and termination signals to their process group.
Linux checks for the util-linux `script` command before acquiring an interactive
shell workspace.

Warm workspaces enter `available` directly after detached creation and
dependency preparation. Assigned `exec` and `shell` operations reuse the same
transitions and preserve ownership whenever normal release is unsafe. A reused
acquisition also remains assigned when dependency synchronization cannot verify
that an aborted installer process tree is gone; its structured error returns the
exact recovery ID instead of recycling the slot. That retained transition clears
the incomplete handoff marker so the exact release command is executable without
making the workspace available automatically. Command
launch snapshots its original assignment across dependency repair and rejects
reassignment or an initially unassigned pool slot instead of adopting another
agent's lease. Explicit `--fetch` is
the only workspace operation in this layer that contacts a Git remote, and an
explicit remote name must exist.
This includes shorthand `remote/branch` start points unless the name resolves to
an existing local branch.
Default fetch rejects multiple remotes when `origin` is absent.

Garbage collection can recover abandoned preparation, acquisition handoff, and
collection operations. Warm, acquisition, and per-workspace locks prevent
recovery from racing live work; operation IDs and update timestamps fence final
transitions. Pool reservations are published only after their handoff lock is
held. Acquisition recovery revalidates under the same lock, and a failed
recovery restores its acquisition marker. Failed removal is re-locked before a
workspace becomes available, and post-removal state remains retryable and is
excluded from available-capacity statistics and warm counts. Forced-expiry
release uses the handoff lock before it changes workspace state.
Warm validation and garbage collection share a pool-maintenance lock so a slot
cannot be removed after being reported as available.
Collection revalidates every stale snapshot under the slot's acquisition lock,
passes that update fence into the lifecycle transaction, and acquisition handoff
does not overwrite a concurrent lease renewal.
Ordinary release cannot cross an active acquisition marker, and an unreadable
identity for a provably live lock owner never authorizes stale-lock recovery.

See [the lifecycle design](./plans/2026-08-03-workspace-lifecycle-design.md)
for transitions, fencing, GC boundaries, and non-goals.
