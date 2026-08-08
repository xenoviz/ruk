import assert from "node:assert/strict";
import test from "node:test";
import { errorRecord, jsonRequested } from "../src/errors.js";
import { ProcessIdentityUnavailableError } from "../src/process.js";

test("structured failures expose stable automation categories", () => {
  assert.deepEqual(errorRecord(new Error("Workspace has uncommitted changes.")), {
    status: "error",
    code: "WORKSPACE_DIRTY",
    message: "Workspace has uncommitted changes.",
    retryable: false,
  });
  assert.equal(errorRecord(new Error("Could not allocate an available port")).code, "PORT_UNAVAILABLE");
  assert.equal(errorRecord(new Error("Dependency installation failed")).code, "DEPENDENCY_PREPARATION_FAILED");
  assert.equal(errorRecord(new Error("Cannot read /repo/.rukrc.json: malformed JSON")).code, "INVALID_ARGUMENT");
  assert.deepEqual(
    errorRecord(new Error("bun 1.3.14 or newer is required for Ruk's shared dependency backend")),
    {
      status: "error",
      code: "DEPENDENCY_PREPARATION_FAILED",
      message: "bun 1.3.14 or newer is required for Ruk's shared dependency backend",
      retryable: true,
    },
  );
  assert.deepEqual(errorRecord(new ProcessIdentityUnavailableError(42)), {
    status: "error",
    code: "RESOURCE_BUSY",
    message: "Process 42 could not be identified, so its workspace cannot be released safely",
    retryable: true,
  });
  assert.deepEqual(errorRecord(new Error("Could not enumerate POSIX processes: unavailable")), {
    status: "error",
    code: "RESOURCE_BUSY",
    message: "Could not enumerate POSIX processes: unavailable",
    retryable: true,
  });
  assert.equal(errorRecord(new Error("unexpected")).code, "OPERATION_FAILED");
  assert.equal(jsonRequested(["status", "--json"]), true);
  assert.equal(jsonRequested(["exec", "branch", "--", "tool", "--json"]), false);
  assert.equal(jsonRequested(["run", "tool", "--json"]), false);
});
