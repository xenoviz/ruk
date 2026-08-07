# Assignments and renewal

An assignment gives one owner exclusive host-local use of a managed workspace.
The assignment ID is its fencing token.

## Assignment identity

Every acquisition creates a new UUID, including when Ruk reuses an available
workspace. Renew and release accept only the exact active ID.

This rule prevents delayed automation from releasing a workspace after another
agent has acquired it.

Normal and forced release preserve only dependency projections recorded by
Ruk. Other untracked and ignored files are removed before reuse. If the
fingerprint and projection remain valid, the next assignment skips installation.

## Lease duration

The default time to live is 480 minutes. Set a different duration during
acquisition:

```sh
ruk acquire agent/long-task --owner agent-17 --ttl 120 --json
```

Renewal measures a new duration from renewal time:

```sh
ruk renew <assignment-id> --ttl 120 --json
```

## Expiry is advisory

Expiry makes an assignment visible for recovery; it does not transfer ownership
or automatically reclaim the workspace. This protects a slow or disconnected
agent from destructive cleanup based only on a clock.

Inspect expired assignments with a garbage-collection dry run:

```sh
ruk gc --json
```

Forced expired-assignment collection requires explicit authority. Read
[Garbage collection](/guides/garbage-collection) before using it.

## Owner names

Pass a stable owner explicitly for automation:

```sh
ruk acquire agent/auth-flow --owner agent-17 --json
```

Without `--owner`, Ruk uses `RUK_AGENT_ID`, then `<hostname>:<pid>`. Owner names
help operators identify assignments; they do not replace the assignment fence.
