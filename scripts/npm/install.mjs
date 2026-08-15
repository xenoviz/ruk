#!/usr/bin/env node

import path from "node:path";
import { fileURLToPath } from "node:url";
import { installNativeLauncher } from "./launcher.mjs";

const root = path.resolve(fileURLToPath(new URL("../..", import.meta.url)));

try {
  const installed = await installNativeLauncher({ root });
  process.stdout.write(`Installed native ${installed.target} for ${installed.packageName}.\n`);
} catch (error) {
  const message = error instanceof Error ? error.message : String(error);
  process.stderr.write(`Ruk native installation failed: ${message}\n`);
  process.exitCode = 1;
}
