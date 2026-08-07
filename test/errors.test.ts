import assert from "node:assert/strict";
import test from "node:test";
import { errorRecord, jsonRequested } from "../src/errors.js";

test("structured failures expose stable automation categories", () => {
  assert.deepEqual(errorRecord(new Error("Workspace has uncommitted changes.")), {
    status: "error",
    code: "WORKSPACE_DIRTY",
    message: "Workspace has uncommitted changes.",
    retryable: false,
  });
  assert.equal(errorRecord(new Error("Could not allocate an available port")).code, "PORT_UNAVAILABLE");
  assert.equal(errorRecord(new Error("Dependency installation failed")).code, "DEPENDENCY_PREPARATION_FAILED");
  assert.equal(errorRecord(new Error("unexpected")).code, "OPERATION_FAILED");
  assert.equal(jsonRequested(["status", "--json"]), true);
  assert.equal(jsonRequested(["exec", "branch", "--", "tool", "--json"]), false);
});
