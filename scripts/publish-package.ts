import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { run } from "../src/process.js";
import { isRecord } from "../src/types.js";
import { VERSION } from "../src/version.js";

const specification = `@xenoviz/ruk@${VERSION}`;
const existing = await run("npm", ["view", specification, "version", "--json"], {
  allowFailure: true,
});
if (existing.code === 0) {
  const published: unknown = JSON.parse(existing.stdout);
  if (published !== VERSION) {
    throw new Error(`npm returned unexpected version metadata for ${specification}`);
  }
  const registryIntegrityResult = await run(
    "npm",
    ["view", specification, "dist.integrity", "--json"],
  );
  const registryIntegrity: unknown = JSON.parse(registryIntegrityResult.stdout);
  if (typeof registryIntegrity !== "string") {
    throw new Error(`npm returned invalid integrity metadata for ${specification}`);
  }
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-publish-verify-"));
  try {
    const packed = await run(
      "npm",
      ["pack", "--json", "--ignore-scripts", "--pack-destination", temporary],
    );
    const metadata: unknown = JSON.parse(packed.stdout);
    if (
      !Array.isArray(metadata) ||
      metadata.length !== 1 ||
      !isRecord(metadata[0]) ||
      typeof metadata[0]["integrity"] !== "string"
    ) {
      throw new Error("npm pack returned invalid metadata while verifying publication");
    }
    if (metadata[0]["integrity"] !== registryIntegrity) {
      throw new Error(`${specification} is already published with different contents`);
    }
  } finally {
    await fs.rm(temporary, { recursive: true, force: true });
  }
  process.stdout.write(`${specification} is already published with matching integrity.\n`);
} else {
  const notFound = /(?:E404|404 Not Found)/i.test(`${existing.stdout}\n${existing.stderr}`);
  if (!notFound) {
    throw new Error(`Could not determine whether ${specification} is already published: ${existing.stderr.trim()}`);
  }
  await run("npm", ["publish", "--access", "public"], { stdio: "inherit" });
}
