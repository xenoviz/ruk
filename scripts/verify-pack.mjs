import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "../src/process.js";

const root = fileURLToPath(new URL("..", import.meta.url));
const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-pack-"));
const npmEnvironment = {
  ...process.env,
  npm_config_cache: path.join(temporary, "npm-cache"),
};
try {
  const packed = await run("npm", ["pack", "--json", "--pack-destination", temporary], {
    cwd: root,
    env: npmEnvironment,
  });
  const [{ filename, files }] = JSON.parse(packed.stdout);
  const names = new Set(files.map((file) => file.path));
  for (const required of ["bin/ruk.js", "src/cli.js", "README.md", "LICENSE"]) {
    if (!names.has(required)) throw new Error(`Packed artifact is missing ${required}`);
  }
  for (const forbidden of [".github/workflows/release.yml", "config/github/main-ruleset.json"]) {
    if (names.has(forbidden)) throw new Error(`Packed artifact unexpectedly contains ${forbidden}`);
  }
  const installRoot = path.join(temporary, "install");
  await fs.mkdir(installRoot);
  await run("npm", ["init", "-y"], { cwd: installRoot, env: npmEnvironment });
  await run("npm", ["install", "--ignore-scripts", path.join(temporary, filename)], {
    cwd: installRoot,
    env: npmEnvironment,
  });
  const executable = path.join(installRoot, "node_modules", ".bin", process.platform === "win32" ? "ruk.cmd" : "ruk");
  const help = await run(executable, ["--help"], { cwd: installRoot });
  if (!help.stdout.startsWith("Ruk —")) throw new Error("Installed executable failed its help smoke test");
  process.stdout.write(`Verified ${filename} (${files.length} files).\n`);
} finally {
  await fs.rm(temporary, { recursive: true, force: true });
}
