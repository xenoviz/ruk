# Runtime benchmark

Ruk 0.3 ships one dependency-free Go runtime. The benchmark compares it with
the previous Node distribution as an upgrade decision record; it is not part of
the installed command and does not keep a second production supervisor.

## What to measure

Run the benchmark in Codex Cloud or on the GitHub Actions runners used for
release validation. Repeat each target at concurrency 1, 10, and 20, and record
the operating system, architecture, compiler/runtime versions, sample count,
cold-start latency, binary size, idle RSS, peak RSS, child-process count, and
PowerShell count on Windows. Use the same repository fixture, command, and
duration for every target.

The comparison target is the last TypeScript/Node release artifact. The release
target is the Go binary used by the standalone and npm platform packages. The
benchmark must exercise acquire, dependency readiness, a managed child,
activity renewal, and release; a version-only startup check is not useful.

## Release evidence

The automated migration gate is at least 50% lower median peak RSS for the
Ruk-owned wrapper processes than the Node artifact at all three concurrency
levels, together with compatible CLI and JSON behavior. Cold and idle RSS are
recorded as supporting evidence. Windows routine process inspection must launch
zero PowerShell processes. Measurements are machine-specific, so attach the raw
JSON and runner details to the migration pull request rather than treating one
local sample as a universal number.

The final release record still needs these values:

| Target | Concurrency 1 | Concurrency 10 | Concurrency 20 |
| --- | ---: | ---: | ---: |
| Previous Node artifact median peak RSS | pending | pending | pending |
| Go runtime median peak RSS | pending | pending | pending |
| Median peak RSS reduction | pending | pending | pending |
| Windows PowerShell count | pending | pending | pending |

Do not claim the memory target is met until repeated Cloud or platform runs
populate this table. Correctness, race safety, packaging, updater verification,
and cross-platform process cleanup remain release gates even when the memory
target is met.
