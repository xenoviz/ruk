import path from "node:path";
import { fileURLToPath } from "node:url";
import { assertComparisons, ConformanceHarness } from "./harness.js";
import { defaultScenarioFiles, loadScenarios } from "./scenarios.js";

const root = path.resolve(fileURLToPath(new URL("../..", import.meta.url)));
const scenarioFiles = process.argv.slice(2);
const files = scenarioFiles.length > 0 ? scenarioFiles : defaultScenarioFiles(root);
const scenarios = (await Promise.all(files.map((file) => loadScenarios(file)))).flat();
const comparisons = await new ConformanceHarness({ root }).compare(scenarios);
assertComparisons(comparisons);
process.stdout.write(`Conformance passed: ${comparisons.length} scenario(s).\n`);
