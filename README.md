# Ruk

[![npm beta version](https://img.shields.io/npm/v/@xenoviz/ruk?tag=beta&label=npm%20beta)](https://www.npmjs.com/package/@xenoviz/ruk)

Ruk creates dependency-aware Git workspaces for parallel coding agents. It is
an independent, Go-native worktree manager: no Treehouse runtime, service, or
resident JavaScript supervisor is required.

The command is pronounced “rook.”

[Read the documentation](https://xenoviz.github.io/ruk/) for installation,
agent workflows, configuration, command reference, and troubleshooting.

## Why

Twenty agents working in a large monorepo should not require twenty complete
copies of every package. They also must not share one writable `node_modules`
directory: workspace symlinks can point into the wrong branch, and one install
can break every active agent.

Ruk keeps each workspace's link projection local and, with supported Bun and
pnpm versions, shares only the package manager's content-addressed store.
Dependency inputs are fingerprinted so a prepared workspace can skip unchanged
installs.

## Status

Ruk 0.3 is a Go-native beta with a deliberately small, tested surface:

- create, inspect, run commands in, and remove Git worktrees;
- acquire, renew, release, and reuse fenced agent workspace assignments;
- prewarm pools, run short assigned jobs, and open interactive assigned shells;
- reserve named host-local ports and inject them into assigned processes;
- report recorded reuse, preparation, failure, timing, and optional disk metrics;
- track commands launched by `ruk run` and safely collect recorded workspaces;
- renew assignments automatically while Ruk-managed work remains active;
- keep the primary checkout as a guarded control location during parallel work;
- fingerprint root and workspace manifests, lockfiles, package-manager config,
  patches, runtime, platform, architecture, and install strategy;
- serialize preparation of the same workspace;
- share immutable package content by default with supported Bun and pnpm versions;
- allow repositories to opt out when an isolated layout is incompatible;
- inspect processes with native operating-system APIs, including Windows, so
  routine liveness checks do not launch PowerShell;
- publish one native runtime through npm platform packages or standalone
  executables.

Lifecycle coordination is local to one host. Ruk allocates cooperative named
ports but provides no broader runtime namespaces. Garbage collection acts only
on Ruk-managed records; it does not discover or remove arbitrary orphan
worktrees or processes.

## Requirements

- Git
- the repository's declared package manager
- Node.js, Bun, pnpm, or Yarn when using that package manager to install or
  update Ruk from npm
- Bun 1.3.14+ or pnpm 10.12.1+ for the default shared dependency mode

## Install

Ruk 0.3 is currently published on npm's `beta` channel:

```bash
npm install --global @xenoviz/ruk@beta
```

The package manager installs a matching native optional package for the host.
When install scripts are allowed, postinstall places the native binary eagerly;
when they are blocked, the first `ruk` invocation finishes placement and then
runs that binary. Node.js is not kept resident as the command supervisor
afterward. Bun can also install the package:

```bash
bun install --global @xenoviz/ruk@beta
```

GitHub Releases also provides standalone Linux, macOS, and Windows executables
for x64 and ARM64, including Linux x64 with musl. These are native Go binaries,
so users do not need Node.js or Bun installed to run Ruk. Checksums are
published beside every executable, and GitHub records signed build-provenance
attestations for the release binaries.

## Usage

Acquire a workspace for an agent and retain both values from its JSON output:

```bash
ruk acquire agent/auth-flow --owner agent-17 --json
cd <returned-path>
ruk run -- bun test
ruk release <returned-assignmentId> --json
```

The assignment ID is a fencing token, so renew and release must use the exact
value returned by acquire. Managed commands renew automatically while active;
use explicit `renew` for long idle editing sessions. See the [agent JSON contract](./docs/agent-interface.md)
and [workspace lifecycle design](./docs/plans/2026-08-03-workspace-lifecycle-design.md).

Prepare the current workspace:

```bash
ruk init
```

Create and prepare an agent workspace:

```bash
ruk create agent/auth-flow
```

Run a command after verifying dependency readiness:

```bash
ruk run -- bun test
ruk run -- bun run typecheck
```

When other Ruk assignments are active, `run` and `sync` refuse task work in the
primary checkout by default. Acquire a dedicated workspace, configure
`sharedCheckoutPolicy`, or use `--allow-shared-checkout` for one intentional
command. Default deny-mode task execution is serialized with assignment
publication rather than relying on a point-in-time state snapshot.

Run a short job with automatic safe release, or prewarm before an agent burst:

```bash
ruk exec agent/check -- bun test
ruk warm --count 5 --from origin/main --fetch --json
```

Reserve named ports for an assigned workspace:

```bash
ruk acquire agent/web --port app --port inspector --json
```

`ruk run`, `ruk exec`, and `ruk shell` expose those values as
`RUK_PORT_APP` and `RUK_PORT_INSPECTOR`. Reservations coordinate Ruk
assignments on one host; they do not hold sockets against unrelated processes.

Inspect or clean up:

```bash
ruk status
ruk status --json
ruk status --explain
ruk stats --json
ruk stats --disk --json
ruk list --json
ruk remove ../project-agent-auth-flow
```

Ruk refuses to remove the workspace in which it is currently running. Removing
a dirty workspace requires Git's normal protection to pass or an explicit
`--force`.

Update Ruk without guessing how it was installed:

```bash
ruk update --check
ruk update
```

Standalone executables select the matching operating-system, architecture, and
Linux libc asset, verify its manifest-pinned SHA-256 digest, and replace the
current executable atomically. Published checksum files remain available for
manual verification. A failed post-replacement version check rolls back on
POSIX systems. Windows schedules locked executable replacement after the
running Ruk process exits, including when a package manager installs the new
native package. Package installations otherwise delegate the exact released
version to their owning package manager rather than modifying managed files
directly. A stable install
selects stable releases; an install already running a prerelease follows newer
prereleases on that channel. Ruk never performs background update checks or
downloads.

For unusual global installation layouts or automation, set
`RUK_UPDATE_INSTALLER` to `npm`, `bun`, `pnpm`, or `yarn` to override automatic
detection.

## Dependency modes

Ruk defaults to shared package content for supported Bun and pnpm versions.
Workspace links and package-manager metadata remain local:

```json
{
  "dependencyMode": "shared"
}
```

No configuration is required for this default. Save `.rukrc.json` in the
repository root only when overriding it. npm, Yarn, and other detected
executables use managed mode unless explicitly configured otherwise.
Custom `installCommand` values also default to managed mode, even when their
executable is Bun or pnpm; select shared mode explicitly only for a compatible
custom command.

If a repository is incompatible with an isolated shared-store layout, opt out:

```json
{
  "dependencyMode": "managed"
}
```

Shared Bun mode sets `BUN_INSTALL_GLOBAL_STORE=1` and uses `--linker isolated`.
Shared pnpm mode enables its global virtual store. External package contents
are shared; workspace links and package-manager metadata remain local.

Run type checks, builds, tests, bundlers, lifecycle scripts, and repository
package-policy checks before relying on the default in automation. Use managed
mode if any check depends on a traditional dependency layout.

For a custom deterministic installer:

```json
{
  "dependencyMode": "managed",
  "installCommand": ["npm", "ci"]
}
```

Temporary automation may use `RUK_DEPENDENCY_MODE` and
`RUK_INSTALL_COMMAND`; the latter must contain a JSON string array.

## Dependency fingerprint

The fingerprint includes:

- all tracked or non-ignored `package.json` files;
- Bun, npm, pnpm, and Yarn lock/configuration files;
- patch directories;
- package-manager name, version, command, and dependency mode;
- the selected dependency manager's runtime identity, when one is supplied;
- operating system and CPU architecture.

Changing ordinary source code does not invalidate the dependency projection.
Changing a manifest or lockfile prepares only that workspace; immutable package
content already present in the package manager store remains reusable.

## Development

```bash
bun install --frozen-lockfile
bun run check
go test ./...
go test -race ./...
bun run test:conformance
bun run binary:check
bun run binary:cross-check
bun run pack:check
```

The shipped runtime is written in Go and has no third-party runtime
dependencies. Bun remains the repository toolchain for VitePress, documentation,
and supporting scripts; it is not required to run the installed Ruk command.
The module targets Go 1.24, and release CI pins Go 1.24.6 for reproducible
native builds.

This repository commits managed mode in `.rukrc.json` because Bun's isolated
global-store linker does not reliably install the VitePress dependency graph on
Windows. The exception is repository-specific; supported Bun and pnpm projects
still default to shared package content.

See [CONTRIBUTING.md](./CONTRIBUTING.md) and
[docs/architecture.md](./docs/architecture.md) before changing lifecycle,
locking, state, or dependency behavior.

## Security and releases

- Report vulnerabilities privately by following [the security policy](./SECURITY.md).
- Published runtime code has no third-party npm dependencies.
- CI runs Go vet, unit, race, conformance, native package, and standalone
  executable checks in the cloud environment.
- GitHub Actions exercises native process, shell, packaging, and update paths
  on Linux, Windows, and macOS.
- Routine Windows process inspection uses native APIs and launches zero
  PowerShell helpers.
- GitHub Actions are pinned to immutable commit SHAs.
- Releases verify tag/package parity, publish seven native npm platform packages
  plus the root package through npm trusted publishing with provenance, and
  attach seven standalone executables with SHA-256 checksums and signed GitHub
  build-provenance attestations.
- A release becomes visible to `ruk update` only after npm publication and all
  binaries, checksums, and attestations succeed; `ruk-release.json` is uploaded
  last. Later releases exercise a real previous-to-current Windows self-update.
- `main` is governed by a checked-in ruleset requiring reviewed pull requests,
  code-owner approval, linear history, resolved discussions, and passing CI.
  Repository administrators can bypass the approval rule only through a pull
  request, which prevents sole-maintainer deadlock while blocking direct pushes.
  A separate ruleset without bypass actors always requires passing CI.
- Version tags cannot be updated or deleted after creation, and release jobs
  accept only commits reachable from protected `main`.

## License

MIT © Xenoviz
