import type { Repository, RukState, SharedCheckoutPolicy } from "./types.js";

export class SharedCheckoutError extends Error {
  override readonly name = "SharedCheckoutError";
  readonly activeAssignments: number;
  readonly recovery = "ruk acquire <branch>";

  constructor(activeAssignments: number) {
    super(
      `Primary checkout has ${activeAssignments} active Ruk assignment${activeAssignments === 1 ? "" : "s"}; `
      + "acquire a dedicated workspace or pass --allow-shared-checkout",
    );
    this.activeAssignments = activeAssignments;
  }
}

export function activeAssignmentCount(state: RukState): number {
  return Object.values(state.workspaces).filter(
    ({ lifecycle, assignment }) => lifecycle === "assigned" && assignment !== null,
  ).length;
}

export function sharedCheckoutDiagnostic(
  repository: Repository,
  state: RukState,
  policy: SharedCheckoutPolicy,
  allowSharedCheckout: boolean,
): string | null {
  const activeAssignments = activeAssignmentCount(state);
  if (!repository.primaryCheckout || activeAssignments === 0 || allowSharedCheckout || policy === "allow") {
    return null;
  }
  const error = new SharedCheckoutError(activeAssignments);
  if (policy === "deny") throw error;
  return `Warning: ${error.message}\n`;
}
