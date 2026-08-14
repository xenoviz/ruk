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
  refresh?: typeof refreshAssignmentActivity;
  retryAttempts?: number;
  retryDelayMs?: number;
}

export class AssignmentActivityError extends Error {
  override readonly name = "AssignmentActivityError";

  constructor(assignmentId: string, cause: unknown) {
    const detail = cause instanceof Error ? cause.message : String(cause);
    super(`Assignment ${assignmentId} activity renewal failed: ${detail}`, { cause });
  }
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
    const complete = () => {
      signal.removeEventListener("abort", abort);
      resolve();
    };
    const timer = setTimeout(complete, milliseconds);
    const abort = () => {
      clearTimeout(timer);
      complete();
    };
    if (signal.aborted) abort();
    else signal.addEventListener("abort", abort, { once: true });
  });
}

function activityWindow(now: number, heartbeatIntervalMs: number): { now: string; validUntil: string } {
  return {
    now: new Date(now).toISOString(),
    validUntil: new Date(now + heartbeatIntervalMs * 2).toISOString(),
  };
}

function ownershipChanged(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error);
  return /assignment .* does not exist|expected assigned|lease keeper .* is not active/i.test(message);
}

export async function withAssignmentActivity<T>(
  paths: StorePaths,
  assignmentId: string,
  operation: (signal: AbortSignal) => Promise<T>,
  options: AssignmentActivityOptions = {},
): Promise<T> {
  const assignment = await currentAssignment(paths, assignmentId);
  let heartbeatIntervalMs = options.heartbeatIntervalMs
    ?? activityHeartbeatInterval(assignment.leaseDurationMinutes);
  if (!Number.isFinite(heartbeatIntervalMs) || heartbeatIntervalMs <= 0) {
    throw new Error("heartbeatIntervalMs must be positive and finite");
  }
  const retryAttempts = options.retryAttempts ?? 2;
  const retryDelayMs = options.retryDelayMs ?? Math.min(1_000, heartbeatIntervalMs / 4);
  if (!Number.isSafeInteger(retryAttempts) || retryAttempts < 0) {
    throw new Error("retryAttempts must be a non-negative integer");
  }
  if (!Number.isFinite(retryDelayMs) || retryDelayMs < 0) {
    throw new Error("retryDelayMs must be non-negative and finite");
  }
  const refresh = options.refresh ?? refreshAssignmentActivity;
  const keeperId = options.keeperId ?? crypto.randomUUID();
  await beginAssignmentActivity(paths, assignmentId, {
    keeperId,
    durationMs: heartbeatIntervalMs * 2,
  });

  const controller = new AbortController();
  const workController = new AbortController();
  let heartbeatError: unknown;
  let rejectHeartbeat!: (error: unknown) => void;
  const heartbeatFailure = new Promise<never>((_resolve, reject) => { rejectHeartbeat = reject; });
  const heartbeat = (async () => {
    try {
      while (!controller.signal.aborted) {
        await wait(heartbeatIntervalMs, controller.signal);
        if (controller.signal.aborted) return;
        let refreshed = false;
        for (let attempt = 0; attempt <= retryAttempts; attempt += 1) {
          try {
            const refreshedWorkspace = await refresh(paths, assignmentId, {
              keeperId,
              ...activityWindow(Date.now(), heartbeatIntervalMs),
              lockTimeoutMs: Math.max(1, Math.min(30_000, heartbeatIntervalMs / 2)),
            });
            if (options.heartbeatIntervalMs === undefined) {
              heartbeatIntervalMs = activityHeartbeatInterval(
                refreshedWorkspace.assignment!.leaseDurationMinutes,
              );
            }
            refreshed = true;
            break;
          } catch (error) {
            if (ownershipChanged(error) || attempt === retryAttempts) {
              throw new AssignmentActivityError(assignmentId, error);
            }
            await wait(retryDelayMs, controller.signal);
            if (controller.signal.aborted) return;
          }
        }
        if (!refreshed) return;
      }
    } catch (error) {
      heartbeatError = error;
      workController.abort(error);
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
  const work = Promise.resolve().then(() => operation(workController.signal));

  let result!: T;
  let completed = false;
  let failure: unknown;
  try {
    result = await Promise.race([work, heartbeatFailure]);
    completed = true;
  } catch (error) {
    failure = error;
    if (heartbeatError !== undefined) {
      try {
        await work;
      } catch (operationError) {
        if (operationError !== error) {
          failure = new AggregateError(
            [error, operationError],
            `Assignment ${assignmentId} activity renewal and operation cleanup failed`,
          );
        }
      }
    }
  } finally {
    controller.abort();
    await heartbeat;
    try {
      await finishAssignmentActivity(paths, assignmentId, keeperId);
    } catch {
      // The assignment may have been released, or the expiring keeper can be pruned later.
    }
  }
  if (failure !== undefined) throw failure;
  if (heartbeatError !== undefined) throw heartbeatError;
  if (!completed) throw new Error(`Assignment ${assignmentId} activity operation did not complete`);
  return result;
}
