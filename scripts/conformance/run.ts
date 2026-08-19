import path from "node:path";
import { fileURLToPath } from "node:url";
import { assertGoldenComparisons, ConformanceHarness } from "./harness.js";
import {
  loadGoldenFile,
  sha256File,
  validateFixtureHashes,
  validateScenarioManifest,
  type CurrentFixtureManifest,
} from "./golden.js";
import { defaultScenarioFiles, loadScenarios, scenarioSteps } from "./scenarios.js";
import type { ConformanceScenario } from "./types.js";

const root = path.resolve(fileURLToPath(new URL("../..", import.meta.url)));
if (process.argv.length > 2) throw new Error("Frozen conformance does not accept scenario file overrides");
const files = defaultScenarioFiles(root);
const golden = await loadGoldenFile(path.join(root, "test", "conformance", "golden.json"));
await validateFixtureHashes(golden, root);
const manifests: CurrentFixtureManifest[] = [];
const scenarios: ConformanceScenario[] = [];
for (const file of files) {
  const loaded = await loadScenarios(file);
  manifests.push({
    path: path.relative(root, file).replaceAll("\\", "/"),
    sha256: await sha256File(file),
    scenarios: loaded.map((scenario) => ({
      name: scenario.name,
      stepNames: scenarioSteps(scenario).map((step) => step.name),
    })),
  });
  scenarios.push(...loaded);
}
validateScenarioManifest(golden, manifests);
if (scenarios.length !== golden.scenarioCount) throw new Error(`Expected exactly ${golden.scenarioCount} conformance scenarios, found ${scenarios.length}`);
const differences = await new ConformanceHarness({ root }).compareGolden(scenarios, golden.scenarios);
assertGoldenComparisons(differences);
process.stdout.write(`Conformance passed: ${scenarios.length} frozen scenario(s).\n`);
