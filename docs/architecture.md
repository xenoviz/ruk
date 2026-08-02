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

## Boundaries

Ruk has four deliberately separate concerns:

1. `git.js` discovers repositories and performs Git worktree operations.
2. `fingerprint.js` identifies dependency inputs without interpreting source.
3. `dependencies.js` prepares one local projection through a selected backend.
4. `state.js` records preparation metadata under the common Git directory.

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
