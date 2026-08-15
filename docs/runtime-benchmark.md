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
zero PowerShell processes in the Go target; the legacy target's PowerShell count
remains in the raw evidence as part of the comparison. A known legacy cleanup
failure is tolerated only after that wrapper completed the full workload and
only for the historical post-exit identity-retention error. Atomic state errors
remain benchmark failures because they can occur before the workload starts.
Every early failure and every Go failure still fails the benchmark.
Windows wrapper starts are spaced by 100 ms for both targets, leaving more than
ten seconds with all 20 wrappers active while avoiding an artificial synchronized
write burst in the historical state implementation.
Measurements are machine-specific, so attach the raw JSON and runner details to
the migration pull request rather than treating one local sample as a universal
number.

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
