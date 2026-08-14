# Assignments and renewal

An assignment gives one owner exclusive host-local use of a managed workspace.
The assignment ID is its fencing token.

## Assignment identity

Every acquisition creates a new UUID, including when Ruk reuses an available
workspace. Renew and release accept only the exact active ID.

This rule prevents delayed automation from releasing a workspace after another
agent has acquired it.

Normal and forced release preserve only dependency projections recorded by
Ruk. Other untracked and ignored files are removed before reuse. Ruk validates
the recorded projection contents during release: unchanged projections stay
warm, while modified projections are discarded and rebuilt on the next
assignment.

## Lease duration

The default time to live is 480 minutes. Set a different duration during
acquisition:

```sh
ruk acquire agent/long-task --owner agent-17 --ttl 120 --json
```

Ruk automatically renews an assignment while a managed `run`, `exec`, `shell`,
or assigned `sync` operation is active. Each operation uses a fenced lease
keeper, so concurrent commands can renew safely without removing one another's
activity record. The TTL becomes the idle window after the latest Ruk-observed
activity rather than a deadline that interrupts a running command.

Ruk intentionally does not watch file modification times. An editor changing a
file outside a Ruk-managed command is not proof that the assignment owner is
still present. For long idle editing sessions, renew explicitly:

Renewal measures a new duration from renewal time:

```sh
ruk renew <assignment-id> --ttl 120 --json
```

`ruk status --json` reports `lastActivityAt` and `autoRenewing` so agents can
distinguish an active keeper from an idle lease.

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
