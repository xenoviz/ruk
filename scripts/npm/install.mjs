#!/usr/bin/env node

import path from "node:path";
import { fileURLToPath } from "node:url";
import { installNativeLauncher } from "./launcher.mjs";

const root = path.resolve(fileURLToPath(new URL("../..", import.meta.url)));

try {
  const installed = await installNativeLauncher({ root });
  const action = installed.deferred ? "Scheduled native replacement" : "Installed native";
  process.stdout.write(`${action} ${installed.target} for ${installed.packageName}.\n`);
  if (installed.cleanupPending) {
    process.stderr.write("Ruk native installation succeeded, but temporary backup cleanup is pending; manual cleanup may be required.\n");
  }
} catch (error) {
  const message = error instanceof Error ? error.message : String(error);
  process.stderr.write(`Ruk native installation failed: ${message}\n`);
  process.exitCode = 1;
}
