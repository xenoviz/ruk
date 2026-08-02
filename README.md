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

- Node.js 22.14 or newer
- Git
- the repository's declared package manager
- Bun 1.3.14+ or pnpm 10.12.1+ when shared mode is enabled

## Install

```bash
npm install --global @xenoviz/ruk
```

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
- Node version and native module ABI;
- operating system and CPU architecture.

Changing ordinary source code does not invalidate the dependency projection.
Changing a manifest or lockfile prepares only that workspace; immutable package
content already present in the package manager store remains reusable.

## Development

```bash
npm ci
npm run check
npm run test:coverage
npm run pack:check
```

See [CONTRIBUTING.md](./CONTRIBUTING.md) and
[docs/architecture.md](./docs/architecture.md) before changing lifecycle,
locking, state, or dependency behavior.

## Security and releases

- Runtime code has no third-party npm dependencies.
- CI runs on Node 22 and 24 across Linux, Windows, and macOS.
- GitHub Actions are pinned to immutable commit SHAs.
- Releases verify tag/package parity and publish through npm trusted publishing
  with provenance.
- `main` is governed by a checked-in ruleset requiring reviewed pull requests,
  code-owner approval, linear history, resolved discussions, and passing CI.

Report vulnerabilities according to [SECURITY.md](./SECURITY.md).

## License

MIT © Xenoviz
