import crypto from "node:crypto";
import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const VERSION = /^\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$/;

function filename(value) {
  return value instanceof URL ? fileURLToPath(value) : path.resolve(value);
}

async function readManifest(directory) {
  return JSON.parse(await fs.readFile(path.join(directory, "package.json"), "utf8"));
}

function validateVersion(version) {
  if (typeof version !== "string" || !VERSION.test(version)) {
    throw new Error(`Invalid npm package version ${String(version)}`);
  }
}

async function resetOutput(output) {
  const resolved = path.resolve(output);
  const parsed = path.parse(resolved);
  if (resolved === parsed.root || resolved === process.cwd()) {
    throw new Error(`Refusing to replace unsafe package output ${resolved}`);
  }
  await fs.rm(resolved, { recursive: true, force: true });
  await fs.mkdir(resolved, { recursive: true });
  return resolved;
}

async function writeManifest(output, manifest) {
  await fs.writeFile(path.join(output, "package.json"), `${JSON.stringify(manifest, null, 2)}\n`);
}

export async function stagePlatformPackage(options) {
  validateVersion(options.version);
  const template = filename(options.template);
  const binary = filename(options.binary);
  const output = await resetOutput(options.output);
  const manifest = await readManifest(template);
  if (manifest?.ruk?.distribution !== "package" || typeof manifest.ruk.binary !== "string") {
    throw new Error(`Platform template ${template} is missing package distribution metadata`);
  }
  const source = await fs.readFile(binary);
  if (source.length === 0) throw new Error(`Native package binary ${binary} is empty`);
  const relativeBinary = manifest.ruk.binary;
  if (path.isAbsolute(relativeBinary) || relativeBinary.split(/[\\/]+/).includes("..")) {
    throw new Error(`Platform template ${template} has an unsafe binary path`);
  }
  const destination = path.join(output, relativeBinary);
  await fs.mkdir(path.dirname(destination), { recursive: true });
  await fs.writeFile(destination, source, { mode: 0o755 });
  manifest.version = options.version;
  manifest.ruk.sha256 = crypto.createHash("sha256").update(source).digest("hex");
  manifest.repository = { type: "git", url: "git+https://github.com/xenoviz/ruk.git" };
  manifest.publishConfig = { access: "public" };
  delete manifest.bin;
  await writeManifest(output, manifest);
  return { output, manifest };
}

export async function stageRootPackage(options) {
  validateVersion(options.version);
  const template = filename(options.template);
  const output = await resetOutput(options.output);
  await fs.cp(template, output, { recursive: true });
  await fs.mkdir(path.join(output, "scripts"), { recursive: true });
  await fs.cp(filename(options.scripts), path.join(output, "scripts", "npm"), { recursive: true });
  await fs.copyFile(filename(options.license), path.join(output, "LICENSE"));
  const manifest = await readManifest(output);
  if (manifest?.ruk?.distribution !== "package" || typeof manifest.optionalDependencies !== "object") {
    throw new Error(`Root template ${template} is missing package distribution metadata`);
  }
  manifest.version = options.version;
  for (const packageName of Object.keys(manifest.optionalDependencies)) {
    manifest.optionalDependencies[packageName] = options.version;
  }
  manifest.repository = { type: "git", url: "git+https://github.com/xenoviz/ruk.git" };
  manifest.bugs = { url: "https://github.com/xenoviz/ruk/issues" };
  manifest.homepage = "https://github.com/xenoviz/ruk#readme";
  manifest.publishConfig = { access: "public" };
  await writeManifest(output, manifest);
  return { output, manifest };
}

async function main(args) {
  const values = new Map();
  for (let index = 0; index < args.length; index += 2) {
    const key = args[index];
    const value = args[index + 1];
    if (!key?.startsWith("--") || value === undefined) throw new Error("Package staging options must be --name value pairs");
    values.set(key.slice(2), value);
  }
  const kind = values.get("kind");
  const version = values.get("version");
  const output = values.get("output");
  const template = values.get("template");
  if (!kind || !version || !output || !template) throw new Error("--kind, --version, --output, and --template are required");
  if (kind === "platform") {
    const binary = values.get("binary");
    if (!binary) throw new Error("--binary is required for a platform package");
    await stagePlatformPackage({ template, binary, output, version });
  } else if (kind === "root") {
    await stageRootPackage({
      template,
      scripts: values.get("scripts") ?? "scripts/npm",
      license: values.get("license") ?? "LICENSE",
      output,
      version,
    });
  } else {
    throw new Error(`Unsupported package staging kind ${kind}`);
  }
}

const invoked = process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (invoked) {
  main(process.argv.slice(2)).catch((error) => {
    process.stderr.write(`${error instanceof Error ? error.message : String(error)}\n`);
    process.exitCode = 1;
  });
}
