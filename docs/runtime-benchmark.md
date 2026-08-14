# Runtime benchmark

Ruk remains a dependency-free TypeScript application in 0.2. The runtime
benchmark measures whether a smaller supervisor implementation could justify a
future migration without maintaining a second production CLI today.

Build both current distributions and run the default three-sample comparison:

```text
bun run benchmark:runtime
```

The harness compares the compiled Node.js distribution, the standalone Bun
executable, and `experiments/go-supervisor`, a non-shipping standard-library Go
prototype. Each target launches a real long-running child. The Ruk targets use
the normal acquire, run, heartbeat, and release path; the Go target reads a
fixture state file, writes atomic heartbeats, forwards termination, and cleans
up its child.

The JSON result records the operating system, architecture, runtime versions,
sample count, binary size, cold-start latency, and idle and peak resident memory
for 1, 10, and 20 concurrent wrappers. Setup happens before each measured
wrapper phase. The default 25-second workload exercises one periodic heartbeat
in both the Ruk and Go targets. Results are machine-specific and are not
committed as release requirements. Set `RUK_BENCH_NODE` when the desired Node
executable is not named `node` on `PATH`.

For a quick harness check, reduce the sample and concurrency counts:

```text
bun scripts/benchmark-runtime.ts --samples 1 --duration 500 --concurrency 1
```

Use the default duration for comparisons. Very short processes can exit before
platform sampling tools, particularly PowerShell on Windows, observe resident
memory.

A Go migration should happen only if repeated results show a large reduction in
Ruk-owned memory and the replacement preserves CLI and JSON compatibility,
state migration, locking, process cleanup, updater trust, and cross-platform
behavior.
