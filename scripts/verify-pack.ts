import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { detectLibc, platformTarget } from "./npm/launcher.mjs";
import { readPackageVersion } from "./lib/package.js";
import { run } from "./lib/process.js";

interface PackedFile {
  path: string;
}

interface PackResult {
  filename: string;
  files: PackedFile[];
}

function parsePackResult(value: unknown): PackResult {
  if (!Array.isArray(value) || value.length !== 1 || value[0] === null || typeof value[0] !== "object") {
    throw new Error("npm pack returned an invalid result");
  }
  const result = value[0] as { filename?: unknown; files?: unknown };
  if (
    typeof result.filename !== "string" ||
    !Array.isArray(result.files) ||
    !result.files.every((file): file is PackedFile => file !== null && typeof file === "object" && typeof (file as { path?: unknown }).path === "string")
  ) {
    throw new Error("npm pack returned invalid file metadata");
  }
  return { filename: result.filename, files: result.files };
}

async function pack(directory: string, destination: string, environment: NodeJS.ProcessEnv): Promise<PackResult> {
  const result = await run("npm", ["pack", "--json", "--ignore-scripts", "--pack-destination", destination, directory], {
    cwd: directory,
    env: environment,
  });
  return parsePackResult(JSON.parse(result.stdout) as unknown);
}

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const version = await readPackageVersion(root);
const selected = platformTarget(process.platform, process.arch, detectLibc());
const platformDirectory = selected.packageName.slice("@xenoviz/".length);
const platformTemplate = path.join(root, "npm", platformDirectory);
const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-native-pack-"));
const npmEnvironment = { ...process.env, npm_config_cache: path.join(temporary, "npm-cache") };
const binaryName = process.platform === "win32" ? "ruk.exe" : "ruk";
const binary = path.join(temporary, binaryName);
const platformPackage = path.join(temporary, "platform-package");
const rootPackage = path.join(temporary, "root-package");

try {
  await run("go", [
    "build",
    "-trimpath",
    "-ldflags",
    `-s -w -X main.version=${version} -X main.distribution=package`,
    "-o",
    binary,
    "./cmd/ruk",
  ], { cwd: root, env: { ...process.env, CGO_ENABLED: "0" } });
  await run("node", [
    "scripts/npm/package.mjs",
    "--kind",
    "platform",
    "--version",
    version,
    "--template",
    platformTemplate,
    "--binary",
    binary,
    "--output",
    platformPackage,
  ], { cwd: root, env: npmEnvironment });
  await run("node", [
    "scripts/npm/package.mjs",
    "--kind",
    "root",
    "--version",
    version,
    "--template",
    path.join(root, "npm", "ruk"),
    "--scripts",
    path.join(root, "scripts", "npm"),
    "--license",
    path.join(root, "LICENSE"),
    "--output",
    rootPackage,
  ], { cwd: root, env: npmEnvironment });

  const platformPack = await pack(platformPackage, temporary, npmEnvironment);
  const rootPack = await pack(rootPackage, temporary, npmEnvironment);
  const platformTarball = path.join(temporary, platformPack.filename);
  const rootTarball = path.join(temporary, rootPack.filename);
  for (const required of [`native/${binaryName}`, "package.json"]) {
    if (!platformPack.files.some((file) => file.path === required)) {
      throw new Error(`Native platform package is missing ${required}`);
    }
  }
  for (const required of ["bin/ruk", "scripts/npm/install.mjs", "scripts/npm/launcher.mjs", "package.json", "LICENSE"]) {
    if (!rootPack.files.some((file) => file.path === required)) {
      throw new Error(`Native root package is missing ${required}`);
    }
  }

  const installRoot = path.join(temporary, "consumer");
  await fs.mkdir(installRoot);
  await run("npm", ["init", "-y"], { cwd: installRoot, env: npmEnvironment });
  await run("npm", ["install", "--foreground-scripts", rootTarball, platformTarball], {
    cwd: installRoot,
    env: npmEnvironment,
  });
  const executable = path.join(installRoot, "node_modules", ".bin", process.platform === "win32" ? "ruk.exe" : "ruk");
  const versionResult = await run(executable, ["--version"], { cwd: installRoot });
  if (versionResult.stdout.trim() !== version) throw new Error(`Installed native executable returned ${versionResult.stdout.trim()}, expected ${version}`);
  const helpResult = await run(executable, ["--help"], { cwd: installRoot });
  if (!helpResult.stdout.startsWith("Ruk —")) throw new Error("Installed native executable failed its help smoke test");

  const ignoredRoot = path.join(temporary, "consumer-ignore-scripts");
  await fs.mkdir(ignoredRoot);
  await run("npm", ["init", "-y"], { cwd: ignoredRoot, env: npmEnvironment });
  await run("npm", ["install", "--ignore-scripts", rootTarball, platformTarball], {
    cwd: ignoredRoot,
    env: npmEnvironment,
  });
  const packageBin = path.join(ignoredRoot, "node_modules", "@xenoviz", "ruk", "bin", "ruk");
  const ignoredCommand = await run(process.execPath, [packageBin, "--version"], {
    cwd: ignoredRoot,
    env: npmEnvironment,
  });
  if (ignoredCommand.stdout.trim() !== version) {
    throw new Error(`Ignore-scripts package command returned ${ignoredCommand.stdout.trim()}, expected ${version}`);
  }
  const placedNative = path.join(
    ignoredRoot,
    "node_modules",
    "@xenoviz",
    "ruk",
    "bin",
    process.platform === "win32" ? "ruk.exe" : "ruk",
  );
  if (process.platform === "win32") {
    await fs.access(placedNative);
  } else {
    const placed = await fs.readFile(placedNative);
    const expected = await fs.readFile(binary);
    if (!placed.equals(expected)) throw new Error("Ignore-scripts first invocation did not place the native binary");
  }
  process.stdout.write(`Verified native ${selected.target} package and executable for ${process.platform}/${process.arch}.\n`);
} finally {
  await fs.rm(temporary, { recursive: true, force: true });
}
