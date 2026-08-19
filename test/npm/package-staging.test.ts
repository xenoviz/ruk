import assert from "node:assert/strict";
import crypto from "node:crypto";
import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import test from "node:test";

import { stagePlatformPackage, stageRootPackage } from "../../scripts/npm/package.mjs";

test("platform package staging preserves safety metadata and binds the binary checksum", async (t) => {
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-platform-package-"));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const binary = path.join(temporary, "ruk.exe");
  const contents = Buffer.from("package-mode-go-binary");
  await fs.writeFile(binary, contents);
  const output = path.join(temporary, "package");

  await stagePlatformPackage({
    template: new URL("../../npm/ruk-windows-x64", import.meta.url),
    binary,
    output,
    version: "0.3.0-beta.1",
  });

  const manifest = JSON.parse(await fs.readFile(path.join(output, "package.json"), "utf8"));
  assert.equal(manifest.name, "@xenoviz/ruk-windows-x64");
  assert.equal(manifest.version, "0.3.0-beta.1");
  assert.equal(manifest.ruk.distribution, "package");
  assert.equal(manifest.ruk.binary, "native/ruk.exe");
  assert.equal(manifest.ruk.sha256, crypto.createHash("sha256").update(contents).digest("hex"));
  assert.deepEqual(await fs.readFile(path.join(output, "native", "ruk.exe")), contents);
  assert.equal(manifest.bin, undefined);
});

test("root package staging synchronizes every optional package version", async (t) => {
  const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-root-package-"));
  t.after(() => fs.rm(temporary, { recursive: true, force: true }));
  const output = path.join(temporary, "package");

  await stageRootPackage({
    template: new URL("../../npm/ruk", import.meta.url),
    scripts: new URL("../../scripts/npm", import.meta.url),
    license: new URL("../../LICENSE", import.meta.url),
    output,
    version: "0.3.0-beta.1",
  });

  const manifest = JSON.parse(await fs.readFile(path.join(output, "package.json"), "utf8"));
  assert.equal(manifest.version, "0.3.0-beta.1");
  assert.ok(Object.values(manifest.optionalDependencies).every((value) => value === "0.3.0-beta.1"));
  assert.equal(manifest.bin.ruk, "bin/ruk");
  await fs.access(path.join(output, "scripts", "npm", "install.mjs"));
  await fs.access(path.join(output, "LICENSE"));
});
