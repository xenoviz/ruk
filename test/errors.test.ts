import assert from "node:assert/strict";
import test from "node:test";
import { AssignmentActivityError } from "../src/activity.js";
import { errorRecord, jsonRequested, retainedAssignmentFailure } from "../src/errors.js";
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
  const retained = retainedAssignmentFailure(
    "00000000-0000-4000-8000-000000000000",
    "/workspace",
    "2026-08-15T00:00:00.000Z",
    new AggregateError([new ProcessIdentityUnavailableError(42)], "cleanup failed"),
  );
  assert.ok(retained);
  assert.deepEqual(errorRecord(retained), {
    status: "error",
    code: "RESOURCE_BUSY",
    message: "Assignment 00000000-0000-4000-8000-000000000000 retained at /workspace: cleanup failed",
    retryable: true,
    assignmentId: "00000000-0000-4000-8000-000000000000",
    path: "/workspace",
    expiresAt: "2026-08-15T00:00:00.000Z",
    recovery: "ruk release 00000000-0000-4000-8000-000000000000",
  });
  assert.equal(retainedAssignmentFailure("id", "/workspace", "expiry", new Error("failed")), null);
  assert.equal(
    errorRecord(new AggregateError([
      new Error("heartbeat failed"),
      new ProcessIdentityUnavailableError(42),
    ], "activity and cleanup failed")).code,
    "RESOURCE_BUSY",
  );
  assert.deepEqual(errorRecord(new Error("Could not enumerate POSIX processes: unavailable")), {
    status: "error",
    code: "RESOURCE_BUSY",
    message: "Could not enumerate POSIX processes: unavailable",
    retryable: true,
  });
  assert.deepEqual(errorRecord(new AssignmentActivityError("assignment-id", new Error("EPERM"))), {
    status: "error",
    code: "RESOURCE_BUSY",
    message: "Assignment assignment-id activity renewal failed: EPERM",
    retryable: true,
  });
  assert.equal(errorRecord(new Error("unexpected")).code, "OPERATION_FAILED");
  assert.equal(jsonRequested(["status", "--json"]), true);
  assert.equal(jsonRequested(["exec", "branch", "--", "tool", "--json"]), false);
  assert.equal(jsonRequested(["run", "tool", "--json"]), false);
});
