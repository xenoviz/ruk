import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";

const apply = process.argv.includes("--apply");
const repository = process.env.RUK_GITHUB_REPOSITORY ?? "xenoviz/ruk";
const token = process.env.RUK_GITHUB_ADMIN_TOKEN ?? process.env.GITHUB_TOKEN;
const rulesetFile = fileURLToPath(new URL("../config/github/main-ruleset.json", import.meta.url));
const ruleset = JSON.parse(await fs.readFile(rulesetFile, "utf8"));

if (!apply) {
  process.stdout.write(`${JSON.stringify({ repository, settings: "hardened", ruleset }, null, 2)}\n`);
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

async function request(path, options = {}) {
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
const current = existing.find((candidate) => candidate.name === ruleset.name);
const method = current ? "PUT" : "POST";
const endpoint = current
  ? `/repos/${repository}/rulesets/${current.id}`
  : `/repos/${repository}/rulesets`;
const result = await request(endpoint, { method, body: JSON.stringify(ruleset) });
process.stdout.write(
  `${current ? "Updated" : "Created"} ruleset ${result.name} (${result.id}) for ${repository}.\n`,
);
