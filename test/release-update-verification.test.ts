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
    [release({ tag: "v0.5.0", prerelease: false })],
    "v0.5.0",
    "0.5.0",
  );
  assert.deepEqual(plan, {
    kind: "skip",
    message: "No prior ready Windows release at or after 0.4.1 exists to exercise as an upgrade source.\n",
  });
});

test("first prerelease skips a prior stable Windows executable", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.7.0-beta.1", prerelease: true }),
      release({ tag: "v0.5.2", prerelease: false }),
      release({ tag: "v0.5.1", prerelease: false }),
    ],
    "v0.7.0-beta.1",
    "0.7.0-beta.1",
  );
  assert.deepEqual(plan, {
    kind: "skip",
    message: "No prior ready Windows release exists on this prerelease channel; stable installs ignore prereleases and are not an upgrade source.\n",
  });
});

test("later stable selects the latest prior stable Windows executable", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.7.1", prerelease: false }),
      release({ tag: "v0.7.0", prerelease: false }),
      release({ tag: "v0.7.0-beta.4", prerelease: true }),
      release({ tag: "v0.5.2", prerelease: false }),
    ],
    "v0.7.1",
    "0.7.1",
  );
  assert.deepEqual(plan, { kind: "verify", previous: { tagName: "v0.7.0", version: "0.7.0" } });
});

test("later prerelease selects the prior ready Windows executable on the same channel", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.7.0-beta.2", prerelease: true }),
      release({ tag: "v0.5.2", prerelease: false }),
      release({ tag: "v0.7.0-beta.1", prerelease: true }),
    ],
    "v0.7.0-beta.2",
    "0.7.0-beta.2",
  );
  assert.deepEqual(plan, {
    kind: "verify",
    previous: { tagName: "v0.7.0-beta.1", version: "0.7.0-beta.1" },
  });
});

test("prerelease current does not use a different prerelease channel as the upgrade source", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.7.0-rc.1", prerelease: true }),
      release({ tag: "v0.7.0-beta.1", prerelease: true }),
      release({ tag: "v0.5.2", prerelease: false }),
    ],
    "v0.7.0-rc.1",
    "0.7.0-rc.1",
  );
  assert.deepEqual(plan, {
    kind: "skip",
    message: "No prior ready Windows release exists on this prerelease channel; stable installs ignore prereleases and are not an upgrade source.\n",
  });
});

test("drafts, incomplete assets, and newer tags are not upgrade sources", () => {
  const plan = planWindowsUpdateVerification(
    [
      release({ tag: "v0.5.3", draft: true, prerelease: false }),
      release({ tag: "v0.5.2", prerelease: false, assets: ["ruk-windows-x64.exe"] }),
      release({ tag: "v0.6.0", prerelease: false }),
      release({ tag: "v0.5.1", prerelease: false, assets: [...readyWindowsAssets] }),
    ],
    "v0.6.0",
    "0.6.0",
  );
  assert.deepEqual(plan, { kind: "verify", previous: { tagName: "v0.5.1", version: "0.5.1" } });
});

test("releases before the first working Windows updater are not upgrade sources", () => {
  const releases = [
    release({ tag: "v0.4.2", prerelease: false }),
    release({ tag: "v0.4.1", prerelease: false }),
    release({ tag: "v0.4.0", prerelease: false }),
    release({ tag: "v0.3.0", prerelease: false }),
  ];
  assert.deepEqual(planWindowsUpdateVerification(releases, "v0.4.1", "0.4.1"), {
    kind: "skip",
    message: "No prior ready Windows release at or after 0.4.1 exists to exercise as an upgrade source.\n",
  });
  assert.deepEqual(planWindowsUpdateVerification(releases, "v0.4.2", "0.4.2"), {
    kind: "verify",
    previous: { tagName: "v0.4.1", version: "0.4.1" },
  });
});

test("invalid release metadata fails closed", () => {
  assert.throws(
    () => planWindowsUpdateVerification({ not: "an array" }, "v0.6.0", "0.6.0"),
    /GitHub returned invalid release metadata/,
  );
});
