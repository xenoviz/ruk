# Ruk

Ruk creates dependency-aware Git workspaces for parallel coding agents. It is
an independent worktree manager: no Treehouse runtime or service is required.

The name comes from `රුක්`—tree. The command is pronounced “rook”.

## Why

Twenty agents working in a large monorepo should not require twenty complete
copies of every package. They also must not share one writable `node_modules`
directory: workspace symlinks can point into the wrong branch, and one install
can break every active agent.

Ruk keeps each workspace's link projection local. When explicitly enabled, it
shares only the package manager's content-addressed package store. Dependency
inputs are fingerprinted so a prepared workspace can skip unchanged installs.

## Status

Ruk is an early release with a deliberately small, tested surface:

- create, inspect, run commands in, and remove Git worktrees;
- fingerprint root and workspace manifests, lockfiles, package-manager config,
  patches, runtime, platform, architecture, and install strategy;
- serialize preparation of the same workspace;
- use managed installs safely by default;
- opt into Bun or pnpm global-store projections after repository validation.

Reusable workspace pooling, assignment IDs, process cleanup, runtime namespace
allocation, and snapshot garbage collection are planned. The current CLI does
not claim those guarantees yet.

## Requirements

- Git
- the repository's declared package manager
- Node.js 22.14 or newer when installing Ruk from npm
- Bun 1.3.14+ or pnpm 10.12.1+ when shared mode is enabled

## Install

```bash
npm install --global @xenoviz/ruk
```

The same Node.js package can be installed with Bun (the Node.js runtime
requirement still applies to this distribution):

```bash
bun install --global @xenoviz/ruk
```

GitHub Releases also provides standalone Linux, macOS, and Windows executables
for x64 and ARM64. These embed the Bun runtime, so users do not need Node.js or
Bun installed to run Ruk. Checksums are published beside every executable, and
GitHub records signed build-provenance attestations for the release binaries.

## Usage

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

Inspect or clean up:

```bash
ruk status
ruk status --json
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
ruk update --via pnpm
```

Standalone executables select the matching operating-system, architecture, and
Linux libc asset, verify its manifest-pinned SHA-256 digest, and replace the
current executable atomically. Published checksum files remain available for
manual verification. A failed post-replacement version check rolls
back on POSIX systems; Windows schedules replacement after the running process
exits. npm, Bun, pnpm, and Yarn installations delegate the exact released
version to their package manager rather than modifying managed files directly.
Use `--via` when a custom global installation layout prevents reliable
auto-detection; automation may set `RUK_UPDATE_INSTALLER` to the same values.
Ruk never performs background update checks or downloads.

## Dependency modes

Ruk uses ordinary managed installs by default. This preserves the repository's
existing dependency layout:

```json
{
  "dependencyMode": "managed"
}
```

Save configuration as `.rukrc.json` in the repository root.

After the repository's complete check and build suite passes with an isolated
layout, shared mode can be enabled:

```json
{
  "dependencyMode": "shared"
}
```

Shared Bun mode sets `BUN_INSTALL_GLOBAL_STORE=1` and uses `--linker isolated`.
Shared pnpm mode enables its global virtual store. External package contents
are shared; workspace links and package-manager metadata remain local.

Never enable shared mode merely because installation succeeds. Type checks,
builds, tests, bundlers, lifecycle scripts, and repository package policies
must pass first.

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
- runtime identity (Node or Bun), version, and native module ABI;
- operating system and CPU architecture.

Changing ordinary source code does not invalidate the dependency projection.
Changing a manifest or lockfile prepares only that workspace; immutable package
content already present in the package manager store remains reusable.

## Development

```bash
bun install --frozen-lockfile
bun run check
bun run test:coverage
bun run binary:check
bun run binary:cross-check
bun run pack:check
```

Ruk is authored in strict TypeScript. Bun is the repository toolchain and
builds the standalone executables; `tsc` emits the Node-compatible npm
distribution. The published runtime remains dependency-free.

See [CONTRIBUTING.md](./CONTRIBUTING.md) and
[docs/architecture.md](./docs/architecture.md) before changing lifecycle,
locking, state, or dependency behavior.

## Security and releases

- Published runtime code has no third-party npm dependencies.
- CI runs the compiled package on Node 22 and 24 across Linux, Windows, and
  macOS.
- CI also compiles and exercises native Bun executables on all three operating
  systems.
- GitHub Actions are pinned to immutable commit SHAs.
- Releases verify tag/package parity, publish through npm trusted publishing
  with provenance, and attach seven standalone executables with SHA-256
  checksums and signed GitHub build-provenance attestations.
- A release becomes visible to `ruk update` only after npm publication and all
  binaries, checksums, and attestations succeed; `ruk-release.json` is uploaded
  last. Later releases exercise a real previous-to-current Windows self-update.
- `main` is governed by a checked-in ruleset requiring reviewed pull requests,
  code-owner approval, linear history, resolved discussions, and passing CI.

Report vulnerabilities according to [SECURITY.md](./SECURITY.md).

## License

MIT © Xenoviz
