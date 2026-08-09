import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { isRecord } from "../src/types.js";

const root = fileURLToPath(new URL("..", import.meta.url));
const mainRuleset: unknown = JSON.parse(
  await fs.readFile(`${root}/config/github/main-ruleset.json`, "utf8"),
);
if (
  !isRecord(mainRuleset) ||
  mainRuleset["target"] !== "branch" ||
  !Array.isArray(mainRuleset["rules"]) ||
  !mainRuleset["rules"].every(isRecord)
) {
  throw new Error("Main ruleset has an invalid structure");
}
const rules = mainRuleset["rules"];
const bypassActors = mainRuleset["bypass_actors"];
if (
  !Array.isArray(bypassActors) ||
  bypassActors.length !== 1 ||
  !isRecord(bypassActors[0]) ||
  bypassActors[0]["actor_id"] !== 5 ||
  bypassActors[0]["actor_type"] !== "RepositoryRole" ||
  bypassActors[0]["bypass_mode"] !== "pull_request"
) {
  throw new Error("Main must allow repository administrators to bypass rules only through pull requests");
}
const types = new Set(rules.map((rule) => rule["type"]));
for (const required of [
  "deletion",
  "non_fast_forward",
  "required_linear_history",
  "pull_request",
]) {
  if (!types.has(required)) throw new Error(`Main ruleset is missing ${required}`);
}
if (types.has("required_status_checks")) {
  throw new Error("Bypassable main ruleset must not contain required status checks");
}
const pullRequestRule = rules.find((rule) => rule["type"] === "pull_request");
const pullRequest = pullRequestRule?.["parameters"];
if (
  !isRecord(pullRequest) ||
  typeof pullRequest["required_approving_review_count"] !== "number" ||
  pullRequest["required_approving_review_count"] < 1 ||
  pullRequest["require_code_owner_review"] !== true
) {
  throw new Error("Main must require at least one approval and a code-owner review");
}
const requiredCiRuleset: unknown = JSON.parse(
  await fs.readFile(`${root}/config/github/required-ci-ruleset.json`, "utf8"),
);
if (
  !isRecord(requiredCiRuleset) ||
  requiredCiRuleset["target"] !== "branch" ||
  !Array.isArray(requiredCiRuleset["bypass_actors"]) ||
  requiredCiRuleset["bypass_actors"].length !== 0 ||
  !Array.isArray(requiredCiRuleset["rules"]) ||
  !requiredCiRuleset["rules"].every(isRecord)
) {
  throw new Error("Required CI ruleset must be a non-bypassable branch ruleset");
}
const statusRule = requiredCiRuleset["rules"].find(
  (rule) => rule["type"] === "required_status_checks",
);
const statusParameters = statusRule?.["parameters"];
const requiredChecks = isRecord(statusParameters) ? statusParameters["required_status_checks"] : null;
if (!Array.isArray(requiredChecks) || !requiredChecks.every(isRecord)) {
  throw new Error("Required CI ruleset has invalid status checks");
}
const checks = requiredChecks.map((check) => check["context"]);
if (!checks.includes("Required checks")) throw new Error("Required CI ruleset must require the aggregate check");

const releaseTagRuleset: unknown = JSON.parse(
  await fs.readFile(`${root}/config/github/release-tag-ruleset.json`, "utf8"),
);
if (
  !isRecord(releaseTagRuleset) ||
  releaseTagRuleset["target"] !== "tag" ||
  !Array.isArray(releaseTagRuleset["rules"]) ||
  !releaseTagRuleset["rules"].every(isRecord)
) {
  throw new Error("Release tag ruleset has an invalid structure");
}
const tagConditions = releaseTagRuleset["conditions"];
const tagRefName = isRecord(tagConditions) ? tagConditions["ref_name"] : null;
const tagIncludes = isRecord(tagRefName) ? tagRefName["include"] : null;
if (!Array.isArray(tagIncludes) || !tagIncludes.includes("refs/tags/v*")) {
  throw new Error("Release tag ruleset must target version tags");
}
const tagRuleTypes = new Set(releaseTagRuleset["rules"].map((rule) => rule["type"]));
for (const required of ["deletion", "update"]) {
  if (!tagRuleTypes.has(required)) throw new Error(`Release tag ruleset is missing ${required}`);
}

const workflows = [
  ".github/workflows/ci.yml",
  ".github/workflows/docs.yml",
  ".github/workflows/release.yml",
];
for (const workflow of workflows) {
  const content = await fs.readFile(`${root}/${workflow}`, "utf8");
  const mutableAction = content.match(/uses:\s+[^\s]+@(?![a-f0-9]{40}(?:\s|$))[^\s]+/i);
  if (mutableAction) throw new Error(`${workflow} uses an unpinned action: ${mutableAction[0]}`);
  if (/delete-asset[^\r\n]*\|\|\s*true/.test(content)) {
    throw new Error(`${workflow} ignores readiness-marker deletion failures`);
  }
}
const releaseWorkflow = await fs.readFile(`${root}/.github/workflows/release.yml`, "utf8");
if (releaseWorkflow.includes("ref: ${{ github.ref_name }}")) {
  throw new Error("Release checkouts must use the immutable triggering SHA");
}
if (!releaseWorkflow.includes("git merge-base --is-ancestor \"$GITHUB_SHA\" origin/main")) {
  throw new Error("Release workflow must require the triggering SHA to descend from main");
}

const documentationExtensions = [".css", ".json", ".md", ".svg", ".ts", ".vue"];
const generatedDocumentationDirectories = new Set(["cache", "dist"]);
async function documentationFiles(directory: string): Promise<string[]> {
  const entries = await fs.readdir(directory, { withFileTypes: true });
  const files = await Promise.all(
    entries.map(async (entry): Promise<string[]> => {
      const target = `${directory}/${entry.name}`;
      if (entry.isDirectory()) {
        return generatedDocumentationDirectories.has(entry.name) ? [] : documentationFiles(target);
      }
      return documentationExtensions.some((extension) => entry.name.endsWith(extension)) ? [target] : [];
    }),
  );
  return files.flat();
}

const literalSinhala = /[\u0d80-\u0dff]/u;
const escapedSinhala = /(?:\\u(?:0d[89a-f][0-9a-f]|\{0*d[89a-f][0-9a-f]\})|\\0{0,3}d[89a-f][0-9a-f](?=[^0-9a-f]|$))/i;
const documentation = [
  `${root}/README.md`,
  ...(await documentationFiles(`${root}/docs`)),
  ...(await documentationFiles(`${root}/website`)),
];
for (const file of documentation) {
  const content = await fs.readFile(file, "utf8");
  if (literalSinhala.test(content) || escapedSinhala.test(content)) {
    throw new Error(`${file.slice(root.length + 1)} must remain English-only`);
  }
}

process.stdout.write("Validated repository protection, English-only documentation, and immutable workflows.\n");
