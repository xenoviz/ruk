import path from "node:path";
import { fileURLToPath } from "node:url";
import { assertComparisons, ConformanceHarness } from "./harness.js";
import { defaultScenarioFile, loadScenarios } from "./scenarios.js";

const root = path.resolve(fileURLToPath(new URL("../..", import.meta.url)));
const scenarioFile = process.argv[2] ?? defaultScenarioFile(root);
const scenarios = await loadScenarios(scenarioFile);
const comparisons = await new ConformanceHarness({ root }).compare(scenarios);
assertComparisons(comparisons);
process.stdout.write(`Conformance passed: ${comparisons.length} scenario(s).\n`);
