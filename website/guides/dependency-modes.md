# Dependency modes

Every workspace keeps its own writable dependency projection. Ruk never points
multiple workspaces at one writable `node_modules` directory.

## Shared mode

Shared mode is the default for Bun 1.3.14 or newer and pnpm 10.12.1 or newer.
It keeps workspace links and package-manager metadata local while reusing
immutable package content from a global store.

```json
{
  "dependencyMode": "shared"
}
```

No configuration is required to select this default.

## Managed mode

Managed mode uses the repository's normal install layout. npm, Yarn, and custom
installers whose executable is not Bun or pnpm use it automatically because Ruk
does not provide a shared backend for them.

```json
{
  "dependencyMode": "managed"
}
```

Set it explicitly when a Bun or pnpm repository is incompatible with the
isolated shared-store layout.

::: warning Installation success is not validation
An isolated layout can expose undeclared transitive dependencies, TypeScript
resolution assumptions, or root-only `node_modules` policies. Run the full test,
type-check, build, bundler, and lifecycle-script suite before relying on shared
mode in automation.
:::

## Custom installer

Use a deterministic command when automatic package-manager detection does not
fit the repository:

```json
{
  "dependencyMode": "managed",
  "installCommand": ["npm", "ci"]
}
```

The command must be a non-empty JSON string array. Ruk includes it in the
dependency fingerprint.

## What invalidates readiness

The dependency fingerprint includes package manifests, lockfiles, package
manager configuration, patches, the install command and mode, runtime identity,
ABI, operating system, and architecture. Ordinary source changes do not trigger
a reinstall.

After changing dependency inputs inside an assigned workspace, run:

```sh
ruk sync --json
```
