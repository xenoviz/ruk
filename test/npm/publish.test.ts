import assert from "node:assert/strict";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import {
  parsePublishArguments,
  publishPackage,
} from "../../scripts/npm/publish.mjs";

async function packageFixture() {
  const directory = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-publish-"));
  await fs.writeFile(
    path.join(directory, "package.json"),
    JSON.stringify({ name: "@xenoviz/ruk", version: "0.1.2" }),
  );
  return directory;
}

function packOutput(integrity: string) {
  return JSON.stringify([{ name: "@xenoviz/ruk", version: "0.1.2", filename: "xenoviz-ruk-0.1.2.tgz", integrity }]);
}

test("publisher is idempotent when registry integrity exactly matches", async (t) => {
  const directory = await packageFixture();
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const calls: Array<{ command: string; args: string[] }> = [];
  const run = async (command: string, args: string[]) => {
    calls.push({ command, args });
    if (args[0] === "pack") return { code: 0, stdout: packOutput("sha512-same"), stderr: "" };
    if (args[0] === "view") return { code: 0, stdout: JSON.stringify("sha512-same"), stderr: "" };
    throw new Error(`unexpected command ${command} ${args.join(" ")}`);
  };

  const result = await publishPackage({ directory, tag: "next", run });
  assert.equal(result.status, "already-published");
  assert.equal(calls.length, 2);
  assert.equal(calls[0]?.command, "npm");
  assert.deepEqual(calls[0]?.args.slice(0, 3), ["pack", "--json", "--ignore-scripts"]);
  assert.deepEqual(calls[1]?.args, ["view", "@xenoviz/ruk@0.1.2", "dist.integrity", "--json"]);
});

test("publisher rejects mismatched registry content without publishing", async (t) => {
  const directory = await packageFixture();
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const calls: string[][] = [];
  const run = async (_command: string, args: string[]) => {
    calls.push(args);
    if (args[0] === "pack") return { code: 0, stdout: packOutput("sha512-local"), stderr: "" };
    return { code: 0, stdout: JSON.stringify("sha512-registry"), stderr: "" };
  };

  await assert.rejects(publishPackage({ directory, run }), /different registry integrity/);
  assert.equal(calls.length, 2);
  assert.equal(calls.some((args) => args[0] === "publish"), false);
});

test("publisher publishes an absent version with exact public provenance flags", async (t) => {
  const directory = await packageFixture();
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const calls: Array<{ command: string; args: string[] }> = [];
  const run = async (command: string, args: string[]) => {
    calls.push({ command, args });
    if (args[0] === "pack") return { code: 0, stdout: packOutput("sha512-new"), stderr: "" };
    if (args[0] === "view") return { code: 1, stdout: "", stderr: "npm ERR! code E404" };
    return { code: 0, stdout: "published", stderr: "" };
  };

  const result = await publishPackage({ directory, tag: "beta", run });
  assert.equal(result.status, "published");
  assert.deepEqual(calls[2]?.args, [
    "publish",
    "--access",
    "public",
    "--provenance",
    "--tag",
    "beta",
    "--ignore-scripts",
    directory,
  ]);
});

test("publisher preserves non-not-found registry failures", async (t) => {
  const directory = await packageFixture();
  t.after(() => fs.rm(directory, { recursive: true, force: true }));
  const run = async (_command: string, args: string[]) => {
    if (args[0] === "pack") return { code: 0, stdout: packOutput("sha512-new"), stderr: "" };
    return { code: 1, stdout: "", stderr: "registry unavailable" };
  };

  await assert.rejects(publishPackage({ directory, run }), /Could not inspect registry/);
});

test("CLI argument parsing requires a directory and accepts a tag", () => {
  assert.deepEqual(parsePublishArguments(["--directory", "npm/ruk", "--tag", "next"]), {
    directory: "npm/ruk",
    tag: "next",
  });
  assert.deepEqual(parsePublishArguments(["--directory=npm/ruk"]), {
    directory: "npm/ruk",
    tag: "latest",
  });
  assert.throws(() => parsePublishArguments(["--tag", "next"]), /--directory is required/);
  assert.throws(() => parsePublishArguments(["--directory", "npm/ruk", "--tag", "bad tag"]), /invalid npm dist-tag/);
});
