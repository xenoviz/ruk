import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { readPackageJson, VERSION_PATTERN } from "./lib/package.js";
import { isRecord } from "./lib/types.js";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const pkg = await readPackageJson(root);
const expectedDevelopmentDependencies = {
  "@types/node": "22.20.1",
  typescript: "7.0.2",
  vitepress: "1.6.4",
  vue: "3.5.40",
};
const nativePackages = {
  "ruk-linux-x64": { name: "@xenoviz/ruk-linux-x64", target: "bun-linux-x64-baseline", binary: "native/ruk" },
  "ruk-linux-arm64": { name: "@xenoviz/ruk-linux-arm64", target: "bun-linux-arm64", binary: "native/ruk" },
  "ruk-linux-x64-musl": { name: "@xenoviz/ruk-linux-x64-musl", target: "bun-linux-x64-musl-baseline", binary: "native/ruk" },
  "ruk-darwin-x64": { name: "@xenoviz/ruk-darwin-x64", target: "bun-darwin-x64", binary: "native/ruk" },
  "ruk-darwin-arm64": { name: "@xenoviz/ruk-darwin-arm64", target: "bun-darwin-arm64", binary: "native/ruk" },
  "ruk-windows-x64": { name: "@xenoviz/ruk-windows-x64", target: "bun-windows-x64-baseline", binary: "native/ruk.exe" },
  "ruk-windows-arm64": { name: "@xenoviz/ruk-windows-arm64", target: "bun-windows-arm64", binary: "native/ruk.exe" },
} as const;

if (pkg["name"] !== "@xenoviz/ruk" || pkg["license"] !== "MIT" || pkg["type"] !== "module") {
  throw new Error("Root package metadata is invalid");
}
if (pkg["private"] !== true) throw new Error("Root package must be private; npm publishes the staged npm/ruk package");
if (pkg["bin"] !== undefined || pkg["files"] !== undefined || pkg["publishConfig"] !== undefined) {
  throw new Error("Root tooling package must not describe a shipped runtime artifact");
}
if (typeof pkg["version"] !== "string" || !VERSION_PATTERN.test(pkg["version"])) {
  throw new Error(`Invalid package version ${String(pkg["version"])}`);
}
if (pkg["dependencies"] || pkg["optionalDependencies"] || pkg["peerDependencies"]) {
  throw new Error("The private tooling package must not add runtime dependencies");
}
const developmentDependencies = pkg["devDependencies"];
if (!isRecord(developmentDependencies) || Object.keys(developmentDependencies).length !== Object.keys(expectedDevelopmentDependencies).length) {
  throw new Error("Development dependencies must match the pinned tooling set");
}
for (const [name, version] of Object.entries(expectedDevelopmentDependencies)) {
  if (developmentDependencies[name] !== version) throw new Error(`Development dependency ${name} must be pinned to ${version}`);
}
if (pkg["packageManager"] !== "bun@1.3.14") throw new Error("packageManager must pin Bun 1.3.14");

for (const file of [
  "README.md",
  "LICENSE",
  "bun.lock",
  "tsconfig.json",
  "npm/ruk/package.json",
  "npm/ruk/bin/ruk",
  "scripts/npm/install.mjs",
  "scripts/npm/launcher.mjs",
]) {
  await fs.access(path.join(root, file));
}

const nativeRoot = await readPackageJson(path.join(root, "npm", "ruk"));
if (
  nativeRoot["name"] !== "@xenoviz/ruk" ||
  nativeRoot["version"] !== pkg["version"] ||
  !isRecord(nativeRoot["ruk"]) ||
  nativeRoot["ruk"]["distribution"] !== "package" ||
  nativeRoot["ruk"]["binaryPath"] !== "bin/ruk"
) {
  throw new Error("npm/ruk must describe the dependency-free native package distribution");
}
if (!isRecord(nativeRoot["bin"]) || nativeRoot["bin"]["ruk"] !== "bin/ruk") {
  throw new Error("npm/ruk must expose the native launcher destination");
}
if (nativeRoot["dependencies"] !== undefined || nativeRoot["peerDependencies"] !== undefined) {
  throw new Error("npm/ruk must not add runtime or peer dependencies beyond its optional native packages");
}
if (!isRecord(nativeRoot["scripts"]) || nativeRoot["scripts"]["postinstall"] !== "node scripts/npm/install.mjs") {
  throw new Error("npm/ruk must install its native launcher through the postinstall script");
}
const optionalDependencies = nativeRoot["optionalDependencies"];
if (!isRecord(optionalDependencies) || Object.keys(optionalDependencies).length !== Object.keys(nativePackages).length) {
  throw new Error("npm/ruk must list every native platform package as an optional dependency");
}
for (const [directory, expected] of Object.entries(nativePackages)) {
  if (optionalDependencies[expected.name] !== pkg["version"]) {
    throw new Error(`npm/ruk optional dependency ${expected.name} must match the root version`);
  }
  const platform = await readPackageJson(path.join(root, "npm", directory));
  const metadata = platform["ruk"];
  const files = platform["files"];
  if (
    platform["name"] !== expected.name ||
    platform["version"] !== pkg["version"] ||
    !isRecord(metadata) ||
    metadata["distribution"] !== "package" ||
    metadata["target"] !== expected.target ||
    metadata["binary"] !== expected.binary ||
    !Array.isArray(files) ||
    files.length !== 1 ||
    files[0] !== "native" ||
    platform["bin"] !== undefined ||
    platform["dependencies"] !== undefined
  ) {
    throw new Error(`npm/${directory} must describe only its verified native binary`);
  }
}

process.stdout.write(`Validated private tooling metadata and ${String(Object.keys(nativePackages).length)} native npm templates.\n`);
