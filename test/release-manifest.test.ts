import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";
import { createReleaseManifest } from "../scripts/release-manifest.js";
import { RELEASE_ASSET_NAMES } from "../scripts/lib/release.js";

async function writeReleaseFixtures(directory: string): Promise<void> {
  for (const name of RELEASE_ASSET_NAMES) {
    const content = new TextEncoder().encode(`binary:${name}`);
    const digest = crypto.createHash("sha256").update(content).digest("hex");
    await fs.writeFile(path.join(directory, name), content);
    await fs.writeFile(path.join(directory, `${name}.sha256`), `${digest}  ${name}\n`);
  }
}

test("release readiness manifest requires the exact verified asset set", async (t) => {
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-release-manifest-"));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  await writeReleaseFixtures(temporary);

  const manifest = await createReleaseManifest(temporary, "1.2.3");
  assert.equal(manifest.version, "1.2.3");
  assert.deepEqual(Object.keys(manifest.assets), [...RELEASE_ASSET_NAMES]);
  assert.equal(manifest.assets["ruk-windows-x64.exe"]?.size, "binary:ruk-windows-x64.exe".length);

  await fs.writeFile(path.join(temporary, "unexpected"), "no");
  await assert.rejects(createReleaseManifest(temporary, "1.2.3"), /Unexpected staged release file/);
  await fs.rm(path.join(temporary, "unexpected"));

  await fs.writeFile(path.join(temporary, "ruk-linux-x64.sha256"), `${"0".repeat(64)}  ruk-linux-x64\n`);
  await assert.rejects(createReleaseManifest(temporary, "1.2.3"), /checksum does not match/);

  await fs.rm(path.join(temporary, "ruk-windows-arm64.exe.sha256"));
  await assert.rejects(createReleaseManifest(temporary, "1.2.3"), /Missing staged release file/);
});
