import { SharedCheckoutError } from "./checkout.js";
import { ProcessIdentityUnavailableError } from "./process.js";

export interface ErrorRecord {
  status: "error";
  code: string;
  message: string;
  retryable: boolean;
  activeAssignments?: number;
  recovery?: string;
}

export class DependencyPreparationError extends Error {
  override readonly name = "DependencyPreparationError";
}

const CATEGORIES: ReadonlyArray<readonly [RegExp, string, boolean]> = [
  [/shared dependency backend/i, "DEPENDENCY_PREPARATION_FAILED", true],
  [/cannot read .*\.rukrc\.json:/i, "INVALID_ARGUMENT", false],
  [/unknown (?:command|(?:\S+\s+)*options?)(?::|\s|$)|requires|does not accept|must (?:be|contain)|exactly one/i, "INVALID_ARGUMENT", false],
  [/assignment .* does not exist|expected assigned|preparation operation does not match/i, "ASSIGNMENT_CONFLICT", false],
  [/uncommitted changes|workspace .* dirty/i, "WORKSPACE_DIRTY", false],
  [/port .* unavailable|allocate an available port|allocator returned/i, "PORT_UNAVAILABLE", true],
  [/lock|acquisition is still in progress|changed before collection|still has tracked processes|survived graceful termination|could not enumerate POSIX processes/i, "RESOURCE_BUSY", true],
  [/install|dependency|node_modules projection/i, "DEPENDENCY_PREPARATION_FAILED", true],
  [/git |worktree|branch .* checked out|remote .* does not exist/i, "GIT_OPERATION_FAILED", false],
];

export function errorRecord(error: unknown): ErrorRecord {
  const message = error instanceof Error ? error.message : String(error);
  if (error instanceof SharedCheckoutError) {
    return {
      status: "error",
      code: "RESOURCE_BUSY",
      message,
      retryable: true,
      activeAssignments: error.activeAssignments,
      recovery: error.recovery,
    };
  }
  const match = error instanceof DependencyPreparationError
    ? [/.*/, "DEPENDENCY_PREPARATION_FAILED", true] as const
    : error instanceof ProcessIdentityUnavailableError
      ? [/.*/, "RESOURCE_BUSY", true] as const
      : CATEGORIES.find(([pattern]) => pattern.test(message));
  return {
    status: "error",
    code: match?.[1] ?? "OPERATION_FAILED",
    message,
    retryable: match?.[2] ?? false,
  };
}

export function jsonRequested(argv: readonly string[]): boolean {
  if (["run", "exec", "shell"].includes(argv[0] ?? "")) return false;
  const separator = argv.indexOf("--");
  return argv.slice(0, separator < 0 ? argv.length : separator).includes("--json");
}
