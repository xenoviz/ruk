import assert from "node:assert/strict";
import test from "node:test";
import { planWindowsUpdateVerification } from "../scripts/lib/release-update-verification.js";

function release(options: {
  tag: string;
  draft?: boolean;
  prerelease?: boolean;
  assets?: readonly string[];
}): Record<string, unknown> {
  return {
    tag_name: options.tag,
    draft: options.draft ?? false,
    prerelease: options.prerelease ?? options.tag.includes("-"),
    assets: (options.assets ?? ["ruk-release.json", "ruk-windows-x64.exe"]).map((name) => ({ name })),
  };
}

const readyWindowsAssets = ["ruk-release.json", "ruk-windows-x64.exe"] as const;

test("first stable release skips because there is no upgrade source", () => {
  const plan = planWindowsUpdateVerification(
    [release({ tag: "v0.1.0", prerelease: false })],
    "v0.1.0",
    "0.1.0",
  );
  assert.deepEqual(plan, {
    kind: "skip",
    message: "No prior ready Windows release exists; the first release has no upgrade source.\n",
  });
});

test("first prerelease skips a prior stable Windows executable", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.3.0-beta.1", prerelease: true }),
      release({ tag: "v0.1.2", prerelease: false }),
      release({ tag: "v0.1.1", prerelease: false }),
    ],
    "v0.3.0-beta.1",
    "0.3.0-beta.1",
  );
  assert.deepEqual(plan, {
    kind: "skip",
    message: "No prior ready Windows release exists on this prerelease channel; stable installs ignore prereleases and are not an upgrade source.\n",
  });
});

test("first Go-native stable skips TypeScript-era Windows executables", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.3.0", prerelease: false }),
      release({ tag: "v0.3.0-beta.1", prerelease: true }),
      release({ tag: "v0.1.1", prerelease: false }),
      release({ tag: "v0.1.2", prerelease: false }),
    ],
    "v0.3.0",
    "0.3.0",
  );
  assert.deepEqual(plan, {
    kind: "skip",
    message: "No prior ready Windows release exists; the first release has no upgrade source.\n",
  });
});

test("later Go-native stable selects the latest prior Go-native Windows executable", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.3.1", prerelease: false }),
      release({ tag: "v0.3.0", prerelease: false }),
      release({ tag: "v0.3.0-beta.4", prerelease: true }),
      release({ tag: "v0.1.2", prerelease: false }),
    ],
    "v0.3.1",
    "0.3.1",
  );
  assert.deepEqual(plan, { kind: "verify", previous: { tagName: "v0.3.0", version: "0.3.0" } });
});

test("later prerelease selects the prior ready Windows executable on the same channel", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.3.0-beta.2", prerelease: true }),
      release({ tag: "v0.1.2", prerelease: false }),
      release({ tag: "v0.3.0-beta.1", prerelease: true }),
    ],
    "v0.3.0-beta.2",
    "0.3.0-beta.2",
  );
  assert.deepEqual(plan, {
    kind: "verify",
    previous: { tagName: "v0.3.0-beta.1", version: "0.3.0-beta.1" },
  });
});

test("prerelease current does not use a different prerelease channel as the upgrade source", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.3.0-rc.1", prerelease: true }),
      release({ tag: "v0.3.0-beta.1", prerelease: true }),
      release({ tag: "v0.1.2", prerelease: false }),
    ],
    "v0.3.0-rc.1",
    "0.3.0-rc.1",
  );
  assert.deepEqual(plan, {
    kind: "skip",
    message: "No prior ready Windows release exists on this prerelease channel; stable installs ignore prereleases and are not an upgrade source.\n",
  });
});

test("drafts, incomplete assets, and newer tags are not upgrade sources", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.1.3", draft: true, prerelease: false }),
      release({ tag: "v0.1.2", prerelease: false, assets: ["ruk-windows-x64.exe"] }),
      release({ tag: "v0.2.0", prerelease: false }),
      release({ tag: "v0.1.1", prerelease: false, assets: [...readyWindowsAssets] }),
    ],
    "v0.2.0",
    "0.2.0",
  );
  assert.deepEqual(plan, { kind: "verify", previous: { tagName: "v0.1.1", version: "0.1.1" } });
});

test("invalid release metadata fails closed", () => {
  assert.throws(
    () => planWindowsUpdateVerification({ not: "an array" }, "v0.2.0", "0.2.0"),
    /GitHub returned invalid release metadata/,
  );
});
