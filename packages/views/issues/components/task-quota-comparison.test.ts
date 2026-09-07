import { describe, expect, it } from "vitest";
import type { TaskQuotaCheckpoint } from "@multica/core/types";
import { compareTaskQuotaCheckpoints } from "./task-quota-comparison";

function checkpoint(
  phase: "before" | "after",
  used: number,
  overrides: Partial<TaskQuotaCheckpoint> = {},
): TaskQuotaCheckpoint {
  return {
    phase,
    boundary_at: phase === "before" ? "2026-09-07T01:00:01Z" : "2026-09-07T01:10:01Z",
    provider: "codex",
    capture_state: "fresh",
    observation_id: phase,
    overlapping_task_count: 0,
    snapshot: {
      provider: "codex",
      status: "available",
      source: "official",
      observed_at: phase === "before" ? "2026-09-07T01:00:00Z" : "2026-09-07T01:10:00Z",
      windows: [{ id: "five_hour", label: "5 hour", used_percent: used, resets_at: "2026-09-07T05:00:00Z", unit: "percent" }],
    },
    ...overrides,
  };
}

describe("compareTaskQuotaCheckpoints", () => {
  it("reports a delta for distinct observations in one reset window", () => {
    expect(compareTaskQuotaCheckpoints([checkpoint("before", 12), checkpoint("after", 15)]))
      .toMatchObject([{ state: "comparable", before: 12, after: 15, delta: 3 }]);
  });

  it("does not turn one cached observation into a zero delta", () => {
    const before = checkpoint("before", 12, { observation_id: "same" });
    const after = checkpoint("after", 12, { observation_id: "same", capture_state: "cached" });
    expect(compareTaskQuotaCheckpoints([before, after])[0]?.state).toBe("same_observation");
  });

  it("marks a reset crossing instead of showing a negative delta", () => {
    const after = checkpoint("after", 2);
    after.snapshot!.windows![0]!.resets_at = "2026-09-07T10:00:00Z";
    expect(compareTaskQuotaCheckpoints([checkpoint("before", 98), after])[0]?.state)
      .toBe("reset_crossed");
  });

  it("keeps stale observations visible but non-comparable", () => {
    const after = checkpoint("after", 15, { capture_state: "stale_cache" });
    expect(compareTaskQuotaCheckpoints([checkpoint("before", 12), after])[0]?.state)
      .toBe("stale");
  });
});
