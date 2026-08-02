import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const directories = ["bin", "src", "scripts"];
const extensions = new Set([".js", ".mjs"]);

const files = directories.flatMap((directory) =>
  fs
    .readdirSync(path.join(root, directory), { withFileTypes: true })
    .filter((entry) => entry.isFile() && extensions.has(path.extname(entry.name)))
    .map((entry) => path.join(root, directory, entry.name)),
);

for (const file of files) {
  execFileSync(process.execPath, ["--check", file], { stdio: "inherit" });
}

process.stdout.write(`Validated syntax for ${files.length} files.\n`);
