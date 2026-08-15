import fs from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { run } from "../src/process.js";

const root = path.resolve(fileURLToPath(new URL("..", import.meta.url)));
const packageJSON = JSON.parse(await fs.readFile(path.join(root, "package.json"), "utf8")) as { version?: unknown };
if (typeof packageJSON.version !== "string" || packageJSON.version.length === 0) {
  throw new Error("package.json version must be a nonempty string");
}
const version = process.env["RUK_VERSION"] ?? packageJSON.version;
const distribution = process.env["RUK_DISTRIBUTION"] ?? "standalone";
const targetName = process.env["RUK_BINARY_TARGET"];

type GoTarget = {
  readonly goos: string;
  readonly goarch: string;
  readonly windows: boolean;
};

const targets: Readonly<Record<string, GoTarget>> = {
  "bun-linux-x64-baseline": { goos: "linux", goarch: "amd64", windows: false },
  "bun-linux-arm64": { goos: "linux", goarch: "arm64", windows: false },
  "bun-linux-x64-musl-baseline": { goos: "linux", goarch: "amd64", windows: false },
  "bun-darwin-x64": { goos: "darwin", goarch: "amd64", windows: false },
  "bun-darwin-arm64": { goos: "darwin", goarch: "arm64", windows: false },
  "bun-windows-x64-baseline": { goos: "windows", goarch: "amd64", windows: true },
  "bun-windows-arm64": { goos: "windows", goarch: "arm64", windows: true },
};

if (version.trim() === "") throw new Error("RUK_VERSION must not be empty");
if (distribution.trim() === "") throw new Error("RUK_DISTRIBUTION must not be empty");

const target = targetName === undefined ? undefined : targets[targetName];
if (targetName !== undefined && target === undefined) {
  throw new Error(`Unsupported RUK_BINARY_TARGET ${targetName}`);
}

const defaultName = target?.windows || (!target && process.platform === "win32") ? "ruk.exe" : "ruk";
const targetOutputName = targetName === undefined
  ? defaultName
  : `${targetName}${target?.windows === true ? ".exe" : ""}`;
const output = path.resolve(
  root,
  process.env["RUK_BINARY_OUTFILE"] ?? path.join("artifacts", targetOutputName),
);

await fs.mkdir(path.dirname(output), { recursive: true });
const args = [
  "build",
  "-trimpath",
  "-ldflags",
  `-s -w -X main.version=${version} -X main.distribution=${distribution}`,
  "-o",
  output,
  "./cmd/ruk",
];
const environment = { ...process.env, CGO_ENABLED: "0" };
if (target !== undefined) {
  environment.GOOS = target.goos;
  environment.GOARCH = target.goarch;
} else {
  delete environment.GOOS;
  delete environment.GOARCH;
}

await run("go", args, { cwd: root, env: environment, stdio: "inherit" });
const stat = await fs.stat(output);
if (!stat.isFile() || stat.size <= 0) {
  throw new Error(`Go build produced an empty or non-file executable at ${output}`);
}
if (target?.windows !== true && !target && process.platform !== "win32" && (stat.mode & 0o111) === 0) {
  throw new Error(`Go build produced a non-executable file at ${output}`);
}

process.stdout.write(`Built ${output}${targetName ? ` for ${targetName}` : ""}.\n`);
