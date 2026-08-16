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
levels on Linux, together with compatible CLI and JSON behavior. Cold and idle
RSS are recorded as supporting evidence. The Windows gate runs the Go target in
isolation and requires every workload to complete while routine process
inspection launches zero PowerShell processes. The JSON keeps the unavailable
Windows RAM comparison explicit instead of treating a single target as a
comparison. Every selected-target failure still fails the benchmark.
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

[GitHub Actions run 31971535905](https://github.com/xenoviz/ruk/actions/runs/31971535905)
measured commit `3acb480a97f4550e6625779f58fce0812ca47904` with Node
24.14.0 and Go 1.24.6. Each cell below is the median of three 26-second
samples. The raw Linux JSON is in
[`runtime-benchmark-Linux-3acb480a97f4550e6625779f58fce0812ca47904`](https://github.com/xenoviz/ruk/actions/runs/31971535905/artifacts/9270033046)
(artifact ID `9270033046`, SHA-256
`02f3a3aedd64f6e4ed1b9f9efb14d4442dcf0ad88f6016a2adc60c85a80475d6`).

| Linux x64 target | Concurrency 1 | Concurrency 10 | Concurrency 20 |
| --- | ---: | ---: | ---: |
| Previous Node median peak RSS | 56.8 MiB | 636.5 MiB | 1,294.5 MiB |
| Go median peak RSS | 10.4 MiB | 130.2 MiB | 261.6 MiB |
| Go reduction | 81.7% | 79.5% | 79.8% |

The Go runtime met the 50% Linux memory target at every concurrency level. Its
median cold start was 2 ms, compared with 30 ms for Node. The Go executable was
7.02 MiB; the previous bundled JavaScript entrypoint was 431.1 KiB and required
the external Node runtime.

The same run measured the Windows Go runtime independently. Its median peak RSS
was 10.7 MiB, 130.8 MiB, and 279.0 MiB at concurrency 1, 10, and 20. Median cold
start was 21 ms, every selected workload completed, and the maximum observed
PowerShell child count was zero. The raw Windows JSON is in
[`runtime-benchmark-Windows-3acb480a97f4550e6625779f58fce0812ca47904`](https://github.com/xenoviz/ruk/actions/runs/31971535905/artifacts/9270004856)
(artifact ID `9270004856`, SHA-256
`d9a86ae8f7b9e6b2423fc1e81951fd1c86312a37e7f6802cce7e9b3b0708ea16`).

The immutable Windows Node baseline remains diagnostic rather than part of the
release gate. [Run 31963842218](https://github.com/xenoviz/ruk/actions/runs/31963842218)
could not finish because the previous runtime retained its fixture lock and
timed out with `RESOURCE_BUSY`. No Windows Node-vs-Go reduction is claimed from
that partial run. The release evidence is the complete Linux comparison plus
the isolated Windows Go completion, RSS, and zero-PowerShell result.

Exact-head product CI also passed all 17 jobs in
[run 31971518651](https://github.com/xenoviz/ruk/actions/runs/31971518651).

Correctness, race safety, packaging, updater verification, and cross-platform
process cleanup remain release gates even when a memory target passes.
