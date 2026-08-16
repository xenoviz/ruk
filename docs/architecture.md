# Architecture

## Toolchain and distributions

Ruk's shipped runtime is a single dependency-free Go application. The CLI uses
native operating-system APIs for process inspection, locking, signals, and
atomic replacement. Git and repository package managers remain explicit child
processes; Ruk does not reimplement them.

The same Go runtime is distributed in two forms:

- a small `@xenoviz/ruk` npm package with one of seven optional native platform
  packages; the package installer places the verified binary on the user's
  package-manager path and exits;
- standalone Linux, macOS, and Windows binaries for x64 and ARM64, including a
  Linux x64 musl build, with checksums and provenance attestations.

Node.js, Bun, pnpm, and Yarn may run package-manager installation or update
hooks, but none is retained as Ruk's command supervisor. Bun remains a
repository tool for VitePress and supporting scripts. Both distributions must
exercise real Git subprocess behavior in CI; a binary that only starts or
prints its version is not considered verified.

## Updates and release trust

Self-update is an explicit operation; ordinary commands never contact GitHub.
The package distribution delegates an exact version to the package manager that
owns the installation. A durable marker records that installer ownership so
path layouts do not silently select the wrong manager. Standalone builds
download only release assets from the canonical repository, enforce a size
limit, verify the SHA-256 digest committed by the readiness manifest, and
replace the executable through a same-directory staged file.

GitHub release visibility is not update readiness. Protected version tags are
immutable after creation, and the release workflow rejects a triggering commit
that is not reachable from protected `main`. Every job checks out the immutable
triggering SHA. The workflow publishes npm and builds and attests all executable
assets. A final job creates a mutable draft release, verifies the staged
checksums, uploads the assets, uploads `ruk-release.json` last, and only then
publishes the immutable release.
Update discovery considers only stable releases with a valid readiness manifest
for stable installs. A version already carrying a prerelease identifier follows
newer prereleases on that channel without a second opt-in. Incomplete releases
are ignored, so update discovery falls back to the previous ready release.

POSIX replacement retains a rollback copy until the new executable reports the
expected version. Windows standalone and package updates stage verified native
files and defer replacement to a detached helper because a running executable
may be locked. Package-mode updates report that scheduled handoff instead of
claiming immediate replacement. Release CI generates signed
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

Ruk has deliberately separate Go packages:

1. `internal/git` discovers repositories and performs Git worktree operations.
2. `internal/dependencies` identifies dependency inputs and prepares one local
   projection through a selected backend.
3. `internal/state` records preparation metadata under the common Git directory.
4. `internal/lifecycle` owns assignments, activity keepers, returns, warm, and
   garbage collection.
5. `internal/process` owns process identity, descendants, signals, and safe
   termination, with native Windows and POSIX implementations.
6. `internal/ports` and `internal/statistics` own host reservations and bounded
   usage reporting.
7. `internal/update` owns release discovery, installer delegation, integrity
   checks, prerelease selection, and executable replacement.
8. `internal/cli` composes these modules and owns the human and JSON boundary.

Business rules remain in the module that owns the relevant state or operating-
system action instead of accumulating in the CLI.

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
  among active recorded assignments. Active Ruk 0.2 reservations are imported
  under their legacy host lock during migration. They are cooperative
  reservations, not held sockets.
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
On Windows, state commits use atomic replace-with-write-through and retry only
transient access, sharing, and lock violations; permanent replacement errors
still fail immediately without deleting the last valid state.

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
Keeper completion uses the same monotonic fence, so wall-clock rollback cannot
move activity or expiry behind the latest heartbeat or explicit renewal.
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
Attached POSIX leaders and descendants are captured with their start identities,
and each identity is rechecked immediately before signaling, so PID reuse during
process enumeration also fails closed. An empty or failed identity probe is
accepted as completion only when an OS liveness check confirms that process no
longer exists. The workspace tree lock remains held until child registration is
persisted or failed registration cleanup settles, so release cannot recycle the
worktree during that handoff.
Interactive shells inherit the user's terminal and run behind a native POSIX
process-group or Windows job boundary. Ruk records the shell leader and checks
the tracked tree after it exits; an unverifiable or leaderless record fails
closed. The Go runtime does not launch util-linux `script`, PowerShell, or a
shell helper to provide this boundary, and this adapter does not allocate a PTY
or ConPTY. Detached managed commands explicitly forward wrapper interrupt and
termination signals to their tracked process group or job.

Warm workspaces enter `available` directly after detached creation and
dependency preparation. Assigned `exec` and `shell` operations reuse the same
transitions and preserve ownership whenever normal release is unsafe. A reused
acquisition also remains assigned when dependency synchronization cannot verify
that an aborted installer process tree is gone; its structured error returns the
exact recovery ID instead of recycling the slot. That retained transition clears
the incomplete handoff marker so the exact release command is executable without
making the workspace available automatically, and its structured response reads
the current post-heartbeat expiry from the retained record. Command
launch snapshots its original assignment across dependency repair and rejects
reassignment or an initially unassigned pool slot instead of adopting another
agent's lease. Assigned dependency synchronization revalidates that same immutable
assignment inside the tree lock before inspecting or modifying dependency projections.
Explicit `--fetch` is
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
