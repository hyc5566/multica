// @vitest-environment node

import { describe, expect, it } from "vitest";
import { quotaCheckpointsCSV } from "./chat-quota";

describe("quotaCheckpointsCSV", () => {
  it("exports task boundaries and neutralizes spreadsheet formulas", () => {
    const csv = quotaCheckpointsCSV([{
      task_id: "task-1",
      phase: "before",
      boundary_at: "2026-09-08T01:00:01Z",
      provider: "codex",
      capture_state: "fresh",
      overlapping_task_count: 0,
      snapshot: {
        provider: "codex",
        status: "available",
        source: "official",
        observed_at: "2026-09-08T01:00:00Z",
        windows: [{ id: "five-hour", label: "=IMPORTXML()", used_percent: 12, unit: "percent" }],
      },
    }]);

    expect(csv).toContain('"task-1","before","codex"');
    expect(csv).toContain('"\'=IMPORTXML()"');
  });
});
