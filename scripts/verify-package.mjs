import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const pkg = JSON.parse(await fs.readFile(`${root}/package.json`, "utf8"));
const required = {
  name: "@xenoviz/ruk",
  license: "MIT",
  type: "module",
};
for (const [field, expected] of Object.entries(required)) {
  if (pkg[field] !== expected) throw new Error(`package.json ${field} must be ${expected}`);
}
if (pkg.bin?.ruk !== "bin/ruk.js") throw new Error("package.json must expose the ruk executable");
if (pkg.repository?.url !== "git+https://github.com/xenoviz/ruk.git") {
  throw new Error("package.json repository must match the provenance repository");
}
if (!/^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?$/.test(pkg.version)) {
  throw new Error(`Invalid package version ${pkg.version}`);
}
if (pkg.dependencies || pkg.devDependencies || pkg.optionalDependencies) {
  throw new Error("Ruk must remain dependency-free unless a reviewed architecture change requires otherwise");
}
for (const file of ["README.md", "SECURITY.md", "LICENSE", "bin/ruk.js"]) {
  await fs.access(`${root}/${file}`);
}
process.stdout.write(`Validated ${pkg.name}@${pkg.version}.\n`);
