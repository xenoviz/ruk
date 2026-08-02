# Contributing

Changes are accepted through pull requests. Direct pushes to `main` are not
part of the project workflow.

## Before changing code

Read `docs/architecture.md`. Changes to state, locking, destructive worktree
operations, shared-store behavior, or release automation require tests covering
failure and concurrency paths, not only successful execution.

## Validation

Run all three gates:

```bash
npm run check
npm run test:coverage
npm run pack:check
```

CI repeats these checks on the minimum and current Node releases across Linux,
Windows, and macOS.

## Pull requests

- Keep each pull request focused on one coherent change.
- Add or update tests before changing public behavior.
- Preserve dependency-free runtime code unless the architecture clearly
  requires a reviewed dependency.
- Update README and architecture documentation with public or invariant changes.
- Use squash merge and a concise imperative commit title.
- Resolve all review threads before merge.
