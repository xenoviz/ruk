import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";
import { isRecord } from "../src/types.js";

const root = fileURLToPath(new URL("..", import.meta.url));
const ruleset: unknown = JSON.parse(
  await fs.readFile(`${root}/config/github/main-ruleset.json`, "utf8"),
);
if (!isRecord(ruleset) || !Array.isArray(ruleset["rules"]) || !ruleset["rules"].every(isRecord)) {
  throw new Error("Main ruleset has an invalid structure");
}
const rules = ruleset["rules"];
const types = new Set(rules.map((rule) => rule["type"]));
for (const required of [
  "deletion",
  "non_fast_forward",
  "required_linear_history",
  "pull_request",
  "required_status_checks",
]) {
  if (!types.has(required)) throw new Error(`Main ruleset is missing ${required}`);
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
const statusRule = rules.find((rule) => rule["type"] === "required_status_checks");
const statusParameters = statusRule?.["parameters"];
const requiredChecks = isRecord(statusParameters) ? statusParameters["required_status_checks"] : null;
if (!Array.isArray(requiredChecks) || !requiredChecks.every(isRecord)) {
  throw new Error("Main ruleset has invalid required status checks");
}
const checks = requiredChecks.map((check) => check["context"]);
if (!checks.includes("Required checks")) throw new Error("Main must require the aggregate CI check");

const workflows = [
  ".github/workflows/ci.yml",
  ".github/workflows/release.yml",
];
for (const workflow of workflows) {
  const content = await fs.readFile(`${root}/${workflow}`, "utf8");
  const mutableAction = content.match(/uses:\s+[^\s]+@(?![a-f0-9]{40}(?:\s|$))[^\s]+/i);
  if (mutableAction) throw new Error(`${workflow} uses an unpinned action: ${mutableAction[0]}`);
}
process.stdout.write("Validated repository protection and immutable workflow dependencies.\n");
