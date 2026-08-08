import { main } from "./cli.js";
import { errorRecord, jsonRequested } from "./errors.js";
import type { Distribution } from "./update.js";

export function start(distribution: Distribution): void {
  const argv = process.argv.slice(2);
  main(argv, { distribution }).then(
    (code) => {
      if (code) process.exitCode = code;
    },
    (error) => {
      const failure = errorRecord(error);
      process.stderr.write(jsonRequested(argv) ? `${JSON.stringify(failure)}\n` : `ruk: ${failure.message}\n`);
      process.exitCode = 1;
    },
  );
}
