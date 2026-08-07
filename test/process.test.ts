import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import {
  commandExists,
  killProcessTree,
  processIdentity,
  requireProcessIdentity,
  run,
  terminateTrackedProcess,
  trackedProcessExists,
} from "../src/process.js";

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

test("process runner terminates a detached group after its leader exits before registration fails", async (t) => {
  if (process.platform === "win32") return t.skip("POSIX process groups are required");
  const root = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-process-detached-"));
  t.after(() => fs.rm(root, { recursive: true, force: true }));
  const workerFile = path.join(root, "worker.pid");
  const survivedFile = path.join(root, "survived");
  const ps = path.join(root, "ps");
  await fs.writeFile(ps, "#!/bin/sh\nexit 1\n");
  await fs.chmod(ps, 0o755);
  const workerScript = `const fs=require('node:fs');setTimeout(()=>fs.writeFileSync(${JSON.stringify(survivedFile)},'1'),500);setInterval(()=>{},1000)`;
  let workerPid = 0;
  const originalPath = process.env["PATH"];
  process.env["PATH"] = root;
  try {
    await assert.rejects(
      run(
        process.execPath,
        [
          "-e",
          `const{spawn}=require('node:child_process');const fs=require('node:fs');const child=spawn(process.execPath,['-e',${JSON.stringify(workerScript)}],{stdio:'ignore'});fs.writeFileSync(${JSON.stringify(workerFile)},String(child.pid))`,
        ],
        {
          detached: true,
          onSpawn: async () => {
            for (let attempt = 0; attempt < 100 && workerPid === 0; attempt += 1) {
              try { workerPid = Number(await fs.readFile(workerFile, "utf8")); } catch {}
              await new Promise((resolve) => setTimeout(resolve, 10));
            }
            throw new Error("tracking failed");
          },
        },
      ),
      /tracking failed/,
    );
  } finally {
    if (originalPath === undefined) delete process.env["PATH"];
    else process.env["PATH"] = originalPath;
  }
  assert.ok(workerPid > 0);
  await new Promise((resolve) => setTimeout(resolve, 750));
  await assert.rejects(fs.access(survivedFile), { code: "ENOENT" });
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
