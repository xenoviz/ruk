import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { isRecord } from "../src/types.js";

const root = fileURLToPath(new URL("..", import.meta.url));
const pkg: unknown = JSON.parse(await fs.readFile(`${root}/package.json`, "utf8"));
if (!isRecord(pkg)) throw new Error("package.json must contain an object");
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
if (typeof pkg["version"] !== "string" || !/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(pkg["version"])) {
  throw new Error(`Invalid package version ${String(pkg["version"])}`);
}
const versionSource = await fs.readFile(`${root}/src/version.ts`, "utf8");
const sourceVersion = versionSource.match(/VERSION\s*=\s*"([^"]+)"/)?.[1];
if (sourceVersion !== pkg["version"]) {
  throw new Error("src/version.ts must match package.json version");
}
if (pkg["dependencies"] || pkg["optionalDependencies"] || pkg["peerDependencies"]) {
  throw new Error("The published Ruk runtime must remain dependency-free");
}
const developmentDependencies = pkg["devDependencies"];
if (
  !isRecord(developmentDependencies) ||
  developmentDependencies["typescript"] !== "7.0.2" ||
  developmentDependencies["@types/node"] !== "22.20.1" ||
  Object.keys(developmentDependencies).length !== 2
) {
  throw new Error("Only the pinned TypeScript toolchain may be a development dependency");
}
if (pkg["packageManager"] !== "bun@1.3.14") throw new Error("packageManager must pin Bun 1.3.14");
for (const file of ["README.md", "SECURITY.md", "LICENSE", "bin/ruk.ts", "bun.lock", "tsconfig.json"]) {
  await fs.access(`${root}/${file}`);
}
process.stdout.write(`Validated ${String(pkg["name"])}@${pkg["version"]}.\n`);
