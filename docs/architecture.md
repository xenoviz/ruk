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

GitHub release visibility is not update readiness. The release workflow first
publishes npm and builds and attests all executable assets. A final job verifies
the staged checksums, uploads the assets, and uploads `ruk-release.json` last.
Update discovery considers only stable releases with a valid readiness manifest
and falls back to the previous ready release while publication is incomplete.

POSIX replacement retains a rollback copy until the new executable reports the
expected version. Windows defers replacement to a detached operating-system
helper because a running executable may be locked. Release CI generates signed
GitHub build-provenance attestations in addition to checksums. Checksums protect
download integrity; provenance provides an independently verifiable record of
which repository workflow produced an executable.

Path-based package-manager detection is a convenience, not an authority. Users
can select npm, Bun, pnpm, or Yarn explicitly with `--via` or
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

`cli.js` composes these modules. Business rules should remain in the closest
module instead of accumulating in the CLI.

## Safety invariants

- Never share one writable `node_modules` directory between workspaces.
- Managed dependency layout is the default.
- Shared mode must fail when the package manager/backend is unsupported.
- A workspace is recorded as prepared only after installation succeeds.
- Preparation of the same workspace is serialized.
- State replacement is atomic and state files are owner-readable only.
- A stale lock owned by a live local process is never removed by age alone.
- The current workspace cannot remove itself.
- Machine-readable output contains one JSON value on stdout; diagnostics go to
  stderr.

## State

State is stored in `<git-common-dir>/ruk/state.json`, so linked worktrees share
metadata without committing it. Per-workspace preparation locks and the state
lock live beside it.

State is an optimization, not source of truth. Git and the dependency
fingerprint remain authoritative. Invalid state fails visibly rather than being
silently replaced.

## Planned workspace lifecycle

The next lifecycle layer will introduce reusable workspace pooling without
using rental-oriented terminology:

```text
available -> preparing -> assigned -> returning -> available
                         \-> failed
```

Each assignment will have an immutable assignment ID so delayed automation
cannot return a workspace that has since been reassigned. That work must also
include process ownership, crash recovery, port/runtime namespaces, and safe
garbage collection; those guarantees should not be added piecemeal.
