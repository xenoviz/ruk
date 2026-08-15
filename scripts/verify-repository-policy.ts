import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { isRecord } from "./lib/types.js";

const root = fileURLToPath(new URL("..", import.meta.url));
function requireDefaultBranchTarget(ruleset: Record<string, unknown>, label: string): void {
  const conditions = ruleset["conditions"];
  const refName = isRecord(conditions) ? conditions["ref_name"] : null;
  const includes = isRecord(refName) ? refName["include"] : null;
  const excludes = isRecord(refName) ? refName["exclude"] : null;
  if (
    !Array.isArray(includes) ||
    includes.length !== 1 ||
    includes[0] !== "~DEFAULT_BRANCH" ||
    !Array.isArray(excludes) ||
    excludes.length !== 0
  ) {
    throw new Error(`${label} must target only the default branch`);
  }
}

const mainRuleset: unknown = JSON.parse(
  await fs.readFile(`${root}/config/github/main-ruleset.json`, "utf8"),
);
if (
  !isRecord(mainRuleset) ||
  mainRuleset["target"] !== "branch" ||
  mainRuleset["enforcement"] !== "active" ||
  !Array.isArray(mainRuleset["rules"]) ||
  !mainRuleset["rules"].every(isRecord)
) {
  throw new Error("Main ruleset has an invalid structure");
}
requireDefaultBranchTarget(mainRuleset, "Main ruleset");
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
if (rules.length !== 4 || types.size !== 4) {
  throw new Error("Main ruleset must contain only the documented protection rules");
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
  pullRequest["require_code_owner_review"] !== true ||
  pullRequest["dismiss_stale_reviews_on_push"] !== true ||
  pullRequest["require_last_push_approval"] !== true ||
  pullRequest["required_review_thread_resolution"] !== true ||
  !Array.isArray(pullRequest["allowed_merge_methods"]) ||
  pullRequest["allowed_merge_methods"].length !== 1 ||
  pullRequest["allowed_merge_methods"][0] !== "squash"
) {
  throw new Error("Main pull-request protection does not match repository policy");
}
const requiredCiRuleset: unknown = JSON.parse(
  await fs.readFile(`${root}/config/github/required-ci-ruleset.json`, "utf8"),
);
if (
  !isRecord(requiredCiRuleset) ||
  requiredCiRuleset["target"] !== "branch" ||
  requiredCiRuleset["enforcement"] !== "active" ||
  !Array.isArray(requiredCiRuleset["bypass_actors"]) ||
  requiredCiRuleset["bypass_actors"].length !== 0 ||
  !Array.isArray(requiredCiRuleset["rules"]) ||
  requiredCiRuleset["rules"].length !== 1 ||
  !requiredCiRuleset["rules"].every(isRecord)
) {
  throw new Error("Required CI ruleset must be a non-bypassable branch ruleset");
}
requireDefaultBranchTarget(requiredCiRuleset, "Required CI ruleset");
const statusRule = requiredCiRuleset["rules"].find(
  (rule) => rule["type"] === "required_status_checks",
);
const statusParameters = statusRule?.["parameters"];
const requiredChecks = isRecord(statusParameters) ? statusParameters["required_status_checks"] : null;
if (
  !isRecord(statusParameters) ||
  statusParameters["strict_required_status_checks_policy"] !== true ||
  statusParameters["do_not_enforce_on_create"] !== true ||
  !Array.isArray(requiredChecks) ||
  !requiredChecks.every(isRecord)
) {
  throw new Error("Required CI ruleset has invalid status checks");
}
const checks = requiredChecks.map((check) => check["context"]);
if (checks.length !== 1 || checks[0] !== "Required checks") {
  throw new Error("Required CI ruleset must require only the aggregate check");
}

const releaseTagRuleset: unknown = JSON.parse(
  await fs.readFile(`${root}/config/github/release-tag-ruleset.json`, "utf8"),
);
if (
  !isRecord(releaseTagRuleset) ||
  releaseTagRuleset["target"] !== "tag" ||
  releaseTagRuleset["enforcement"] !== "active" ||
  !Array.isArray(releaseTagRuleset["bypass_actors"]) ||
  releaseTagRuleset["bypass_actors"].length !== 0 ||
  !Array.isArray(releaseTagRuleset["rules"]) ||
  !releaseTagRuleset["rules"].every(isRecord)
) {
  throw new Error("Release tag ruleset has an invalid structure");
}
const tagConditions = releaseTagRuleset["conditions"];
const tagRefName = isRecord(tagConditions) ? tagConditions["ref_name"] : null;
const tagIncludes = isRecord(tagRefName) ? tagRefName["include"] : null;
const tagExcludes = isRecord(tagRefName) ? tagRefName["exclude"] : null;
if (
  !Array.isArray(tagIncludes) ||
  tagIncludes.length !== 1 ||
  tagIncludes[0] !== "refs/tags/v*" ||
  !Array.isArray(tagExcludes) ||
  tagExcludes.length !== 0
) {
  throw new Error("Release tag ruleset must target version tags");
}
const tagRuleTypes = new Set(releaseTagRuleset["rules"].map((rule) => rule["type"]));
for (const required of ["deletion", "update"]) {
  if (!tagRuleTypes.has(required)) throw new Error(`Release tag ruleset is missing ${required}`);
}
if (releaseTagRuleset["rules"].length !== 2 || tagRuleTypes.size !== 2) {
  throw new Error("Release tag ruleset must contain only update and deletion protection");
}
const tagUpdate = releaseTagRuleset["rules"].find((rule) => rule["type"] === "update");
const tagUpdateParameters = tagUpdate?.["parameters"];
if (!isRecord(tagUpdateParameters) || tagUpdateParameters["update_allows_fetch_and_merge"] !== false) {
  throw new Error("Release tag updates must remain immutable");
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

const documentationExtensions = [
  ".cjs",
  ".css",
  ".htm",
  ".html",
  ".js",
  ".json",
  ".jsx",
  ".less",
  ".md",
  ".mjs",
  ".sass",
  ".scss",
  ".svg",
  ".ts",
  ".tsx",
  ".txt",
  ".vue",
  ".xml",
  ".yaml",
  ".yml",
];
const generatedDocumentationPaths = new Set([
  `${root}/website/.vitepress/cache`,
  `${root}/website/.vitepress/dist`,
]);
async function documentationFiles(directory: string): Promise<string[]> {
  const entries = await fs.readdir(directory, { withFileTypes: true });
  const files = await Promise.all(
    entries.map(async (entry): Promise<string[]> => {
      const target = `${directory}/${entry.name}`;
      if (entry.isDirectory()) {
        return generatedDocumentationPaths.has(target) ? [] : documentationFiles(target);
      }
      const name = entry.name.toLowerCase();
      return documentationExtensions.some((extension) => name.endsWith(extension)) ? [target] : [];
    }),
  );
  return files.flat();
}

const literalSinhala = /[\u0d80-\u0dff]/u;
const escapedSinhala = /(?:\\u(?:0d[89a-f][0-9a-f]|\{0*d[89a-f][0-9a-f]\})|\\0{0,3}d[89a-f][0-9a-f](?=[^0-9a-f]|$))/i;
function hasSinhalaHtmlReference(content: string): boolean {
  for (const match of content.matchAll(/&#(?:x([0-9a-f]+)|([0-9]+));?/gi)) {
    const hexadecimal = match[1];
    const decimal = match[2];
    const codePoint = Number.parseInt(hexadecimal ?? decimal ?? "", hexadecimal ? 16 : 10);
    if (codePoint >= 0x0d80 && codePoint <= 0x0dff) return true;
  }
  return false;
}
const rootDocumentation = (await fs.readdir(root, { withFileTypes: true }))
  .filter((entry) => entry.isFile() && entry.name.toLowerCase().endsWith(".md"))
  .map((entry) => `${root}/${entry.name}`);
const documentation = [
  ...rootDocumentation,
  ...(await documentationFiles(`${root}/docs`)),
  ...(await documentationFiles(`${root}/website`)),
];
for (const file of documentation) {
  const content = await fs.readFile(file, "utf8");
  if (literalSinhala.test(content) || escapedSinhala.test(content) || hasSinhalaHtmlReference(content)) {
    throw new Error(`${file.slice(root.length + 1)} must remain English-only`);
  }
}

process.stdout.write("Validated repository protection, English-only documentation, and immutable workflows.\n");
