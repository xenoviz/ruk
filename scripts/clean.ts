import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const allowed = new Set(["dist", ".test-dist", "artifacts"]);
const requested = process.argv.slice(2);

if (requested.length === 0) throw new Error("Specify at least one build directory to clean");
for (const directory of requested) {
  if (!allowed.has(directory)) throw new Error(`Refusing to clean unexpected directory: ${directory}`);
  await fs.rm(path.join(root, directory), { recursive: true, force: true });
}
