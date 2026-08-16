# Pools, ports, and statistics

## Prewarm a pool

Prepare workspaces before starting a burst of agents:

```sh
ruk warm --count 5 --from origin/main --fetch --json
```

The count is the desired number of available workspaces. Running the same
command again creates nothing while five ready workspaces remain available.
`--fetch` is explicit because it contacts the selected Git remote.

## Reserve named ports

Request one or more ports during acquisition:

```sh
ruk acquire agent/web --port app --port inspector --json
```

The response contains a `ports` object. Commands started through `ruk run`,
`ruk exec`, or `ruk shell` also receive normalized environment variables:

```text
RUK_PORT_APP
RUK_PORT_INSPECTOR
```

Reservations are unique among active Ruk assignments on the host. Ruk probes
the operating system before recording a port but does not keep the socket open.
When upgrading from Ruk 0.2, the native runtime imports active reservations
from the legacy host registry before allocating a new port.
An unrelated process can still claim it, so applications must report bind
failures normally.

## Inspect recorded use

```sh
ruk stats --json
```

Statistics include acquisitions, workspace reuse, successful preparations,
skipped preparations, failures, total and average preparation time, active
assignments, available workspaces, and reserved ports.

Use the optional filesystem scan only when needed:

```sh
ruk stats --disk --json
```

`projectionBytes` and `linkedTargetBytes` are measured. The
`estimatedBytesAvoided` value derives from repeated symbolic-link targets and is
an estimate, not an exact filesystem allocation measurement.
