// @vitest-environment node
import { describe, expect, it } from "vitest";
import type { RuntimeProviderUsageWindow } from "@multica/core/types";
import { prioritizeAntigravityWindows } from "./antigravity-usage";

const windows: RuntimeProviderUsageWindow[] = [
  { id: "3p-weekly", group: "Claude and GPT models", label: "Weekly", window_duration_mins: 10080, used_percent: 15, unit: "percent" },
  { id: "gemini-weekly", group: "Gemini Models", label: "Weekly", window_duration_mins: 10080, used_percent: 25, unit: "percent" },
  { id: "3p-5h", group: "Claude and GPT models", label: "Five Hour", window_duration_mins: 300, used_percent: 30, unit: "percent" },
  { id: "gemini-5h", group: "Gemini Models", label: "Five Hour", window_duration_mins: 300, used_percent: 40, unit: "percent" },
];
describe("Antigravity quota pools", () => {
  it.each(["claude-sonnet-4-6", "gpt-oss-120b-medium"])("highlights the shared third-party pool for %s", (model) => {
    const rows = prioritizeAntigravityWindows(windows, model);
    expect(rows.map((r) => r.window.id)).toEqual(["3p-5h", "3p-weekly", "gemini-5h", "gemini-weekly"]);
    expect(rows.map((r) => r.current)).toEqual([true, true, false, false]);
    expect(rows.map((r) => r.window.used_percent)).toEqual([30, 15, 40, 25]);
  });
  it("highlights Google and preserves per-window values", () => {
    const rows = prioritizeAntigravityWindows(windows, "gemini-3.1-pro-high");
    expect(rows.map((r) => r.window.id)).toEqual(["gemini-5h", "gemini-weekly", "3p-5h", "3p-weekly"]);
    expect(rows.map((r) => r.current)).toEqual([true, true, false, false]);
  });
  it("keeps missing windows unknown and never borrows another pool's percentage", () => {
    const rows = prioritizeAntigravityWindows([windows[3]!], "gemini-pro");
    expect(rows).toHaveLength(4);
    expect(rows[0]!.window.used_percent).toBe(40);
    for (const { window } of rows.slice(1)) {
      expect(window.used_percent).toBeUndefined();
      expect(window.remaining_percent).toBeUndefined();
      expect(window.resets_at).toBeUndefined();
    }
  });
  it("preserves independent and historical model buckets without marking an unknown model current", () => {
    const independent = ["new-a", "new-b", "gemini-old"].map((id) => ({ id, group: "Same label", label: "Quota", used_percent: 20, unit: "percent" }));
    const rows = prioritizeAntigravityWindows(independent, "unknown");
    expect(rows.filter((r) => r.window.used_percent !== undefined).map((r) => r.window.id)).toEqual(["new-a", "new-b", "gemini-old"]);
    expect(rows.some((r) => r.current)).toBe(false);
  });
});
