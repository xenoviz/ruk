import fs from "node:fs/promises";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const ruleset = JSON.parse(await fs.readFile(`${root}/config/github/main-ruleset.json`, "utf8"));
const types = new Set(ruleset.rules.map((rule) => rule.type));
for (const required of [
  "deletion",
  "non_fast_forward",
  "required_linear_history",
  "pull_request",
  "required_status_checks",
]) {
  if (!types.has(required)) throw new Error(`Main ruleset is missing ${required}`);
}
const pullRequest = ruleset.rules.find((rule) => rule.type === "pull_request").parameters;
if (pullRequest.required_approving_review_count < 1 || !pullRequest.require_code_owner_review) {
  throw new Error("Main must require at least one approval and a code-owner review");
}
const checks = ruleset.rules
  .find((rule) => rule.type === "required_status_checks")
  .parameters.required_status_checks.map((check) => check.context);
if (!checks.includes("Required checks")) throw new Error("Main must require the aggregate CI check");

const workflows = [".github/workflows/ci.yml", ".github/workflows/release.yml"];
for (const workflow of workflows) {
  const content = await fs.readFile(`${root}/${workflow}`, "utf8");
  const mutableAction = content.match(/uses:\s+[^\s]+@(?![a-f0-9]{40}(?:\s|$))[^\s]+/i);
  if (mutableAction) throw new Error(`${workflow} uses an unpinned action: ${mutableAction[0]}`);
}
process.stdout.write("Validated repository protection and immutable workflow dependencies.\n");
