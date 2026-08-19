import fs from "node:fs/promises";
import os from "node:os";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "./lib/process.js";
import { readPackageVersion } from "./lib/package.js";
import { compareVersions } from "./lib/release.js";
import { planWindowsUpdateVerification } from "./lib/release-update-verification.js";

const currentTag = process.env["RELEASE_TAG"];
const VERSION = await readPackageVersion(path.resolve(fileURLToPath(new URL("..", import.meta.url))));
if (typeof currentTag !== "string" || (currentTag !== `v${VERSION}` && currentTag !== VERSION)) {
  throw new Error(`Release tag ${String(currentTag)} does not match ${VERSION}`);
}
const response = await run(
  "gh",
  ["api", "repos/xenoviz/ruk/releases?per_page=100"],
);
const releases: unknown = JSON.parse(response.stdout);
const plan = planWindowsUpdateVerification(releases, currentTag, VERSION);
if (plan.kind === "skip") {
  process.stdout.write(plan.message);
  process.exit(0);
}

const temporary = await fs.mkdtemp(path.join(os.tmpdir(), "ruk-release-update-"));
try {
  await run(
    "gh",
    [
      "release",
      "download",
      plan.previous.tagName,
      "--repo",
      "xenoviz/ruk",
      "--pattern",
      "ruk-windows-x64.exe",
      "--dir",
      temporary,
    ],
    { stdio: "inherit" },
  );
  const executable = path.join(temporary, "ruk-windows-x64.exe");
  const before = await run(executable, ["--version"]);
  if (compareVersions(before.stdout.trim(), VERSION) >= 0) {
    throw new Error(`Expected an older executable, received ${before.stdout.trim()}`);
  }
  await run(executable, ["update"], { stdio: "inherit" });

  const deadline = Date.now() + 60_000;
  let verified = false;
  while (Date.now() < deadline) {
    try {
      const after = await run(executable, ["--version"], { allowFailure: true });
      if (after.code === 0 && after.stdout.trim() === VERSION) {
        verified = true;
        break;
      }
    } catch {
      // The helper can briefly move the executable between polling attempts.
    }
    await new Promise((resolve) => setTimeout(resolve, 250));
  }
  if (!verified) throw new Error(`Windows executable did not update to ${VERSION} within 60 seconds`);
  process.stdout.write(`Verified Windows self-update from ${before.stdout.trim()} to ${VERSION}.\n`);
} finally {
  await fs.rm(temporary, { recursive: true, force: true });
}
