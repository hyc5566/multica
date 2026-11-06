import type {
  TaskQuotaCheckpoint,
  RuntimeProviderUsageWindow,
} from "@multica/core/types";

export type TaskQuotaDeltaState =
  | "comparable"
  | "same_observation"
  | "reset_crossed"
  | "stale"
  | "unavailable"
  | "missing";

export interface TaskQuotaWindowComparison {
  id: string;
  label: string;
  before?: number;
  after?: number;
  delta?: number;
  resetsAt?: string;
  scope?: RuntimeProviderUsageWindow["scope"];
  modelMatch?: RuntimeProviderUsageWindow["model_match"];
  state: TaskQuotaDeltaState;
}

const usableStates = new Set(["fresh", "cached"]);
const staleAfterMs = 15 * 60 * 1000;

function isStale(checkpoint: TaskQuotaCheckpoint): boolean {
  if (checkpoint.capture_state === "stale_cache" || checkpoint.snapshot?.stale) return true;
  if (checkpoint.observation_age_ms != null) return checkpoint.observation_age_ms > staleAfterMs;
  const observed = Date.parse(checkpoint.snapshot?.observed_at ?? "");
  const boundary = Date.parse(checkpoint.boundary_at);
  return Number.isFinite(observed) && Number.isFinite(boundary) && boundary - observed > staleAfterMs;
}

function used(window: RuntimeProviderUsageWindow | undefined): number | undefined {
  if (!window) return undefined;
  if (window.used_percent != null) return window.used_percent;
  return window.remaining_percent == null ? undefined : 100 - window.remaining_percent;
}

function resetWasCrossed(
  before: TaskQuotaCheckpoint,
  after: TaskQuotaCheckpoint,
  beforeWindow: RuntimeProviderUsageWindow,
  afterWindow: RuntimeProviderUsageWindow,
  beforeUsed: number,
  afterUsed: number,
): boolean {
  if (beforeWindow.resets_at !== afterWindow.resets_at) return true;
  if (afterUsed < beforeUsed) return true;
  if (!beforeWindow.resets_at) return false;
  const reset = Date.parse(beforeWindow.resets_at);
  return (
    Number.isFinite(reset) &&
    Date.parse(before.snapshot?.observed_at ?? before.boundary_at) < reset &&
    reset <= Date.parse(after.snapshot?.observed_at ?? after.boundary_at)
  );
}

/**
 * Compare normalized quota windows without implying task attribution. A delta
 * is emitted only for distinct, fresh observations from the same reset window.
 */
export function compareTaskQuotaCheckpoints(
  checkpoints: TaskQuotaCheckpoint[] | undefined,
): TaskQuotaWindowComparison[] {
  const before = checkpoints?.find((checkpoint) => checkpoint.phase === "before");
  const after = checkpoints?.find((checkpoint) => checkpoint.phase === "after");
  const windows = new Map<string, {
    before?: RuntimeProviderUsageWindow;
    after?: RuntimeProviderUsageWindow;
  }>();
  for (const window of before?.snapshot?.windows ?? []) {
    windows.set(window.id, { before: window });
  }
  for (const window of after?.snapshot?.windows ?? []) {
    windows.set(window.id, { ...windows.get(window.id), after: window });
  }
  if (windows.size === 0) return [];

  return Array.from(windows.entries()).map(([id, pair]) => {
    const beforeUsed = used(pair.before);
    const afterUsed = used(pair.after);
    const base = {
      id,
      label: pair.after?.label || pair.before?.label || id,
      before: beforeUsed,
      after: afterUsed,
      resetsAt: pair.after?.resets_at || pair.before?.resets_at,
      scope: pair.after?.scope || pair.before?.scope,
      modelMatch: pair.after?.model_match || pair.before?.model_match,
    };
    if (!before || !after || !pair.before || !pair.after || beforeUsed == null || afterUsed == null) {
      return { ...base, state: "missing" as const };
    }
    if (!usableStates.has(before.capture_state) || !usableStates.has(after.capture_state) ||
        isStale(before) || isStale(after)) {
      const stale = isStale(before) || isStale(after);
      return { ...base, state: stale ? "stale" as const : "unavailable" as const };
    }
    if (!before.observation_id || before.observation_id === after.observation_id) {
      return { ...base, state: "same_observation" as const };
    }
    if (resetWasCrossed(before, after, pair.before, pair.after, beforeUsed, afterUsed)) {
      return { ...base, state: "reset_crossed" as const };
    }
    return { ...base, delta: afterUsed - beforeUsed, state: "comparable" as const };
  });
}
