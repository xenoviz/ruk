import assert from "node:assert/strict";
import test from "node:test";
import {
  SharedCheckoutError,
  activeAssignmentCount,
  sharedCheckoutDiagnostic,
} from "../src/checkout.js";
import { errorRecord } from "../src/errors.js";
import type { Repository, RukState } from "../src/types.js";

const repository: Repository = {
  root: "/repo",
  commonDir: "/repo/.git",
  primaryRoot: "/repo",
  primaryCheckout: true,
};

const state: RukState = {
  version: 4,
  trees: {},
  workspaces: {
    "/workspace": {
      path: "/workspace",
      managed: true,
      branch: "agent/active",
      lifecycle: "assigned",
      operationId: null,
      assignment: {
        id: "8ca205ee-497a-4574-9cb4-ddc6a4656766",
        owner: "agent",
        hostname: "host",
        assignedAt: "2026-01-01T00:00:00.000Z",
        renewedAt: "2026-01-01T00:00:00.000Z",
        expiresAt: "2026-01-01T08:00:00.000Z",
        leaseDurationMinutes: 480,
        lastActivityAt: "2026-01-01T00:00:00.000Z",
        leaseKeepers: [],
        ports: {},
      },
      processes: [],
      createdAt: "2026-01-01T00:00:00.000Z",
      updatedAt: "2026-01-01T00:00:00.000Z",
      availableAt: null,
      failure: null,
    },
  },
  metrics: {
    acquisitions: 0,
    workspaceReuses: 0,
    preparations: 0,
    preparationSkips: 0,
    preparationFailures: 0,
    totalPreparationMs: 0,
    lastPreparationMs: null,
  },
};

test("shared checkout policy denies task commands by default", () => {
  assert.equal(activeAssignmentCount(state), 1);
  assert.throws(
    () => sharedCheckoutDiagnostic(repository, state, "deny", false),
    SharedCheckoutError,
  );
  assert.deepEqual(
    errorRecord(new SharedCheckoutError(1)),
    {
      status: "error",
      code: "RESOURCE_BUSY",
      message: "Primary checkout has 1 active Ruk assignment; acquire a dedicated workspace or pass --allow-shared-checkout",
      retryable: true,
      activeAssignments: 1,
      recovery: "ruk acquire <branch>",
    },
  );
});

test("shared checkout policy warns or permits explicit overrides", () => {
  assert.match(sharedCheckoutDiagnostic(repository, state, "warn", false) ?? "", /Primary checkout/);
  assert.equal(sharedCheckoutDiagnostic(repository, state, "allow", false), null);
  assert.equal(sharedCheckoutDiagnostic(repository, state, "deny", true), null);
  assert.equal(sharedCheckoutDiagnostic({ ...repository, primaryCheckout: false }, state, "deny", false), null);
  assert.equal(sharedCheckoutDiagnostic(repository, { ...state, workspaces: {} }, "deny", false), null);
});
