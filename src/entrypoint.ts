import { main } from "./cli.js";
import type { Distribution } from "./update.js";

export function start(distribution: Distribution): void {
  main(process.argv.slice(2), { distribution }).then(
    (code) => {
      if (code) process.exitCode = code;
    },
    (error) => {
      const message = error instanceof Error ? error.message : String(error);
      process.stderr.write(`ruk: ${message}\n`);
      process.exitCode = 1;
    },
  );
}
