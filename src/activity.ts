import {
  beginAssignmentActivity,
  finishAssignmentActivity,
  refreshAssignmentActivity,
} from "./lifecycle.js";
import { readState } from "./state.js";
import type { AssignmentRecord, StorePaths } from "./types.js";

const MAX_HEARTBEAT_INTERVAL_MS = 5 * 60_000;

export interface AssignmentActivityOptions {
  heartbeatIntervalMs?: number;
  keeperId?: string;
  onFailure?: (error: unknown) => void | Promise<void>;
}

export function activityHeartbeatInterval(leaseDurationMinutes: number): number {
  if (!Number.isFinite(leaseDurationMinutes) || leaseDurationMinutes <= 0) {
    throw new Error("leaseDurationMinutes must be positive and finite");
  }
  return Math.max(1, Math.min(MAX_HEARTBEAT_INTERVAL_MS, leaseDurationMinutes * 60_000 / 3));
}

function assignmentById(assignments: readonly AssignmentRecord[], assignmentId: string): AssignmentRecord {
  const assignment = assignments.find(({ id }) => id === assignmentId);
  if (!assignment) throw new Error(`Assignment ${assignmentId} does not exist`);
  return assignment;
}

async function currentAssignment(paths: StorePaths, assignmentId: string): Promise<AssignmentRecord> {
  const state = await readState(paths);
  return assignmentById(
    Object.values(state.workspaces).flatMap(({ assignment }) => assignment ? [assignment] : []),
    assignmentId,
  );
}

function wait(milliseconds: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, milliseconds);
    signal.addEventListener("abort", () => {
      clearTimeout(timer);
      resolve();
    }, { once: true });
  });
}

function activityWindow(now: number, heartbeatIntervalMs: number): { now: string; validUntil: string } {
  return {
    now: new Date(now).toISOString(),
    validUntil: new Date(now + heartbeatIntervalMs * 2).toISOString(),
  };
}

export async function withAssignmentActivity<T>(
  paths: StorePaths,
  assignmentId: string,
  operation: () => Promise<T>,
  options: AssignmentActivityOptions = {},
): Promise<T> {
  const assignment = await currentAssignment(paths, assignmentId);
  const heartbeatIntervalMs = options.heartbeatIntervalMs
    ?? activityHeartbeatInterval(assignment.leaseDurationMinutes);
  if (!Number.isFinite(heartbeatIntervalMs) || heartbeatIntervalMs <= 0) {
    throw new Error("heartbeatIntervalMs must be positive and finite");
  }
  const keeperId = options.keeperId ?? crypto.randomUUID();
  await beginAssignmentActivity(paths, assignmentId, {
    keeperId,
    ...activityWindow(Date.now(), heartbeatIntervalMs),
  });

  const controller = new AbortController();
  let heartbeatError: unknown;
  let rejectHeartbeat!: (error: unknown) => void;
  const heartbeatFailure = new Promise<never>((_resolve, reject) => { rejectHeartbeat = reject; });
  const heartbeat = (async () => {
    try {
      while (!controller.signal.aborted) {
        await wait(heartbeatIntervalMs, controller.signal);
        if (controller.signal.aborted) return;
        await refreshAssignmentActivity(paths, assignmentId, {
          keeperId,
          ...activityWindow(Date.now(), heartbeatIntervalMs),
        });
      }
    } catch (error) {
      heartbeatError = error;
      try {
        await options.onFailure?.(error);
      } catch (cleanupError) {
        heartbeatError = new AggregateError(
          [error, cleanupError],
          `Assignment ${assignmentId} activity renewal and cleanup failed`,
        );
      }
      rejectHeartbeat(heartbeatError);
    }
  })();
  const work = Promise.resolve().then(operation);

  try {
    const result = await Promise.race([work, heartbeatFailure]);
    return result;
  } catch (error) {
    if (heartbeatError !== undefined) await work.catch(() => undefined);
    throw error;
  } finally {
    controller.abort();
    await heartbeat;
    try {
      await finishAssignmentActivity(paths, assignmentId, keeperId);
    } catch {
      // The assignment may have been released, or the expiring keeper can be pruned later.
    }
  }
}
