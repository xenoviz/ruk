import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { readPackageJson, VERSION_PATTERN } from "./lib/package.js";
import { isRecord } from "./lib/types.js";

const root = fileURLToPath(new URL("..", import.meta.url));
const pkg = await readPackageJson(root);
const required = {
  name: "@xenoviz/ruk",
  license: "MIT",
  type: "module",
};
for (const [field, expected] of Object.entries(required)) {
  if (pkg[field] !== expected) throw new Error(`package.json ${field} must be ${expected}`);
}
const bin = pkg["bin"];
if (!isRecord(bin) || bin["ruk"] !== "dist/bin/ruk.js") {
  throw new Error("package.json must expose the compiled Ruk executable");
}
const repository = pkg["repository"];
if (!isRecord(repository) || repository["url"] !== "git+https://github.com/xenoviz/ruk.git") {
  throw new Error("package.json repository must match the provenance repository");
}
if (typeof pkg["version"] !== "string" || !VERSION_PATTERN.test(pkg["version"])) {
  throw new Error(`Invalid package version ${String(pkg["version"])}`);
}
if (pkg["dependencies"] || pkg["optionalDependencies"] || pkg["peerDependencies"]) {
  throw new Error("The published Ruk runtime must remain dependency-free");
}
const developmentDependencies = pkg["devDependencies"];
if (
  !isRecord(developmentDependencies) ||
  developmentDependencies["typescript"] !== "7.0.2" ||
  developmentDependencies["@types/node"] !== "22.20.1" ||
  developmentDependencies["vitepress"] !== "1.6.4" ||
  developmentDependencies["vue"] !== "3.5.40" ||
  Object.keys(developmentDependencies).length !== 4
) {
  throw new Error("Development dependencies must match the pinned TypeScript and documentation toolchains");
}
const overrides = pkg["overrides"];
if (!isRecord(overrides) || overrides["vite"] !== "6.4.3" || Object.keys(overrides).length !== 1) {
  throw new Error("Package overrides must pin the audited Vite documentation toolchain");
}
if (pkg["packageManager"] !== "bun@1.3.14") throw new Error("packageManager must pin Bun 1.3.14");
for (const file of [
  "README.md",
  "LICENSE",
  "bin/ruk.ts",
  "bin/ruk-standalone.ts",
  "bun.lock",
  "tsconfig.json",
  "scripts/create-release-manifest.ts",
  "scripts/verify-release-update.ts",
]) {
  await fs.access(`${root}/${file}`);
}
const nativeRoot = await readPackageJson(`${root}/npm/ruk`);
if (
  nativeRoot["name"] !== "@xenoviz/ruk" ||
  nativeRoot["version"] !== pkg["version"] ||
  !isRecord(nativeRoot["ruk"]) ||
  nativeRoot["ruk"]["distribution"] !== "package" ||
  nativeRoot["ruk"]["binaryPath"] !== "bin/ruk"
) {
  throw new Error("npm/ruk must describe the dependency-free native package distribution");
}
const nativePackages = {
  "ruk-linux-x64": { name: "@xenoviz/ruk-linux-x64", target: "bun-linux-x64-baseline", binary: "native/ruk" },
  "ruk-linux-arm64": { name: "@xenoviz/ruk-linux-arm64", target: "bun-linux-arm64", binary: "native/ruk" },
  "ruk-linux-x64-musl": { name: "@xenoviz/ruk-linux-x64-musl", target: "bun-linux-x64-musl-baseline", binary: "native/ruk" },
  "ruk-darwin-x64": { name: "@xenoviz/ruk-darwin-x64", target: "bun-darwin-x64", binary: "native/ruk" },
  "ruk-darwin-arm64": { name: "@xenoviz/ruk-darwin-arm64", target: "bun-darwin-arm64", binary: "native/ruk" },
  "ruk-windows-x64": { name: "@xenoviz/ruk-windows-x64", target: "bun-windows-x64-baseline", binary: "native/ruk.exe" },
  "ruk-windows-arm64": { name: "@xenoviz/ruk-windows-arm64", target: "bun-windows-arm64", binary: "native/ruk.exe" },
} as const;
const optionalDependencies = nativeRoot["optionalDependencies"];
if (!isRecord(optionalDependencies) || Object.keys(optionalDependencies).length !== Object.keys(nativePackages).length) {
  throw new Error("npm/ruk must list every native platform package as an optional dependency");
}
for (const [directory, expected] of Object.entries(nativePackages)) {
  if (optionalDependencies[expected.name] !== pkg["version"]) {
    throw new Error(`npm/ruk optional dependency ${expected.name} must match the root version`);
  }
  const platform = await readPackageJson(`${root}/npm/${directory}`);
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
    !files.includes("native")
  ) {
    throw new Error(`npm/${directory} must describe its verified native binary`);
  }
}
process.stdout.write(`Validated ${String(pkg["name"])}@${pkg["version"]}.\n`);
