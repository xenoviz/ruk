# Dependency modes

Every workspace keeps its own writable dependency projection. Ruk never points
multiple workspaces at one writable `node_modules` directory.

## Managed mode

Managed mode is the default and uses the repository's normal install layout.

```json
{
  "dependencyMode": "managed"
}
```

Choose managed mode unless the repository has passed its complete checks under
an isolated shared-store layout.

## Shared mode

Shared mode keeps workspace links and package-manager metadata local while
allowing Bun or pnpm to reuse immutable package content from a global store.

```json
{
  "dependencyMode": "shared"
}
```

Ruk currently supports shared mode with Bun 1.3.14 or newer and pnpm 10.12.1 or
newer. Unsupported managers and versions fail instead of silently falling back.

::: warning Installation success is not validation
An isolated layout can expose undeclared transitive dependencies, TypeScript
resolution assumptions, or root-only `node_modules` policies. Run the full test,
type-check, build, bundler, and lifecycle-script suite before enabling shared
mode.
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
