# Runtime benchmark

Ruk 0.3 ships one dependency-free Go runtime. The benchmark compares it with
the previous Node distribution as an upgrade decision record; it is not part of
the installed command and does not keep a second production supervisor.

## What to measure

Run the benchmark in Codex Cloud or on the GitHub Actions runners used for
release validation. Repeat each target at concurrency 1, 10, and 20, and record
the operating system, architecture, compiler/runtime versions, sample count,
cold-start latency, binary size, idle RSS, peak RSS, child-process count, and
PowerShell count on Windows. Use equivalent clean repository fixtures,
commands, and durations for every target. All wrappers and samples for one
target intentionally share that target's repository so the benchmark still
measures coordination and state contention without allowing a failed baseline
cleanup to corrupt the other runtime's evidence.

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
Both targets use the same readiness-gated launch schedule. The harness starts
the next wrapper only after the current wrapper exposes its expected managed
child, then waits 250 ms for its initial state mutation to settle. It rejects a
sample unless every wrapper is ready with at least half of the configured
duration left for concurrent measurement. This keeps the shared repository and
steady-state contention while avoiding an artificial synchronized startup
burst in the historical state implementation.
The default 26-second command window leaves the required half-duration overlap
at concurrency 20 on Windows without weakening the 250 ms readiness settling
period.
Measurements are machine-specific, so attach the raw JSON and runner details to
the migration pull request rather than treating one local sample as a universal
number.

## Verified Cloud result

[GitHub Actions run 31921251259](https://github.com/xenoviz/ruk/actions/runs/31921251259)
measured commit `ac283dcbeea5895a8eefffb7ec2b83dc4bcf41ab` with Node
24.14.0 and Go 1.24.6. Each cell below is the median of three 12-second
samples. The raw Linux artifact is
`runtime-benchmark-Linux-ac283dcbeea5895a8eefffb7ec2b83dc4bcf41ab`
(SHA-256 `5ff180bce006c8b8e7ce18d25d189d371f2181618019653b00848bde5103d7ec`).

| Linux x64 target | Concurrency 1 | Concurrency 10 | Concurrency 20 |
| --- | ---: | ---: | ---: |
| Previous Node median peak RSS | 56.7 MiB | 606.0 MiB | 1,293.6 MiB |
| Go median peak RSS | 12.9 MiB | 128.6 MiB | 257.6 MiB |
| Go reduction | 77.2% | 78.8% | 80.1% |

The Go runtime met the 50% Linux memory target at every concurrency level. Its
median cold start was 3 ms, compared with 47 ms for Node. The Go executable was
6.93 MiB; the previous bundled JavaScript entrypoint was 431.1 KiB and required
the external Node runtime.

The Windows comparison did not produce a valid memory artifact. Both attempts
failed in the previous Node runtime before the Go target ran. The first attempt
lost two wrappers, and the failed-job rerun lost one, to `EPERM` while replacing
the shared `state.json`. Ruk 0.3's Windows state writer has bounded retries for
this file-sharing condition, and exact-head Windows tests pass, but these runs
cannot support a Windows RSS or PowerShell-count claim. Keep the Windows
benchmark open as a release gate; do not replace the failed measurements with
partial samples.

Correctness, race safety, packaging, updater verification, and cross-platform
process cleanup remain release gates even when a memory target passes.
