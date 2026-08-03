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
bun run test:coverage
bun run binary:check
bun run binary:cross-check
bun run pack:check
```

CI repeats the Node distribution checks on the minimum and current Node
releases across Linux, Windows, and macOS. It also builds and exercises the
native executable on every operating system.

## Pull requests

- Keep each pull request focused on one coherent change.
- Add or update tests before changing public behavior.
- Preserve dependency-free runtime code unless the architecture clearly
  requires a reviewed dependency.
- Preserve strict TypeScript settings; do not replace type errors with broad
  casts or `any`.
- Update README and architecture documentation with public or invariant changes.
- Use squash merge and a concise imperative commit title.
- Resolve all review threads before merge.
