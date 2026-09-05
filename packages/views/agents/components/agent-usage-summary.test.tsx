// @vitest-environment jsdom

import { describe, expect, it } from "vitest";
import type { RuntimeProviderUsageWindow } from "@multica/core/types";
import { prioritizeUsageWindows } from "./agent-usage-summary";

const codexWindows: RuntimeProviderUsageWindow[] = [
  {
    id: "codex-primary",
    group: "Codex",
    label: "5 hour limit",
    used_percent: 20,
    remaining_percent: 80,
    window_duration_mins: 300,
    unit: "percent",
  },
  {
    id: "codex-secondary",
    group: "Codex",
    label: "Weekly limit",
    used_percent: 35,
    remaining_percent: 65,
    window_duration_mins: 10080,
    unit: "percent",
  },
  {
    id: "spark-primary",
    group: "GPT-5.3-Codex-Spark",
    label: "5 hour limit",
    used_percent: 25,
    remaining_percent: 75,
    window_duration_mins: 300,
    unit: "percent",
  },
  {
    id: "spark-secondary",
    group: "GPT-5.3-Codex-Spark",
    label: "Weekly limit",
    used_percent: 40,
    remaining_percent: 60,
    window_duration_mins: 10080,
    unit: "percent",
  },
  {
    id: "code-review-primary",
    group: "Code Review",
    label: "Weekly limit",
    used_percent: 2,
    remaining_percent: 98,
    window_duration_mins: 10080,
    unit: "percent",
  },
];

describe("prioritizeUsageWindows", () => {
  it("highlights both current-model Codex windows and lists Spark beside them", () => {
    const rows = prioritizeUsageWindows(codexWindows, "gpt-5.6-sol", "codex");

    expect(rows.map(({ current }) => current)).toEqual([true, true, false, false]);
    expect(rows.map(({ displayLabel }) => displayLabel)).toEqual([
      "gpt-5.6-sol",
      "gpt-5.6-sol",
      "GPT-5.3-Codex-Spark",
      "GPT-5.3-Codex-Spark",
    ]);
    expect(rows.map(({ window }) => window.window_duration_mins)).toEqual([
      300,
      10080,
      300,
      10080,
    ]);
  });

  it("moves Spark's independent limits first when Spark is the current model", () => {
    const rows = prioritizeUsageWindows(
      codexWindows,
      "gpt-5.3-codex-spark",
      "codex",
    );

    expect(rows.map(({ current }) => current)).toEqual([true, true, false, false]);
    expect(rows.slice(0, 2).map(({ displayLabel }) => displayLabel)).toEqual([
      "gpt-5.3-codex-spark",
      "gpt-5.3-codex-spark",
    ]);
    expect(rows.slice(0, 2).map(({ window }) => window.window_duration_mins)).toEqual([
      300,
      10080,
    ]);
  });

  it("marks Claude's shared five-hour and weekly windows as current", () => {
    const rows = prioritizeUsageWindows(
      [
        { ...codexWindows[1]!, id: "seven-day", group: "Claude Code" },
        { ...codexWindows[0]!, id: "five-hour", group: "Claude Code" },
      ],
      "claude-sonnet-4-6",
      "claude",
    );

    expect(rows.map(({ current }) => current)).toEqual([true, true]);
    expect(rows.map(({ displayLabel }) => displayLabel)).toEqual([
      "claude-sonnet-4-6",
      "claude-sonnet-4-6",
    ]);
    expect(rows.map(({ window }) => window.window_duration_mins)).toEqual([
      300,
      10080,
    ]);
  });
});
