import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { changelogEntry } from "./lib/changelog.js";
import { readPackageVersion } from "./lib/package.js";

// Print the CHANGELOG.md entry for the release tag as GitHub release notes.
const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const version = await readPackageVersion(root);
const tag = process.env["RELEASE_TAG"];
if (tag !== `v${version}`) throw new Error(`Release tag ${String(tag)} does not match package version v${version}`);
const entry = changelogEntry(await fs.readFile(path.join(root, "CHANGELOG.md"), "utf8"), version);
process.stdout.write(`${entry.body}\n`);
