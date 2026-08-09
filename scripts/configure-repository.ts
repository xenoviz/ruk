import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { isRecord } from "../src/types.js";

const apply = process.argv.includes("--apply");
const repository = process.env["RUK_GITHUB_REPOSITORY"] ?? "xenoviz/ruk";
const token = process.env["RUK_GITHUB_ADMIN_TOKEN"] ?? process.env["GITHUB_TOKEN"];
const rulesetFiles = [
  fileURLToPath(new URL("../config/github/main-ruleset.json", import.meta.url)),
  fileURLToPath(new URL("../config/github/release-tag-ruleset.json", import.meta.url)),
];
const rulesets: Record<string, unknown>[] = [];
for (const file of rulesetFiles) {
  const ruleset: unknown = JSON.parse(await fs.readFile(file, "utf8"));
  if (!isRecord(ruleset) || typeof ruleset["name"] !== "string") {
    throw new Error(`${file} must contain a ruleset object with a name`);
  }
  rulesets.push(ruleset);
}

if (!apply) {
  process.stdout.write(`${JSON.stringify({ repository, settings: "hardened", rulesets }, null, 2)}\n`);
  process.exit(0);
}
if (!token) {
  throw new Error("RUK_GITHUB_ADMIN_TOKEN or GITHUB_TOKEN is required with --apply");
}

const headers = {
  Accept: "application/vnd.github+json",
  Authorization: `Bearer ${token}`,
  "Content-Type": "application/json",
  "X-GitHub-Api-Version": "2022-11-28",
};

async function request(path: string, options: RequestInit = {}): Promise<unknown | null> {
  const response = await fetch(`https://api.github.com${path}`, { ...options, headers });
  if (!response.ok) {
    const detail = await response.text();
    throw new Error(`GitHub ${options.method ?? "GET"} ${path} failed (${response.status}): ${detail}`);
  }
  if (response.status === 204) return null;
  return response.json();
}

await request(`/repos/${repository}`, {
  method: "PATCH",
  body: JSON.stringify({
    allow_auto_merge: true,
    allow_merge_commit: false,
    allow_rebase_merge: false,
    allow_squash_merge: true,
    delete_branch_on_merge: true,
    has_issues: true,
  }),
});

const existing = await request(`/repos/${repository}/rulesets?includes_parents=false`);
if (!Array.isArray(existing)) throw new Error("GitHub returned an invalid ruleset list");
for (const ruleset of rulesets) {
  const current = existing.find(
    (candidate): candidate is Record<string, unknown> =>
      isRecord(candidate) && candidate["name"] === ruleset["name"] && typeof candidate["id"] === "number",
  );
  const method = current ? "PUT" : "POST";
  const endpoint = current
    ? `/repos/${repository}/rulesets/${current["id"]}`
    : `/repos/${repository}/rulesets`;
  const result = await request(endpoint, { method, body: JSON.stringify(ruleset) });
  if (!isRecord(result) || typeof result["name"] !== "string" || typeof result["id"] !== "number") {
    throw new Error("GitHub returned an invalid ruleset result");
  }
  process.stdout.write(
    `${current ? "Updated" : "Created"} ruleset ${result["name"]} (${result["id"]}) for ${repository}.\n`,
  );
}
