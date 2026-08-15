#!/usr/bin/env node

import { spawn } from "node:child_process";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";

function commandRunner(command, args, options = {}) {
  return new Promise((resolve, reject) => {
    const child = spawn(command, args, {
      cwd: options.cwd,
      env: options.env ?? process.env,
      stdio: ["ignore", "pipe", "pipe"],
    });
    const stdout = [];
    const stderr = [];
    child.stdout.on("data", (chunk) => stdout.push(Buffer.from(chunk)));
    child.stderr.on("data", (chunk) => stderr.push(Buffer.from(chunk)));
    child.on("error", reject);
    child.on("close", (code, signal) => resolve({
      code: code ?? 1,
      signal,
      stdout: Buffer.concat(stdout).toString("utf8"),
      stderr: Buffer.concat(stderr).toString("utf8"),
    }));
  });
}

function isRecord(value) {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}

function parseJSON(text, description) {
  try {
    return JSON.parse(text);
  } catch (error) {
    throw new Error(`npm returned invalid ${description} JSON`, { cause: error });
  }
}

function assertPackageManifest(value) {
  if (!isRecord(value) || typeof value.name !== "string" || value.name === "" || typeof value.version !== "string" || value.version === "") {
    throw new Error("publish directory package.json must contain a package name and version");
  }
  return { name: value.name, version: value.version };
}

function notFoundResult(result) {
  return result.code !== 0 && /(?:E404|404 Not Found|No matching version found|not in this registry)/i.test(`${result.stdout}\n${result.stderr}`);
}

export function parsePublishArguments(args) {
  let directory;
  let tag = "latest";
  for (let index = 0; index < args.length; index += 1) {
    const argument = args[index];
    if (argument === "--directory") {
      directory = args[index + 1];
      index += 1;
    } else if (argument?.startsWith("--directory=")) {
      directory = argument.slice("--directory=".length);
    } else if (argument === "--tag") {
      tag = args[index + 1];
      index += 1;
    } else if (argument?.startsWith("--tag=")) {
      tag = argument.slice("--tag=".length);
    } else {
      throw new Error(`Unknown publisher argument ${argument}`);
    }
  }
  if (typeof directory !== "string" || directory.trim() === "") throw new Error("--directory is required");
  if (typeof tag !== "string" || !/^(?![._])[A-Za-z0-9][A-Za-z0-9._-]*$/.test(tag)) {
    throw new Error(`invalid npm dist-tag ${String(tag)}`);
  }
  return { directory, tag };
}

export async function publishPackage({ directory, tag = "latest", run = commandRunner }) {
  if (typeof directory !== "string" || directory.trim() === "") throw new Error("publish directory is required");
  if (typeof tag !== "string" || !/^(?![._])[A-Za-z0-9][A-Za-z0-9._-]*$/.test(tag)) throw new Error(`invalid npm dist-tag ${String(tag)}`);
  const root = path.resolve(directory);
  let manifest;
  try {
    manifest = assertPackageManifest(parseJSON(await fs.readFile(path.join(root, "package.json"), "utf8"), "package manifest"));
  } catch (error) {
    if (error instanceof Error && error.message.startsWith("publish directory package.json")) throw error;
    throw new Error(`Cannot read publish package at ${root}`, { cause: error });
  }
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-npm-pack-"));
  try {
    const packed = await run("npm", ["pack", "--json", "--ignore-scripts", "--pack-destination", temporary, root]);
    if (packed.code !== 0) throw new Error(`npm pack failed: ${packed.stderr.trim() || packed.stdout.trim()}`);
    const records = parseJSON(packed.stdout, "pack");
    if (!Array.isArray(records) || records.length !== 1 || !isRecord(records[0]) || records[0].name !== manifest.name || records[0].version !== manifest.version || typeof records[0].integrity !== "string" || records[0].integrity === "") {
      throw new Error("npm pack returned metadata that does not match the package manifest");
    }
    const integrity = records[0].integrity;
    const specification = `${manifest.name}@${manifest.version}`;
    const existing = await run("npm", ["view", specification, "dist.integrity", "--json"]);
    if (existing.code === 0) {
      const registryIntegrity = parseJSON(existing.stdout, "registry integrity");
      if (typeof registryIntegrity !== "string" || registryIntegrity !== integrity) {
        throw new Error(`${specification} has different registry integrity (local ${integrity}, registry ${String(registryIntegrity)})`);
      }
      return { status: "already-published", name: manifest.name, version: manifest.version, integrity, tag };
    }
    if (!notFoundResult(existing)) {
      throw new Error(`Could not inspect registry for ${specification}: ${existing.stderr.trim() || existing.stdout.trim()}`);
    }
    const published = await run("npm", ["publish", "--access", "public", "--provenance", "--tag", tag, "--ignore-scripts", root]);
    if (published.code !== 0) throw new Error(`npm publish failed: ${published.stderr.trim() || published.stdout.trim()}`);
    return { status: "published", name: manifest.name, version: manifest.version, integrity, tag };
  } finally {
    await fs.rm(temporary, { recursive: true, force: true });
  }
}

async function main() {
  const input = parsePublishArguments(process.argv.slice(2));
  const result = await publishPackage(input);
  process.stdout.write(`${result.status === "published" ? "Published" : "Already published"} ${result.name}@${result.version} (${result.tag}).\n`);
}

if (process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    await main();
  } catch (error) {
    process.stderr.write(`npm publisher failed: ${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  }
}
