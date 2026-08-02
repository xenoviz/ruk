import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "../src/process.js";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const target = process.env["RUK_BINARY_TARGET"];
const defaultName = process.platform === "win32" ? "ruk.exe" : "ruk";
const output = path.resolve(
  root,
  process.env["RUK_BINARY_OUTFILE"] ?? path.join("artifacts", defaultName),
);

await fs.mkdir(path.dirname(output), { recursive: true });
const args = ["build", "--compile"];
if (target) args.push(`--target=${target}`);
args.push(path.join(root, "bin", "ruk.ts"), "--outfile", output);

await run("bun", args, { cwd: root, stdio: "inherit" });
process.stdout.write(`Built ${output}${target ? ` for ${target}` : ""}.\n`);
