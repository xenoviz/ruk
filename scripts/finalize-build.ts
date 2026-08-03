import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const executable = path.join(root, "dist", "bin", "ruk.js");
await fs.access(executable);
await fs.chmod(executable, 0o755);
process.stdout.write("Prepared the Node.js distribution.\n");
