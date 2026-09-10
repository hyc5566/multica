// @vitest-environment jsdom

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { RuntimeProviderUsageWindow } from "@multica/core/types";
import { AgentUsageSummary, prioritizeUsageWindows } from "./agent-usage-summary";

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

// Refresh polling/failure matrices live in core/runtimes/provider-usage.test.ts.
// These checks cover the user-visible wiring and stale-data regression.

const mocks = vi.hoisted(() => ({ read: vi.fn(), start: vi.fn(), poll: vi.fn(), tokens: vi.fn() }));
vi.mock("@multica/core/api", () => ({ api: {
  getProviderUsageSnapshot: mocks.read,
  initiateProviderUsage: mocks.start,
  getProviderUsageResult: mocks.poll,
  getRuntimeUsageByAgent: mocks.tokens,
} }));
vi.mock("../../common/use-viewing-timezone", () => ({ useViewingTimezone: () => "Asia/Taipei" }));

import { cleanup, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";
import type { Agent, AgentRuntime, RuntimeProviderUsage } from "@multica/core/types";

const resources = { en: { common: enCommon, agents: enAgents } };
const agent = { id: "agent-1", model: "gpt-6-astra" } as Agent;
const runtime = { id: "rt-1", provider: "codex", status: "online" } as AgentRuntime;
let client: QueryClient;
let snapshot: RuntimeProviderUsage;
function mountSummary() {
  client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(<QueryClientProvider client={client}><I18nProvider locale="en" resources={resources}>
    <AgentUsageSummary agent={agent} runtime={runtime} />
  </I18nProvider></QueryClientProvider>);
}
beforeEach(() => {
  vi.resetAllMocks();
  snapshot = { provider: "codex", status: "available", source: "official", observed_at: new Date().toISOString(), windows: [codexWindows[0]!] };
  mocks.read.mockImplementation(async () => snapshot);
  mocks.tokens.mockResolvedValue([]);
});
afterEach(() => { cleanup(); client?.clear(); });

describe("AgentUsageSummary refresh", () => {
  it("keeps opening cache-only, then the button requests a refresh and displays the new value", async () => {
    mountSummary();
    await screen.findByText("20% used");
    expect(mocks.start).not.toHaveBeenCalled();
    snapshot = { ...snapshot, windows: [{ ...codexWindows[0]!, used_percent: 42 }] };
    mocks.start.mockResolvedValue({ id: "req-1", status: "completed", provider_usage: snapshot });
    fireEvent.click(screen.getByRole("button", { name: "Refresh usage" }));
    await screen.findByText("42% used");
    expect(mocks.start).toHaveBeenCalledWith("rt-1");
    await waitFor(() => expect(screen.getByRole("button", { name: "Refresh usage" })).toBeEnabled());
  });
  it("reports request failures while retaining the last successful observation", async () => {
    mountSummary();
    await screen.findByText("20% used");
    mocks.start.mockRejectedValue(new Error("offline"));
    fireEvent.click(screen.getByRole("button", { name: "Refresh usage" }));
    await screen.findByText(enAgents.detail.usage.refresh_failed);
    expect(screen.getByText("20% used")).toBeInTheDocument();
  });
  it("shows stale/failure metadata, a dated observation and no current percentages for reset windows", async () => {
    snapshot = { ...snapshot, status: "partial", stale: true, last_error_code: "error", observed_at: "2026-09-09T06:22:28Z",
      windows: [{ ...codexWindows[0]!, used_percent: 41, resets_at: "2020-01-01T00:00:00Z" }] };
    mountSummary();
    await screen.findByText(enAgents.detail.usage.stale);
    expect(screen.getByText(enAgents.detail.usage.refresh_failed)).toBeInTheDocument();
    expect(screen.getByText(enAgents.detail.usage.window_expired)).toBeInTheDocument();
    expect(screen.queryByText("41% used")).not.toBeInTheDocument();
    expect(document.querySelector("time")?.textContent).toContain("2026");
    expect(document.querySelector("time")?.textContent).toContain("02:22 PM");
  });
});
