# Dashboard

`ruk ui` opens a local web dashboard that shows every Ruk workspace on your
machine and lets you renew, release, and clean them up.

```sh
ruk ui --open
```

Ruk prints an address such as `http://127.0.0.1:47213/?token=…` and serves it
until you press Ctrl+C. Without `--open`, open the printed address yourself.
Use `--port` to choose the port; the default picks a free one.

## What it shows

The sidebar lists repositories that have Ruk workspaces, plus the repository
you started `ruk ui` in. Each workspace shows its status, owner, time left on
its lease, and when it was last active. Expired leases and failed workspaces
sort to the top and are counted beside their repository.

Select a workspace to see its path, assignment ID, reserved ports, tracked
processes, dependency mode, and any recorded failure. The panel also shows the
command that performs the same action from a terminal.

The page refreshes every three seconds. Viewing it never renews a lease.

## Actions

| Workspace             | Actions                                   |
| --------------------- | ----------------------------------------- |
| Assigned              | Renew for its original lease length, release |
| Expired lease         | Renew, force release                      |
| Worktree from `create` | Remove                                   |
| Available or failed   | Removed by Cleanup                        |

**Cleanup** previews what `ruk gc` would remove in each repository. Nothing
changes until you choose **Collect**. Expired leases stay out of the plan
unless you include them, which is the same as `--force-expired`.

**Measure disk** runs `ruk stats --disk` for the selected repository. Ruk does
not scan disk usage otherwise.

Each action runs the matching Ruk command, so it follows the same rules and
fails with the same messages. See [Assignments and renewal](./assignments) and
[Garbage collection](./garbage-collection).

## Security

The dashboard listens on `127.0.0.1` only. The printed address contains a
one-time token that the page exchanges for a session cookie, and it rejects
requests from other sites and other host names. Anyone who can run programs as
your user on the same machine could still reach it, as they could run `ruk`
directly. Stop the dashboard when you are done.

## Automation

`ruk ui --json` prints one line and keeps serving:

```json
{"status":"listening","address":"127.0.0.1:47213","url":"http://127.0.0.1:47213/?token=…"}
```
