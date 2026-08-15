# Contributing

Changes are accepted through pull requests. Direct pushes to `main` are not
part of the project workflow.

## Before changing code

Read `docs/architecture.md`. Changes to state, locking, destructive worktree
operations, shared-store behavior, or release automation require tests covering
failure and concurrency paths, not only successful execution.

## Validation

Install exactly from the committed Bun lockfile and run every gate:

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

CI runs Go vet, unit, race, frozen-conformance, cross-compilation, native npm
installation, and platform smoke checks. It builds and exercises the native
executable on Linux, Windows, and macOS.

## Pull requests

- Keep each pull request focused on one coherent change.
- Add or update tests before changing public behavior.
- Preserve dependency-free runtime code unless the architecture clearly
  requires a reviewed dependency.
- Keep Go runtime code idiomatic and dependency-free. Preserve strict
  TypeScript settings in repository tooling and documentation code.
- Update README and architecture documentation with public or invariant changes.
- Use squash merge and a concise imperative commit title.
- Resolve all review threads before merge.
