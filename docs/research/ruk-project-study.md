# Ruk project study (historical snapshot)

Research snapshot: 2026-08-04. This note is based on repository-owned
documentation, implementation, tests, scripts, and workflows, plus the dated
GitHub project-state observations called out explicitly below.

This note describes the pre-Go 0.2 repository state. Its implementation links
are pinned to the final TypeScript snapshot (`86e3ac6`) for auditability; they do
not describe the shipped runtime. For the current Go-native runtime, distributions,
and release behavior, use the [architecture guide](../architecture.md),
[installation guide](../../website/getting-started/install.md), and the public
documentation site.

## Executive summary

Ruk is a dependency-aware Git worktree manager for parallel coding agents. Its
target user is an agent runner (or a human operating one) that needs many
independent writable workspaces without copying every dependency package or
allowing workspaces to share one writable `node_modules`. It is intentionally
standalone: no Treehouse service or long-running Ruk service is required.
([README](../../README.md#L3-L17))

The product has two related layers:

1. A preparation layer creates ordinary Git worktrees, fingerprints dependency
   inputs, performs a deterministic package-manager install, and records when a
   workspace's local dependency projection is ready.
   ([architecture](../architecture.md#L48-L60),
   [dependency implementation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L134-L181))
2. A lifecycle layer pools Ruk-managed worktrees and assigns them to agents with
   leases and immutable assignment IDs. The ID is a fencing token: delayed
   automation cannot renew or release a workspace after it has been reassigned.
   ([lifecycle design](../plans/2026-08-03-workspace-lifecycle-design.md#L3-L8),
   [lifecycle tests](https://github.com/xenoviz/ruk/blob/86e3ac6/test/lifecycle.test.ts#L70-L102))

The current scope is deliberately local and narrow. Coordination is limited to
one host; Ruk does not allocate ports or runtime namespaces, discover unrecorded
processes, or collect arbitrary orphan worktrees. Garbage collection acts only
on state Ruk recorded.
([README](../../README.md#L32-L34),
[lifecycle design](../plans/2026-08-03-workspace-lifecycle-design.md#L3-L8))

The repository describes version `0.1.0` as an unreleased early release. The
surface is nevertheless strongly defended by strict TypeScript, concurrency and
failure tests, cross-platform CI, package smoke tests, native executable checks,
and a manifest-gated release process.
([CHANGELOG](../../CHANGELOG.md#L3-L22),
[package metadata](../../package.json#L1-L4),
[CI workflow](../../.github/workflows/ci.yml#L15-L113))

## Domain vocabulary

| Term | Meaning in Ruk |
| --- | --- |
| **Repository root** | The current worktree's top-level path, discovered with `git rev-parse --show-toplevel`. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/git.ts#L11-L20)) |
| **Git common directory** | The shared Git administrative directory returned by `git rev-parse --git-common-dir`; Ruk stores cross-worktree state beneath it. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/git.ts#L11-L20), [state paths](https://github.com/xenoviz/ruk/blob/86e3ac6/src/state.ts#L15-L22)) |
| **Dependency projection** | Workspace-local `node_modules` directories created by the package manager. Even shared mode shares package content, not writable workspace links or metadata. ([README](../../README.md#L129-L157), [projection discovery](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L53-L73)) |
| **Managed mode** | The fallback for npm, Yarn, and custom installers, or an explicit opt-out for Bun and pnpm. Ruk runs the repository's normal install layout and does not enable a global virtual store. ([configuration](https://github.com/xenoviz/ruk/blob/86e3ac6/src/config.ts), [test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/dependencies.test.ts#L111-L128)) |
| **Shared mode** | An opt-in mode for Bun or pnpm that enables their global/virtual content store while retaining local projections. It requires Bun 1.3.14+ or pnpm 10.12.1+. ([implementation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L19-L51), [backend setup](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L81-L118)) |
| **Dependency fingerprint** | A SHA-256 identity over dependency-related files plus package-manager command/version/mode, runtime/ABI, OS, and architecture. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/fingerprint.ts#L9-L57)) |
| **Preparation record / tree record** | Cached evidence that a path was successfully prepared for a fingerprint and has particular projection paths. It is an optimization, not the source of truth. ([types](https://github.com/xenoviz/ruk/blob/86e3ac6/src/types.ts#L34-L41), [architecture](../architecture.md#L75-L83)) |
| **Managed workspace** | A pooled worktree represented by a lifecycle record (`preparing`, `assigned`, `returning`, `available`, or `failed`). ([types](https://github.com/xenoviz/ruk/blob/86e3ac6/src/types.ts#L43-L73)) |
| **Assignment** | Exclusive, host-local use of a managed workspace by one owner until an expiry time. It carries a new UUID each time a workspace is assigned. ([lifecycle design](../plans/2026-08-03-workspace-lifecycle-design.md#L19-L34)) |
| **Assignment ID / fence** | The immutable UUID that authorizes renew, process-record changes, and release. A prior ID is invalid after reuse. ([agent contract](../agent-interface.md#L34-L36), [lifecycle source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L71-L97)) |
| **Lease** | Reporting metadata (`assignedAt`, `renewedAt`, `expiresAt`) that must be renewed explicitly. Expiry alone does not reclaim a workspace. ([types](https://github.com/xenoviz/ruk/blob/86e3ac6/src/types.ts#L45-L52), [lifecycle design](../plans/2026-08-03-workspace-lifecycle-design.md#L36-L53)) |
| **Operation ID** | A separate UUID fencing multi-step preparation and collection work while the state lock is not held continuously. ([preparation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L100-L124), [collection](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L367-L425)) |
| **Tracked process** | A child launched through `ruk run`, recorded by PID and process-start identity (plus a POSIX process group ID) for the current assignment. ([types](https://github.com/xenoviz/ruk/blob/86e3ac6/src/types.ts#L54-L59), [run path](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L616-L671)) |

## Command surface

The built-in help exposes `init`, `create`, `acquire`, `renew`, `release`,
`sync`, `run`, `list`, `remove`, `status`, `gc`, and `update`, plus help and
version output. The dispatcher also accepts undocumented `exec` as an alias for
`run`.
([help text](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L47-L65),
[dispatcher](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L681-L748))

| Command | Purpose and important behavior |
| --- | --- |
| `ruk init [--json]` / `ruk sync [--json]` | Prepare or revalidate dependencies in the current worktree. `init` and `sync` are exact dispatcher aliases. ([dispatcher](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L696-L700), [sync](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L154-L174)) |
| `ruk create <branch> [--path ...] [--from ...] [--detach] [--json]` | Create an ordinary worktree, prepare it, and remove it again if preparation fails. It does **not** add a pooled lifecycle/assignment record. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L389-L437)) |
| `ruk acquire <branch> [--from ...] [--ttl ...] [--owner ...] [--json]` | Reuse the oldest available pooled workspace or create a detached, locked worktree; switch/create the requested branch, prepare dependencies, and return an assignment fence. Default TTL is 480 minutes. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L196-L263), [contract](../agent-interface.md#L12-L36)) |
| `ruk renew <assignment-id> [--ttl ...] [--json]` | Extend only the exact active assignment; the default renewal is another 480 minutes from renewal time. ([contract](../agent-interface.md#L38-L55), [source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L281-L300)) |
| `ruk release <assignment-id> [--force] [--json]` | Fence the return, stop recorded children, clean the worktree, detach it, and put it back in the pool. Non-forced release preserves ownership on surviving processes or a dirty tree; forced release may kill process trees and discard all tracked, untracked, and ignored files. ([contract](../agent-interface.md#L57-L80), [source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L346-L386)) |
| `ruk run -- <command> [args...]` | Synchronize dependencies before execution. In an assigned pooled workspace, register the detached child against the assignment and unregister it on exit; elsewhere, simply execute it after sync. The child exit code becomes Ruk's exit code. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L616-L671)) |
| `ruk list [--json]` | Join Git's worktree list with preparation and lifecycle state. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L439-L469)) |
| `ruk status [--json]` | Recompute the current fingerprint, check recorded projections, and report `ready` or `sync-required` plus lifecycle/assignment fields. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L471-L509)) |
| `ruk remove <path> [--force]` | Remove an ordinary worktree, never the current one. It refuses pooled workspaces and directs callers to `release` or `gc`. ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L512-L532)) |
| `ruk gc [--max-age ...] [--apply] [--force-expired] [--json]` | Dry-run by default. Select old `available`/`failed` records; only report expired assignments unless both `--apply` and `--force-expired` are supplied. Default age is 1,440 minutes. ([contract](../agent-interface.md#L82-L113), [source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L561-L613)) |
| `ruk update [--check] [--json]` | Explicitly discover a ready stable release and either report it, delegate an exact package version to the owning package manager, or replace a standalone executable. No normal command checks for updates. ([architecture](../architecture.md#L19-L46), [source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L735-L746)) |

For automation, `--json` promises exactly one JSON value plus a newline on
stdout, with progress and diagnostics on stderr; failures are nonzero and emit
no success record. Paths are absolute, timestamps are ISO-8601 UTC, and fields
are stable within a major release while new fields may be added.
([agent contract](../agent-interface.md#L1-L10),
[reporter routing](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L138-L152))

## End-to-end lifecycle

### Acquire

1. Ruk resolves the repository root/common directory and reads `.rukrc.json`.
   The state store is therefore shared by all linked worktrees but is not
   committed to the repository.
   ([context](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L126-L135), [state paths](https://github.com/xenoviz/ruk/blob/86e3ac6/src/state.ts#L15-L22))
2. Under the global state lock, Ruk chooses the oldest `available` workspace
   (path is the deterministic tie-breaker) and immediately gives it a new
   assignment. If none is available, it creates a new `preparing` record with an
   operation ID at a sibling path suffixed by eight UUID characters.
   ([reservation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L127-L140), [acquire](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L211-L222))
3. For a new pool member, Ruk creates a detached Git worktree and applies a Git
   worktree lock labelled `ruk pool`. It then switches to an existing local
   branch or creates the requested branch from `--from`/`HEAD`.
   ([acquire](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L224-L241), [Git operations](https://github.com/xenoviz/ruk/blob/86e3ac6/src/git.ts#L37-L60), [assignment](https://github.com/xenoviz/ruk/blob/86e3ac6/src/git.ts#L85-L101))
4. It synchronizes dependencies inside that worktree. A newly created workspace
   becomes `assigned` only after preparation succeeds. A reused workspace was
   already reserved atomically; its lease is renewed after preparation.
   ([acquire](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L242-L260), [assignment finalizer](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L143-L157))
5. New-workspace failures become `failed` records for inspection/collection.
   Reuse failures are force-cleaned and returned to `available`; an additional
   cleanup failure is surfaced together with the original error.
   ([acquire cleanup](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L264-L278), [failure state](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L160-L179))

The successful JSON record includes the assignment ID, absolute path, branch,
expiry, whether a pooled workspace was reused, dependency fingerprint, and
preparation mode. The owner is retained in state but deliberately is not part of
the documented acquire response.
([agent contract](../agent-interface.md#L21-L36),
[CLI result](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L251-L260),
[CLI test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/cli.test.ts#L84-L107))

### Dependency preparation

1. Configuration accepts only `dependencyMode` and `installCommand`; unknown
   keys and malformed commands fail. `RUK_DEPENDENCY_MODE` and a JSON-array
   `RUK_INSTALL_COMMAND` override the file for automation.
   ([configuration](https://github.com/xenoviz/ruk/blob/86e3ac6/src/config.ts#L19-L67), [tests](https://github.com/xenoviz/ruk/blob/86e3ac6/test/config.test.ts#L23-L71))
2. Without a custom command, Ruk prefers `packageManager` from `package.json`,
   then infers Bun/pnpm/Yarn/npm from lockfiles, and finally defaults to npm. It
   requires the executable on `PATH` and chooses frozen/locked installs where
   supported (`npm ci` when `package-lock.json` exists). Supported Bun and pnpm
   versions use shared mode by default; other managers retain managed installs.
   ([detection](https://github.com/xenoviz/ruk/blob/86e3ac6/src/config.ts#L69-L118), [tests](https://github.com/xenoviz/ruk/blob/86e3ac6/test/config.test.ts#L74-L116))
3. Ruk acquires a per-workspace directory lock, computes the fingerprint, and
   validates the shared backend version when applicable. If recorded fingerprint
   and all recorded projections still match, it skips installation.
   ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L134-L181), [concurrency test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/dependencies.test.ts#L130-L143))
4. Otherwise it performs the install. Shared Bun sets
   `BUN_INSTALL_GLOBAL_STORE=1` and enforces the isolated linker; shared pnpm
   enables its global virtual store. Other managers fail in shared mode.
   ([installer](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L81-L119))
5. After success, Ruk recomputes the fingerprint (so install-time changes are
   captured), requires at least one actual `node_modules` projection, and only
   then atomically records preparation state.
   ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L159-L174))

The fingerprint considers tracked and non-ignored untracked manifests,
lock/configuration files, `.rukrc.json`, and patch directories. It normalizes
text line endings, treats binary `bun.lockb` as bytes, and includes the package
manager version/command/mode, Node and Bun runtime identity, native module ABI,
OS, and CPU architecture. Ordinary source changes do not invalidate it.
([dependency file discovery](https://github.com/xenoviz/ruk/blob/86e3ac6/src/git.ts#L146-L178),
[hash construction](https://github.com/xenoviz/ruk/blob/86e3ac6/src/fingerprint.ts#L16-L46),
[behavior test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/fingerprint.test.ts#L28-L58))

### Run and process ownership

`ruk run` first calls the same synchronization path, so a manifest change is
prepared before the requested command starts. For an assigned pooled workspace,
Ruk starts the command detached, records the PID plus process-start identity, and
uses the PID as a POSIX process-group ID. Registration happens while holding the
workspace lock; if registration fails, the runner terminates the child.
([run path](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L616-L662),
[runner failure handling](https://github.com/xenoviz/ruk/blob/86e3ac6/src/process.ts#L79-L145),
[process test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/process.test.ts#L40-L65))

Process-start identity prevents a recycled PID from being killed. POSIX cleanup
signals the recorded process group; Windows uses `taskkill /T`. The Windows
runner resolves `.cmd`/`.bat` shims and passes arguments through environment JSON
to a fixed PowerShell command rather than enabling a general shell.
([process identity and termination](https://github.com/xenoviz/ruk/blob/86e3ac6/src/process.ts#L148-L205),
[Windows shim](https://github.com/xenoviz/ruk/blob/86e3ac6/src/process.ts#L22-L78),
[injection test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/process.test.ts#L26-L38))

### Release, reuse, and collection

Release changes `assigned` to `returning` under the state lock, then holds the
workspace lock while it terminates only the assignment's recorded processes. It
tries graceful termination for 1.5 seconds and requires `--force` before a hard
kill. A failed return is changed back to `assigned` with the failure retained,
so ownership is not silently lost.
([release implementation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L303-L371),
[return transitions](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L202-L251))

Git cleanup refuses a dirty tree unless forced. Forced cleanup resets tracked
content; both forced and ordinary successful cleanup run `git clean -ffdx` and
detach HEAD, removing untracked and ignored files before pooling. The lifecycle
can become `available` only after tracked process records are empty.
([Git return](https://github.com/xenoviz/ruk/blob/86e3ac6/src/git.ts#L103-L112),
[lifecycle finalizer](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L218-L235),
[Git safety test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/git.test.ts#L9-L34))

Garbage collection uses operation IDs to prevent a candidate from being
reassigned or changed between planning and deletion. Safe collection unlocks
the pooled Git worktree, removes it forcibly, then deletes lifecycle and
preparation records. Dry runs do not delete; the current worktree is skipped.
([collection fencing](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L367-L425),
[collection implementation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L534-L597),
[lifecycle test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/lifecycle.test.ts#L155-L202))

## State, locking, Git, and process safety invariants

### State and locking

- State lives at `<git-common-dir>/ruk/state.json`. Schema v2 separates
  preparation records (`trees`) from pooled lifecycle records (`workspaces`) and
  migrates v1 preparation-only state in memory before the next write.
  ([types](https://github.com/xenoviz/ruk/blob/86e3ac6/src/types.ts#L75-L86), [migration](https://github.com/xenoviz/ruk/blob/86e3ac6/src/state.ts#L134-L172),
  [migration test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/state.test.ts#L44-L69))
- Every state read is structurally validated, including absolute managed paths,
  legal lifecycle/assignment combinations, unique assignment and operation IDs,
  timestamps, and process identities. Corruption fails visibly rather than being
  silently replaced.
  ([validation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/state.ts#L33-L173), [tests](https://github.com/xenoviz/ruk/blob/86e3ac6/test/state.test.ts#L33-L42))
- State mutation is serialized by a common directory lock, validated again,
  written to a mode-`0600` temporary file, and atomically renamed over the state
  file. Parallel mutation is covered by a 12-workspace test.
  ([atomic update](https://github.com/xenoviz/ruk/blob/86e3ac6/src/state.ts#L189-L202), [test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/state.test.ts#L8-L31))
- Locks are directories created atomically and contain mode-`0600` owner metadata
  with PID, hostname, UUID token, timestamp, and process-start identity when
  available. The owner token prevents one contender from removing another's
  lock.
  ([lock implementation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lock.ts#L73-L132))
- A lock older than the stale threshold is not abandoned if its owner is a live
  process on the local host with the expected identity. Truly abandoned locks
  are renamed to unique tombstones; concurrent recovery is tested to remain
  serialized.
  ([stale detection](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lock.ts#L46-L69),
  [race test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/lock.test.ts#L64-L90))
- The global state lock serializes short metadata mutations; a hashed
  per-workspace lock serializes dependency preparation, run registration,
  release, removal, and collection for one path.
  ([lock paths](https://github.com/xenoviz/ruk/blob/86e3ac6/src/state.ts#L15-L30),
  [dependency lock](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L177-L181),
  [CLI uses](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L355-L371))

### Git and destructive-operation boundaries

- Ruk never shares one writable workspace-level `node_modules`; supported Bun
  and pnpm versions default to shared immutable package content, unsupported
  managers retain managed installs, and preparation is recorded only after
  installation succeeds.
  ([architecture invariants](../architecture.md#L62-L73))
- The current workspace cannot remove itself. Ordinary `remove` also refuses
  pooled records, ensuring lifecycle-aware cleanup goes through `release` or
  `gc`.
  ([source](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L512-L531))
- Non-forced release preserves a dirty workspace. Forced release is explicitly
  destructive and clears tracked, untracked, and ignored content; agents must
  commit or otherwise save intended artifacts before release.
  ([agent contract](../agent-interface.md#L72-L80))
- Pooled worktrees are Git-locked while retained, then explicitly unlocked only
  for GC removal.
  ([lock/unlock operations](https://github.com/xenoviz/ruk/blob/86e3ac6/src/git.ts#L114-L123),
  [collection](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L543-L551))

### Process and trust boundaries

- Ruk only claims processes registered through `ruk run` for the current
  assignment. It does not infer workspace ownership from the OS process table;
  commands launched by other means are outside cleanup coverage.
  ([lifecycle design](../plans/2026-08-03-workspace-lifecycle-design.md#L46-L48),
  [agent contract](../agent-interface.md#L115-L120))
- Assignment fences apply to process-record mutation as well as renew/release,
  and process identity checks defend against PID reuse.
  ([process record mutation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L254-L308),
  [termination guard](https://github.com/xenoviz/ruk/blob/86e3ac6/src/process.ts#L175-L187))
- Ruk executes Git, package managers, repository manifests, and lifecycle scripts
  with the current user's permissions. A repository is executable input, so the
  security model assumes trusted repositories and dependency changes.
  ([process execution](https://github.com/xenoviz/ruk/blob/86e3ac6/src/process.ts#L56-L91))

## Update and release model

The Node/npm and standalone binaries share portable `node:*` core code. `tsc`
emits the dependency-free Node.js 22.14+ package; Bun compiles seven standalone
targets: Linux x64/glibc, Linux x64/musl, Linux arm64/glibc, macOS x64/arm64,
and Windows x64/arm64.
([architecture](../architecture.md#L3-L17),
[asset list](https://github.com/xenoviz/ruk/blob/86e3ac6/src/release.ts#L1-L10),
[cross-build script](../../scripts/verify-cross-binaries.ts#L8-L36))

Updates are never background activity. The updater requests the ten most recent
GitHub releases, ignores drafts/prereleases and releases without a valid
`ruk-release.json`, and can fall back from an incomplete newer release to an
older ready one.
([update discovery](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L16-L18),
[ready-release selection](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L146-L181),
[fallback tests](https://github.com/xenoviz/ruk/blob/86e3ac6/test/update.test.ts#L142-L195))

The readiness manifest must bind the repository, version, npm package, exact set
of seven binary names, sizes, and SHA-256 digests. Standalone updates accept only
canonical GitHub release URLs, enforce a 250 MiB limit, select the OS/CPU/libc
asset, verify its manifest size and digest, and stage it beside the current
executable.
([manifest validation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L101-L144),
[URL and download validation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L213-L247),
[standalone update](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L351-L399))

On POSIX, replacement keeps a backup until the new executable reports the
expected version and rolls back on failure. On Windows, a detached `.cmd` helper
waits for the running process to exit, replaces and verifies the executable, and
rolls back if verification fails. Package installations instead invoke npm,
Bun, pnpm, or Yarn with the exact released version; unusual layouts can set
`RUK_UPDATE_INSTALLER`.
([replacement](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L278-L349),
[package delegation](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L249-L270),
[update orchestration](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L402-L442))

The release workflow starts when a version tag is pushed. It verifies tag and
package version parity, tests and publishes the npm package through trusted
publishing, builds and attests all binaries, and creates a mutable draft release.
The finalizer verifies the complete staged asset set, attests the readiness
manifest, uploads binaries/checksums first, uploads `ruk-release.json` last, and
then publishes the immutable release. A later job exercises a real
previous-ready to current Windows self-update; the first ready release skips
because it has no source version.
([release workflow](../../.github/workflows/release.yml#L14-L162),
[Windows smoke job](../../.github/workflows/release.yml#L164-L184),
[upgrade verifier](../../scripts/verify-release-update.ts#L15-L92))

Client-side checksum verification establishes asset integrity, while GitHub
attestations establish build provenance. The updater does not itself verify an
attestation.
([architecture](../architecture.md#L34-L39),
[update verification](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L351-L399))

## Test, CI, and repository posture

- The source, tests, and scripts use strict TypeScript with unchecked-index,
  exact-optional-property, unknown-catch, and isolated-module checks enabled.
  Bun 1.3.14 is pinned; only TypeScript and Node types are development
  dependencies, and published runtime dependencies are forbidden by a
  repository check.
  ([TypeScript config](../../tsconfig.json#L1-L22),
  [package metadata](../../package.json#L26-L62),
  [package verifier](https://github.com/xenoviz/ruk/blob/86e3ac6/scripts/verify-package.ts#L27-L44))
- Tests compile to JavaScript and run with Node's built-in test runner,
  sequentially, with minimum coverage thresholds of 85% lines, 90% functions,
  and 70% branches.
  ([test runner](https://github.com/xenoviz/ruk/blob/86e3ac6/scripts/run-node-tests.ts#L5-L31))
- The suite includes real Git/CLI lifecycle coverage, stale-fence rejection,
  concurrent assignment reservation, concurrent state and preparation locking,
  dirty-worktree cleanup rules, process-tree cleanup, Windows shim injection
  resistance, update rollback, and exact release-manifest validation.
  ([CLI integration](https://github.com/xenoviz/ruk/blob/86e3ac6/test/cli.test.ts#L13-L177),
  [lifecycle concurrency](https://github.com/xenoviz/ruk/blob/86e3ac6/test/lifecycle.test.ts#L143-L202),
  [lock recovery](https://github.com/xenoviz/ruk/blob/86e3ac6/test/lock.test.ts#L32-L90),
  [update rollback](https://github.com/xenoviz/ruk/blob/86e3ac6/test/update.test.ts#L256-L316),
  [manifest tests](../../test/release-manifest.test.ts#L19-L38))
- CI runs compiled Node-package checks on Node 22.14 and 24.14 across Ubuntu,
  Windows, and macOS; it separately exercises a native binary on each OS,
  installs the packed npm tarball, and cross-compiles all seven release targets.
  An aggregate `Required checks` job gates the protected branch.
  ([CI](../../.github/workflows/ci.yml#L15-L113),
  [CI ruleset](../../config/github/required-ci-ruleset.json))
- The package smoke test verifies the tarball's allow/deny file set, installs it
  with npm, invokes its CLI, and also executes the installed entry point under
  Bun.
  ([pack verifier](../../scripts/verify-pack.ts#L33-L85))
- The checked-in policy requires pull requests, at least one approval,
  code-owner and last-push approval, resolved threads, squash-only linear
  history, and the aggregate CI check. Repository administrators can bypass
  approval only through pull requests, while a separate ruleset keeps CI
  non-bypassable. A verifier also rejects mutable GitHub Action references.
  ([review ruleset](../../config/github/main-ruleset.json),
  [CI ruleset](../../config/github/required-ci-ruleset.json),
  [policy verifier](../../scripts/verify-repository-policy.ts#L12-L53))

## Maturity and evidenced limitations

Ruk is small but not a sketch: the end-to-end CLI test creates, leases, renews,
runs in, releases, reuses, expires, and collects real Git worktrees. The first
public package release is available, and the public README describes it as an
early release with a deliberately small surface.
([CLI integration test](https://github.com/xenoviz/ruk/blob/86e3ac6/test/cli.test.ts#L13-L177),
[CHANGELOG](../../CHANGELOG.md#L6-L22),
[README status](../../README.md#L19-L34))

Validation observation on 2026-08-04: the prescribed repository checks and the
46-test coverage suite passed in this workspace. The declared gates and coverage
thresholds are the ones documented in the package scripts and Node test runner.
([package scripts](../../package.json#L9-L24),
[coverage runner](https://github.com/xenoviz/ruk/blob/86e3ac6/scripts/run-node-tests.ts#L16-L31))

GitHub project-state observation on 2026-08-08: the repository is public and the
first npm package and GitHub tag have been published. These are point-in-time
hosting facts rather than durable product invariants.
([PR 1](https://github.com/xenoviz/ruk/pull/1),
[releases](https://github.com/xenoviz/ruk/releases),
[tags](https://github.com/xenoviz/ruk/tags),
[open Dependabot PRs](https://github.com/xenoviz/ruk/pulls?q=is%3Apr%20state%3Aopen%20author%3Aapp%2Fdependabot),
[open issues](https://github.com/xenoviz/ruk/issues?q=is%3Aissue%20state%3Aopen))

Current explicit ceilings are:

- coordination, state locks, leases, and process cleanup are host-local;
  ([lifecycle design](../plans/2026-08-03-workspace-lifecycle-design.md#L3-L8))
- there is no port/runtime namespace allocation;
  ([README](../../README.md#L32-L34))
- unrecorded processes and arbitrary orphan worktrees are neither discovered nor
  collected;
  ([lifecycle design](../plans/2026-08-03-workspace-lifecycle-design.md#L46-L53))
- dependency readiness is Node-ecosystem-specific: supported automatic managers
  are Bun, pnpm, npm, and Yarn, shared mode only supports Bun/pnpm, and a
  successful install must create at least one `node_modules` projection;
  ([detection](https://github.com/xenoviz/ruk/blob/86e3ac6/src/config.ts#L100-L118),
  [shared backend](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L40-L51),
  [projection requirement](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L159-L166))
- Linux arm64/musl has no standalone asset, unlike Linux x64/musl;
  ([asset selection](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L197-L210))
- release cleanup is intentionally lossy after assignment work is saved: ignored
  files are always removed, and `--force` also discards tracked/untracked edits;
  ([agent contract](../agent-interface.md#L72-L80))

## Questions and tensions for Wayfinder

These are not claims of defects; they are the highest-value areas where the
current boundaries create product or architecture choices.

1. **Is Ruk a workspace primitive or an agent orchestration layer?** The code is
   deliberately a host-local primitive with no service, namespace allocation,
   or unrecorded-process discovery. Wayfinder should decide whether those remain
   integrations owned by a higher layer or belong in Ruk.
   ([README](../../README.md#L3-L4),
   [lifecycle scope](../plans/2026-08-03-workspace-lifecycle-design.md#L3-L8))
2. **Who owns lease liveness?** Expiry is advisory until an operator invokes
   forced GC, and renewal is explicit. Explore heartbeat responsibility,
   visibility for nearly expired work, clock assumptions, and safe recovery
   after the assigning agent disappears.
   ([lifecycle design](../plans/2026-08-03-workspace-lifecycle-design.md#L36-L53),
   [GC contract](../agent-interface.md#L82-L113))
3. **How should externally launched services be handled?** Only `ruk run` creates
   process ownership records. IDE terminals, nested tools that daemonize away
   from the tracked group, and commands started directly in the worktree may
   outlive release without Ruk knowing.
   ([run contract](../agent-interface.md#L115-L120),
   [process non-goal](../plans/2026-08-03-workspace-lifecycle-design.md#L46-L48))
4. **What makes a pooled workspace compatible?** Selection currently takes the
   oldest unreserved `available` record without filtering by prior fingerprint,
   branch, manager, or mode; correctness is recovered by branch switching and a
   full sync. Explore whether this is the right simple policy at larger pool
   sizes or whether locality-aware selection would materially reduce installs.
   ([selection](https://github.com/xenoviz/ruk/blob/86e3ac6/src/lifecycle.ts#L127-L140),
   [acquire/sync](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L224-L250))
5. **How far should dependency support expand?** The model is clean for
   JavaScript package managers but defines readiness as `node_modules`
   projections. Wayfinder should determine whether Python/Rust/Go or custom
   artifact preparation belongs in the product, and what a generic readiness
   contract would be.
   ([projection logic](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L53-L73),
   [projection requirement](https://github.com/xenoviz/ruk/blob/86e3ac6/src/dependencies.ts#L159-L166))
6. **How should branch conflicts be presented?** Reassignment switches an
   existing pooled worktree to a requested local branch or creates it from a
   start point. Git can reject a branch already checked out elsewhere; the
   current behavior is to surface the Git failure and return/mark the workspace.
   Explore whether preflight diagnostics or a branch ownership view would help
   agents recover.
   ([Git assignment](https://github.com/xenoviz/ruk/blob/86e3ac6/src/git.ts#L85-L101),
   [acquire cleanup](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L264-L278))
7. **What observability is sufficient without a daemon?** JSON contracts expose
   lifecycle, assignment, expiry, preparation, and GC plans, while process owner
   and failure details live only in state. Explore the minimum inspection/event
   surface needed for fleet operators without turning Ruk into a service.
   ([inspection contract](../agent-interface.md#L115-L120),
   [workspace record](https://github.com/xenoviz/ruk/blob/86e3ac6/src/types.ts#L61-L73))
8. **Should `create` and `acquire` remain separate mental models?** `create`
   prepares an ordinary removable worktree; `acquire` creates a locked pooled
   worktree governed by assignment fences. The distinction is sound in code but
   deserves explicit user-journey validation.
   ([create](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L389-L437), [acquire](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L196-L278),
   [remove policy](https://github.com/xenoviz/ruk/blob/86e3ac6/src/cli.ts#L512-L531))
9. **What update trust should be automatic?** The client verifies a
   manifest-pinned digest and canonical URL, while provenance attestation
   verification remains a separate user action. Wayfinder should decide whether
   that is the intended long-term trust boundary.
   ([architecture](../architecture.md#L19-L39),
   [update verification](https://github.com/xenoviz/ruk/blob/86e3ac6/src/update.ts#L351-L399))
10. **How broad should release validation become?** CI deeply tests the Node
    distribution and exercises one native binary per host, while cross-targets
    are compile/size checked and the only real release-to-release smoke is
    Windows. Explore which additional native lifecycle and upgrade paths are
    worth their CI cost.
    ([CI jobs](../../.github/workflows/ci.yml#L15-L113),
    [cross-binary check](../../scripts/verify-cross-binaries.ts#L8-L39),
    [release smoke](../../.github/workflows/release.yml#L164-L184))
