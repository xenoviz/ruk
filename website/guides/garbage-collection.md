# Garbage collection

Ruk collects only workspaces recorded in its own lifecycle state. It does not
discover or delete arbitrary Git worktrees.

## Preview first

Garbage collection is a dry run unless you pass `--apply`:

```sh
ruk gc --json
```

The default age is 1,440 minutes. Choose another cutoff with `--max-age`:

```sh
ruk gc --max-age 2880 --json
```

The `removed` array lists available or failed workspaces eligible for
collection. The `expired` array reports expired active assignments separately.

## Apply the safe plan

```sh
ruk gc --max-age 2880 --apply --json
```

Normal collection removes old available and failed records. It skips the
current workspace and does not reclaim expired active assignments.

## Force expired assignments

```sh
ruk gc --apply --force-expired --json
```

::: danger Destructive recovery
`--force-expired` terminates recorded processes, discards workspace changes,
and collects expired assignments. Expiry alone does not prove an agent has
stopped. Use this option only after confirming that recovery is safe.
:::

Ruk still limits cleanup to recorded process identities and managed workspace
paths. It does not scan for unrelated processes or orphan worktrees.
