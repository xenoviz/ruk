import fs from "node:fs/promises";
import path from "node:path";
import { run } from "../src/process.js";

const root = process.cwd();
const testDirectory = path.join(root, ".test-dist", "test");
const testFiles = (await fs.readdir(testDirectory))
  .filter((file) => file.endsWith(".test.js"))
  .sort()
  .map((file) => path.join(testDirectory, file));

if (testFiles.length === 0) {
  throw new Error(`No compiled tests found in ${testDirectory}.`);
}

const args = ["--test", "--test-concurrency=1"];
if (process.argv.includes("--coverage")) {
  args.push(
    "--experimental-test-coverage",
    "--test-coverage-lines=85",
    "--test-coverage-functions=90",
    "--test-coverage-branches=70",
    "--test-coverage-exclude=.test-dist/bin/**",
    "--test-coverage-exclude=.test-dist/test/**",
  );
}
args.push(...testFiles);

await run("node", args, {
  cwd: root,
  stdio: "inherit",
});
