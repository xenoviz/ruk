import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";

const packageFile = fileURLToPath(new URL("../package.json", import.meta.url));
const pkg = JSON.parse(await fs.readFile(packageFile, "utf8"));
const tag = process.env.RELEASE_TAG;
if (!tag) throw new Error("RELEASE_TAG is required");
if (tag !== `v${pkg.version}`) {
  throw new Error(`Release tag ${tag} does not match package version v${pkg.version}`);
}
process.stdout.write(`Release ${tag} matches ${pkg.name}@${pkg.version}.\n`);
