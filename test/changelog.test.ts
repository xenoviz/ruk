import assert from "node:assert/strict";
import test from "node:test";
import { changelogEntry } from "../scripts/lib/changelog.js";

const changelog = `# Changelog

## Unreleased

- Pending work.

## 0.4.0 - 2026-09-25

- Remove 0.2 compatibility.
- Fix a lock race.

## 0.4.0-beta.1 - 2026-09-20

- Preview.
`;

test("changelog entry returns the exact dated version section", () => {
  assert.deepEqual(changelogEntry(changelog, "0.4.0"), {
    version: "0.4.0",
    date: "2026-09-25",
    body: "- Remove 0.2 compatibility.\n- Fix a lock race.",
  });
  assert.equal(changelogEntry(changelog.replaceAll("\n", "\r\n"), "0.4.0-beta.1").body, "- Preview.");
});

test("changelog entry rejects missing, undated, and empty releases", () => {
  assert.throws(() => changelogEntry(changelog, "0.5.0"), /no dated "## 0.5.0/);
  assert.throws(() => changelogEntry("## 0.5.0\n\n- Undated.\n", "0.5.0"), /no dated/);
  assert.throws(() => changelogEntry("## 0.5.0 - 2026-10-01\n\n## 0.4.0 - 2026-09-25\n- x\n", "0.5.0"), /is empty/);
});
