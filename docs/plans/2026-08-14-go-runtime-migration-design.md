# Go Runtime Migration Design

**Status:** Approved for implementation  
**Target:** Ruk 0.3  
**Scope:** Replace the shipped TypeScript CLI with a feature-compatible Go CLI

## Goal

Ruk will move its shipped runtime to Go while preserving its commands, JSON
contract, state files, safety rules, release channels, and supported platforms.
The migration will reduce Ruk-owned memory, remove routine PowerShell process
inspection on Windows, and produce one native application runtime.

The work will use one integration branch and one final pull request. Small,
reviewable commits will build the replacement behind a compatibility harness.
The TypeScript CLI will remain the behavioral reference during development and
will be removed before the migration pull request becomes merge-ready.

## Scope

The migration includes the complete shipped CLI:

- command parsing, help, human output, and JSON output;
- configuration loading and validation;
- Git repository discovery and worktree management;
- dependency fingerprints, projections, and installer execution;
- state migration, atomic persistence, and directory locks;
- assignment lifecycle, activity keepers, release, warm, and garbage collection;
- process identity, descendant tracking, signals, and safe termination;
- named ports, statistics, self-update, and release verification.

VitePress, documentation, and repository automation may remain TypeScript.
They do not contribute to the installed CLI's steady-state memory.

## Non-goals

- Redesigning the public CLI during the language migration.
- Introducing a permanent TypeScript and Go supervisor split.
- Reimplementing Git or package managers.
- Adding an always-running daemon.
- Changing the state version without a demonstrated compatibility need.
- Weakening fail-closed process, lock, release, or update behavior.

## Runtime architecture

The Go code will preserve Ruk's current responsibility boundaries:

```text
cmd/ruk                 CLI entrypoint
internal/cli            parsing, command composition, and output
internal/config         .rukrc.json loading and validation
internal/git            repository and worktree operations
internal/dependencies   fingerprints and dependency preparation
internal/state          v1-v4 migration and atomic persistence
internal/lifecycle      assignments, activity, returns, warm, and GC
internal/lock           cross-process locks and owner validation
internal/process        process identity, trees, signals, and termination
internal/ports          host-level port reservations
internal/statistics     bounded counters and disk statistics
internal/update         trusted discovery and executable replacement
```

The migration will translate behavior and boundaries rather than files. CLI
code will compose internal modules; business rules will remain in the module
that owns the relevant state or operating-system action.

The existing `experiments/go-supervisor` remains a benchmark prototype. It can
inform the production implementation, but production code will start under the
new package structure.

## Command data flow

Every command will use the same high-level path:

```text
arguments
  -> configuration
  -> repository discovery
  -> lock and state transaction
  -> Git, dependency, or process operation
  -> state commit
  -> human or JSON response
```

Git and dependency installers remain subprocesses. Go will own their lifecycle,
output policy, cancellation, and process tracking.

JSON compatibility is strict. The Go CLI must preserve field names, error codes,
retryability, recovery commands, and the single-record stdout and stderr
contract. Human-readable messages should also remain stable when scripts or
tests depend on them.

## State and lifecycle compatibility

Go will read existing state versions 1 through 4 and write the current version
4 shape. Users must be able to replace the TypeScript executable while keeping
active assignments and pooled workspaces.

The migration preserves these lifecycle rules:

- Assignment IDs remain immutable fences.
- Operation IDs protect incomplete transitions.
- Activity keepers renew from the stored lease duration.
- Multiple keepers may coexist and remove only their own records.
- Activity and renewal timestamps remain monotonic under the state lock.
- Expiry remains informational until explicit forced collection.
- Release retains ownership when process cleanup cannot prove safety.
- Garbage collection revalidates stale snapshots under the correct locks.
- Machine-readable failures retain their current error category and retryability.

The migration will introduce state version 5 only if Go needs new durable data
that cannot fit the version 4 contract. Any such change requires a separate
design amendment and bidirectional migration fixtures.

## Native operating-system layer

Platform-specific implementations will sit behind small internal interfaces:

- process identity and liveness;
- descendant and session enumeration;
- signal forwarding and tree termination;
- atomic replacement and file permissions;
- lock ownership and stale-owner checks;
- standalone executable update behavior.

Windows will use native operating-system APIs for routine process inspection.
It must not launch PowerShell for liveness polling. Native calls must preserve
the current safety model: an unreadable identity, reused PID, uncertain process
tree, or incomplete termination retains the assignment.

POSIX implementations will preserve process-group, session, terminal, and start
identity fences. They will revalidate identities immediately before signaling.

## Packaging and releases

GitHub Releases will publish native Go executables for every currently supported
operating system and architecture. The workflow will keep checksums, provenance
attestations, the readiness manifest, atomic updates, and rollback behavior.

The `@xenoviz/ruk` package will become a small distribution package. It will
select a platform-specific optional package containing the correct Go binary.
It must preserve the installed `ruk` command.

The npm launcher must not keep Node resident during a managed command. Tests
will inspect the process tree after startup. If npm-generated shims cannot meet
this requirement reliably, npm will act as a one-time installer that places the
native executable in a stable user bin directory.

Release jobs will run in this order:

1. Build and test Go executables.
2. Run cross-platform compatibility tests.
3. Generate checksums and provenance.
4. Publish platform-specific npm packages.
5. Publish the `@xenoviz/ruk` metadata package.
6. Upload the readiness manifest last.
7. Publish the GitHub release after all artifacts verify.

The first Go release must update successfully from the previous TypeScript
standalone release. Failed verification must restore that previous executable.

## Compatibility harness

A shared harness will run the TypeScript and Go CLIs against equivalent fresh
repositories. It will normalize temporary paths, timestamps, PIDs, and UUIDs,
then compare every stable result.

The harness will cover:

- every command, option, and exit code;
- help, version, human, and JSON output;
- structured errors and retryability;
- state versions 1 through 4;
- acquire, renew, release, warm, and garbage collection;
- concurrent keepers and lock contention;
- dependency modes, projections, and failed installers;
- named ports and host reservations;
- Git remotes, branches, and worktree recovery;
- signals, detached children, descendants, sessions, and PID reuse;
- self-update, checksum failure, deferred replacement, and rollback.

Windows tests will assert that ordinary Ruk operations launch zero PowerShell
processes. Stress tests will verify that repeated identity probes keep process
handles, goroutines, subprocess counts, and memory bounded.

## Performance gates

Correctness remains the first release gate. The migration must also prove the
reason for changing runtimes.

Repeated Cloud and platform benchmarks should show at least 50 percent lower
idle Ruk-owned resident memory than the Node distribution at 1, 10, and 20
concurrent managed commands. The benchmark will also record cold start, peak
memory, binary size, and child-process counts. Routine Windows inspection must
spawn no PowerShell processes.

Benchmark results are machine-specific. The release decision will use repeated
runs on controlled runners rather than a single local sample.

## Implementation sequence

Four isolated work lanes can proceed in parallel:

1. **State and lifecycle:** types, migrations, atomic persistence, assignments,
   activity keepers, returns, warm, and garbage collection.
2. **Native process and locking:** Windows and POSIX identity, descendants,
   PID-reuse fences, signals, termination, and directory locks.
3. **Repository operations:** Git, worktrees, fingerprints, dependencies,
   ports, and statistics.
4. **Compatibility and distribution:** conformance harness, npm packages,
   release manifests, self-update, documentation, and the bundled Ruk skill.

The CLI composition layer will integrate these foundations after their contracts
stabilize. Each lane owns separate files and lands small commits into the
integration branch.

Codex Cloud will run the large Linux conformance and coverage suites at useful
checkpoints. GitHub Actions will run the Windows, macOS, native packaging, and
required final checks. Local development will avoid heavy process suites.

## Cutover

Before the pull request becomes merge-ready, the project will:

1. Freeze the compatibility fixtures.
2. Pass TypeScript-versus-Go conformance tests.
3. Switch package and release entrypoints to Go.
4. Remove the TypeScript runtime and obsolete runtime tests.
5. Update architecture docs, guides, examples, and `ruk-workspaces` skill.
6. Pass Cloud, platform, packaging, update, and benchmark verification.
7. Resolve all review feedback and obtain a clean exact-head Codex review.

The project will publish `0.3.0-beta.1` for installation, upgrade, update, and
recovery smoke tests. The same Go implementation will become stable `0.3.0`
after the prerelease passes those checks. The repository will not maintain two
production CLIs.

## Main risks

| Risk | Mitigation |
| --- | --- |
| Subtle state incompatibility | Golden state fixtures and cross-version lifecycle scenarios |
| Unsafe process cleanup | Native identity fences, forced PID-reuse tests, and fail-closed errors |
| Cross-platform lock differences | Real Windows, macOS, and Linux contention tests |
| npm wrapper keeps Node alive | Process-tree acceptance test and installer fallback |
| Large review surface | Small commits, isolated ownership, one integration owner |
| False performance conclusion | Repeated controlled benchmarks at several concurrency levels |

## Completion criteria

The migration is complete when:

- Go provides every supported Ruk command and feature.
- Existing state and workspaces continue without manual conversion.
- Public human and JSON contracts remain compatible.
- All lifecycle and process-safety invariants pass on every platform.
- npm and standalone installation, update, and rollback work.
- No routine Windows liveness check starts PowerShell.
- Memory benchmarks meet the agreed target.
- The TypeScript runtime is removed.
- Documentation and the bundled skill describe the Go implementation.
- Required checks and exact-head Codex review are clean.
