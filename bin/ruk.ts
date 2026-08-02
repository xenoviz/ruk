#!/usr/bin/env node

import { main } from "../src/cli.js";

main(process.argv.slice(2)).then(
  (code) => {
    if (code) process.exitCode = code;
  },
  (error) => {
    const message = error instanceof Error ? error.message : String(error);
    process.stderr.write(`ruk: ${message}\n`);
    process.exitCode = 1;
  },
);
