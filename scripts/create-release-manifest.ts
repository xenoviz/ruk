import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { createReleaseManifest } from "./release-manifest.js";
import { VERSION } from "../src/version.js";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const artifacts = path.join(root, "artifacts");
const releaseTag = process.env["RELEASE_TAG"];
if (releaseTag !== `v${VERSION}` && releaseTag !== VERSION) {
  throw new Error(`Release tag ${String(releaseTag)} does not match ${VERSION}`);
}

const manifest = await createReleaseManifest(artifacts, VERSION);
await fs.writeFile(
  path.join(artifacts, "ruk-release.json"),
  `${JSON.stringify(manifest, null, 2)}\n`,
  { flag: "wx", mode: 0o644 },
);
process.stdout.write(`Created readiness manifest for Ruk ${VERSION}.\n`);
