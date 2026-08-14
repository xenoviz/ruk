import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  commandExists,
  createBoundedIdentityProbe,
  killProcessTree,
  processIdentity,
  requireProcessIdentity,
  run,
  terminateTrackedProcess,
  trackedProcessExists,
} from "../src/process.js";

test("process identity probes batch requests and serialize subprocess work", async () => {
  let active = 0;
  let maxActive = 0;
  let probeCount = 0;
  let releaseFirst: (() => void) | undefined;
  const firstProbeBlocked = new Promise<void>((resolve) => { releaseFirst = resolve; });
  const probe = createBoundedIdentityProbe(async (pids) => {
    probeCount += 1;
    active += 1;
    maxActive = Math.max(maxActive, active);
    if (probeCount === 1) await firstProbeBlocked;
    await new Promise((resolve) => setTimeout(resolve, 5));
    active -= 1;
    return new Map(pids.map((pid) => [pid, `identity-${pid}`]));
  }, { cacheDurationMs: 1_000, maxCacheEntries: 128, maxBatchSize: 64 });

  const firstBatch = Array.from({ length: 64 }, (_, pid) => probe(pid + 1));
  await new Promise((resolve) => setTimeout(resolve, 0));
  const secondBatch = Array.from({ length: 64 }, (_, pid) => probe(pid + 65));
  releaseFirst!();
  const identities = await Promise.all([...firstBatch, ...secondBatch]);

  assert.equal(identities.length, 128);
  assert.equal(maxActive, 1);
  assert.equal(probeCount, 2);
  assert.equal(await probe(1), "identity-1");
  assert.equal(probeCount, 2);
  assert.equal(await probe(1, true), "identity-1");
  assert.equal(probeCount, 3);

  let reusedIdentity: string | null = null;
  const reused = createBoundedIdentityProbe(async (pids) =>
    new Map(pids.map((pid) => [pid, reusedIdentity]))
  );
  assert.equal(await reused(200), null);
  reusedIdentity = "reused-200";
  const fresh = reused(200, true);
  assert.deepEqual(await Promise.all([fresh, reused(200)]), ["reused-200", "reused-200"]);
});

test("process identity probe failures reject a batch and allow a later retry", async () => {
  let attempts = 0;
  const probe = createBoundedIdentityProbe(async (pids) => {
    attempts += 1;
    if (attempts === 1) throw new Error("identity lookup failed");
    return new Map(pids.map((pid) => [pid, `identity-${pid}`]));
  });

  await assert.rejects(Promise.all([probe(1), probe(2)]), /identity lookup failed/);
  assert.equal(await probe(1), "identity-1");
  assert.equal(attempts, 2);

  let missingAttempts = 0;
  const missing = createBoundedIdentityProbe(async (pids) => {
    missingAttempts += 1;
    return new Map(pids.map((pid) => [pid, null]));
  }, { cacheNull: false });
  assert.equal(await missing(3), null);
  assert.equal(await missing(3), null);
  assert.equal(missingAttempts, 2);
});

test("a process identity subprocess does not recursively identify itself", async (t) => {
  if (process.platform === "win32") return t.skip("the bounded PowerShell path is covered on Windows CI");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-identity-probe-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const countFile = path.join(root, "count");
  const ps = path.join(root, "ps");
  await fs.writeFile(ps, "#!/bin/sh\nprintf '1\\n' >> \"$RUK_PROBE_COUNT\"\nprintf 'Fri Aug 14 12:00:00 2026\\n'\n");
  await fs.chmod(ps, 0o755);
  const originalPath = process.env["PATH"];
  const originalCount = process.env["RUK_PROBE_COUNT"];
  process.env["PATH"] = root;
  process.env["RUK_PROBE_COUNT"] = countFile;
  try {
    assert.equal(await processIdentity(2_000_000_000 - process.pid), "Fri Aug 14 12:00:00 2026");
  } finally {
    if (originalPath === undefined) delete process.env["PATH"];
    else process.env["PATH"] = originalPath;
    if (originalCount === undefined) delete process.env["RUK_PROBE_COUNT"];
    else process.env["RUK_PROBE_COUNT"] = originalCount;
  }
  assert.equal((await fs.readFile(countFile, "utf8")).trim(), "1");
});

test("process runner captures output and preserves non-zero results when requested", async () => {
  const success = await run(process.execPath, ["-e", "process.stdout.write('ok')"]);
  assert.equal(success.stdout, "ok");
  const failure = await run(process.execPath, ["-e", "process.stderr.write('bad');process.exit(7)"], {
    allowFailure: true,
  });
  assert.equal(failure.code, 7);
  assert.equal(failure.stderr, "bad");
  await assert.rejects(
    run(process.execPath, ["-e", "process.stderr.write('bad');process.exit(7)"]),
    /exit code 7: bad/,
  );
});

test("process runner terminates a managed child when its abort signal fires", async () => {
  const controller = new AbortController();
  const running = run(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
    detached: process.platform !== "win32",
    signal: controller.signal,
    onSpawn: () => controller.abort(new Error("heartbeat lost")),
  });
  await assert.rejects(running, /heartbeat lost|cannot be released safely/);
});

test("process runner fails closed when attached abort cleanup cannot inspect descendants", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX process enumeration is required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-abort-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const ps = path.join(root, "ps");
  await fs.writeFile(ps, "#!/bin/sh\nexit 1\n");
  await fs.chmod(ps, 0o755);
  const originalPath = process.env["PATH"];
  process.env["PATH"] = root;
  const controller = new AbortController();
  try {
    const running = run(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
      signal: controller.signal,
      onSpawn: () => controller.abort(new Error("heartbeat lost")),
    });
    await assert.rejects(running, /cannot be released safely/);
  } finally {
    if (originalPath === undefined) delete process.env["PATH"];
    else process.env["PATH"] = originalPath;
  }
});

test("process runner retains an unverified detached group after abort", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX process groups are required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-detached-abort-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const ps = path.join(root, "ps");
  await fs.writeFile(ps, "#!/bin/sh\nexit 1\n");
  await fs.chmod(ps, 0o755);
  const originalPath = process.env["PATH"];
  process.env["PATH"] = root;
  const controller = new AbortController();
  let childPid = 0;
  try {
    await assert.rejects(
      run(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
        detached: true,
        signal: controller.signal,
        onSpawn: (pid) => {
          childPid = pid;
          controller.abort(new Error("heartbeat lost"));
        },
      }),
      /cannot be released safely/,
    );
  } finally {
    if (originalPath === undefined) delete process.env["PATH"];
    else process.env["PATH"] = originalPath;
    if (childPid > 0) {
      try { process.kill(-childPid, "SIGKILL"); } catch {}
    }
  }
});

test("process runner does not signal a detached PID after its identity changes", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX process groups are required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-reused-abort-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const countFile = path.join(root, "count");
  const ps = path.join(root, "ps");
  await fs.writeFile(
    ps,
    "#!/bin/sh\ncount=0\nif [ -f \"$RUK_PROBE_COUNT\" ]; then read count < \"$RUK_PROBE_COUNT\"; fi\ncount=$((count+1))\nprintf '%s' \"$count\" > \"$RUK_PROBE_COUNT\"\nif [ \"$count\" -eq 1 ]; then printf 'Mon Jan  1 00:00:00 2026\\n'; else printf 'Tue Jan  2 00:00:00 2026\\n'; fi\n",
  );
  await fs.chmod(ps, 0o755);
  const originalPath = process.env["PATH"];
  const originalCount = process.env["RUK_PROBE_COUNT"];
  process.env["PATH"] = `${root}${path.delimiter}${originalPath ?? ""}`;
  process.env["RUK_PROBE_COUNT"] = countFile;
  const controller = new AbortController();
  let childPid = 0;
  try {
    await assert.rejects(
      run(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
        detached: true,
        signal: controller.signal,
        onSpawn: async (pid) => {
          childPid = pid;
          for (let attempt = 0; attempt < 100; attempt += 1) {
            try {
              if (Number(await fs.readFile(countFile, "utf8")) >= 1) break;
            } catch {}
            await new Promise((resolve) => setTimeout(resolve, 5));
          }
          controller.abort(new Error("heartbeat lost"));
        },
      }),
      /cannot be released safely/,
    );
  } finally {
    if (originalPath === undefined) delete process.env["PATH"];
    else process.env["PATH"] = originalPath;
    if (originalCount === undefined) delete process.env["RUK_PROBE_COUNT"];
    else process.env["RUK_PROBE_COUNT"] = originalCount;
    if (childPid > 0) {
      try { process.kill(-childPid, "SIGKILL"); } catch {}
    }
  }
});

test("command detection recognizes the active Node executable", async () => {
  assert.equal(await commandExists(process.execPath), true);
  assert.equal(await commandExists(path.join(os.tmpdir(), "ruk-command-that-does-not-exist")), false);
});

test("missing process identity fails closed", async () => {
  assert.equal(await processIdentity(0), null);
  await assert.rejects(
    requireProcessIdentity(42, async () => null, async () => true),
    /cannot be released safely/,
  );
  await assert.rejects(
    requireProcessIdentity(42, async () => null, async () => { throw new Error("unavailable"); }),
    /cannot be released safely/,
  );
  assert.equal(await requireProcessIdentity(42, async () => null, async () => false), null);
  await assert.rejects(
    trackedProcessExists({ pid: 42, startedAt: "original" }, async () => "reused", async () => true),
    /cannot be released safely/,
  );
  assert.equal(await trackedProcessExists({ pid: 999_999, startedAt: "missing" }), false);
  assert.equal(await terminateTrackedProcess({ pid: process.pid, startedAt: "wrong" }), false);
  assert.equal(await terminateTrackedProcess({ pid: 999_999, startedAt: "missing" }), false);
  await assert.rejects(killProcessTree(0), /Refusing to terminate invalid process/);
});

test("process runner executes Windows command shims without shell injection", async (t) => {
  if (process.platform !== "win32") return t.skip("Windows command shims are Windows-specific");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-command-shim-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  await fs.writeFile(
    path.join(root, "ruk-echo.cmd"),
    '@echo off\r\nnode -e "process.stdout.write(process.argv[1])" "%~1"\r\n',
  );
  const result = await run("ruk-echo", ["safe&echo injected"], {
    env: { ...process.env, PATH: `${root}${path.delimiter}${process.env["PATH"] ?? ""}` },
  });
  assert.equal(result.stdout, "safe&echo injected");
});

test("process runner reports and cleans up a tracked process group", async () => {
  let pid = 0;
  let identity: string | null = null;
  const running = run(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
    detached: true,
    allowFailure: true,
    onSpawn: async (childPid) => {
      pid = childPid;
      identity = await processIdentity(childPid);
    },
  });
  while (!identity) await new Promise((resolve) => setTimeout(resolve, 10));
  await assert.rejects(killProcessTree(pid, true, "wrong-identity"), /reused process ID/);
  assert.equal(await killProcessTree(pid, true, identity), true);
  const result = await running;
  assert.notEqual(result.code, 0);
});

test("process runner terminates a child when tracking registration fails", async () => {
  await assert.rejects(
    run(process.execPath, ["-e", "setInterval(() => {}, 1000)"], {
      detached: true,
      onSpawn: () => { throw new Error("tracking failed"); },
    }),
    /tracking failed/,
  );
});

test("process runner retains a detached group when its leader cannot be identity-fenced", async (t) => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-detached-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const workerFile = path.join(root, "worker.pid");
  const survivedFile = path.join(root, "survived");
  if (process.platform !== "win32") {
    const ps = path.join(root, "ps");
    await fs.writeFile(ps, "#!/bin/sh\nexit 1\n");
    await fs.chmod(ps, 0o755);
  }
  const workerScript = process.platform === "win32"
    ? "setInterval(()=>{},1000)"
    : `const fs=require('node:fs');setTimeout(()=>fs.writeFileSync(${JSON.stringify(survivedFile)},'1'),500);setInterval(()=>{},1000)`;
  let launcherPid = 0;
  let workerPid = 0;
  const originalPath = process.env["PATH"];
  if (process.platform !== "win32") process.env["PATH"] = root;
  try {
    await assert.rejects(
      run(
        process.execPath,
        [
          "-e",
          `const{spawn}=require('node:child_process');const fs=require('node:fs');const child=spawn(process.execPath,['-e',${JSON.stringify(workerScript)}],{stdio:'ignore',detached:process.platform==='win32'});child.unref();fs.writeFileSync(${JSON.stringify(workerFile)},String(child.pid))`,
        ],
        {
          detached: true,
          onSpawn: async (pid) => {
            launcherPid = pid;
            for (let attempt = 0; attempt < 100 && workerPid === 0; attempt += 1) {
              try { workerPid = Number(await fs.readFile(workerFile, "utf8")); } catch {}
              await new Promise((resolve) => setTimeout(resolve, 10));
            }
            if (process.platform === "win32") {
              for (let attempt = 0; attempt < 100 && await processIdentity(pid); attempt += 1) {
                await new Promise((resolve) => setTimeout(resolve, 10));
              }
            }
            throw new Error("tracking failed");
          },
        },
      ),
      /cannot be released safely/,
    );
  } finally {
    if (process.platform !== "win32") {
      if (originalPath === undefined) delete process.env["PATH"];
      else process.env["PATH"] = originalPath;
    }
  }
  assert.ok(workerPid > 0);
  if (process.platform === "win32") {
    const identity = await processIdentity(workerPid);
    assert.ok(identity);
    await killProcessTree(workerPid, true, identity);
  } else {
    await new Promise((resolve) => setTimeout(resolve, 750));
    await fs.access(survivedFile);
    try { process.kill(-launcherPid, "SIGKILL"); } catch {}
  }
});

test("process runner terminates an attached process tree when tracking registration fails", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX attached process cleanup is required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-attached-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const childFile = path.join(root, "child.pid");
  let childPid = 0;
  await assert.rejects(
    run(
      process.execPath,
      [
        "-e",
        `const {spawn}=require('node:child_process');const fs=require('node:fs');const child=spawn(process.execPath,['-e','setInterval(()=>{},1000)'],{stdio:'ignore'});fs.writeFileSync(${JSON.stringify(childFile)},String(child.pid));setInterval(()=>{},1000)`,
      ],
      {
        onSpawn: async () => {
          for (let attempt = 0; attempt < 100; attempt += 1) {
            try {
              childPid = Number(await fs.readFile(childFile, "utf8"));
              break;
            } catch {
              await new Promise((resolve) => setTimeout(resolve, 10));
            }
          }
          throw new Error("tracking failed");
        },
      },
    ),
    /tracking failed/,
  );
  assert.ok(childPid > 0);
  const childIsActive = async () => {
    const status = await run("ps", ["-o", "stat=", "-p", String(childPid)], { allowFailure: true });
    return status.code === 0 && status.stdout.trim() !== "" && !status.stdout.trim().startsWith("Z");
  };
  for (let attempt = 0; attempt < 100 && await childIsActive(); attempt += 1) {
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.equal(await childIsActive(), false);
});

test("Windows process identity inspection confirms the current process", async (t) => {
  if (process.platform !== "win32") return t.skip("Windows identity inspection is required");
  assert.match((await processIdentity(process.pid)) ?? "", /^\d+$/);
});

test("POSIX session inspection failures fail closed", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX process enumeration is required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-ps-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const ps = path.join(root, "ps");
  await fs.writeFile(ps, "#!/bin/sh\nexit 1\n");
  await fs.chmod(ps, 0o755);
  const originalPath = process.env["PATH"];
  process.env["PATH"] = root;
  try {
    await assert.rejects(
      trackedProcessExists({
        pid: 999_999,
        sessionId: 999_999,
        sessionStartedAt: "missing",
        startedAt: "missing",
      }),
      /Could not enumerate POSIX processes/,
    );
  } finally {
    if (originalPath === undefined) delete process.env["PATH"];
    else process.env["PATH"] = originalPath;
  }
});

test("leaderless POSIX sessions fail closed before termination", async (t) => {
  if (process.platform !== "linux") return t.skip("Linux session IDs are required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-session-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const ps = path.join(root, "ps");
  await fs.writeFile(ps, "#!/bin/sh\nif [ \"$1\" = \"-e\" ]; then printf '424243 424242 S\\n'; exit 0; fi\nexit 1\n");
  await fs.chmod(ps, 0o755);
  const originalPath = process.env["PATH"];
  process.env["PATH"] = root;
  try {
    await assert.rejects(
      terminateTrackedProcess({
        pid: 424_242,
        sessionId: 424_242,
        sessionStartedAt: "original-session",
        startedAt: "original-session",
      }, true),
      /cannot be released safely/,
    );
  } finally {
    if (originalPath === undefined) delete process.env["PATH"];
    else process.env["PATH"] = originalPath;
  }
});

test("leaderless detached groups fail closed before termination", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX process groups are required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-group-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const ps = path.join(root, "ps");
  await fs.writeFile(ps, "#!/bin/sh\nif [ \"$1\" = \"-e\" ]; then printf '424243 1 424242 S\\n'; exit 0; fi\nexit 1\n");
  await fs.chmod(ps, 0o755);
  const originalPath = process.env["PATH"];
  process.env["PATH"] = root;
  try {
    await assert.rejects(
      terminateTrackedProcess({
        pid: 424_242,
        groupId: 424_242,
        startedAt: "original-group",
      }, true),
      /cannot be released safely/,
    );
  } finally {
    if (originalPath === undefined) delete process.env["PATH"];
    else process.env["PATH"] = originalPath;
  }
});
