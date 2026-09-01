// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type {
  Agent,
  AgentRuntime,
  RuntimeProviderUsage,
  RuntimeProviderUsageWindow,
} from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import enAgents from "../../locales/en/agents.json";

const providerState = vi.hoisted(() => ({
  current: null as RuntimeProviderUsage | null,
}));

vi.mock("@multica/core/runtimes", () => ({
  runtimeProviderUsageOptions: () => ({ queryKey: ["provider-usage"] }),
  runtimeUsageByAgentOptions: () => ({ queryKey: ["usage-by-agent"] }),
}));

vi.mock("@tanstack/react-query", () => ({
  useQuery: (options: { queryKey: string[] }) =>
    options.queryKey[0] === "provider-usage"
      ? {
          data: providerState.current,
          isFetching: false,
          isPending: false,
          isError: false,
          refetch: vi.fn(),
        }
      : {
          data: [
            {
              agent_id: "agent-1",
              provider: "codex",
              model: "gpt-5.6-sol",
              input_tokens: 10,
              output_tokens: 2,
              cache_read_tokens: 0,
              cache_write_tokens: 0,
              task_count: 1,
            },
          ],
          isFetching: false,
          isPending: false,
          isError: false,
          refetch: vi.fn(),
        },
}));

vi.mock("../../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => "UTC",
}));

import {
  AgentUsageSummary,
  prioritizeUsageWindows,
} from "./agent-usage-summary";

const agent = {
  id: "agent-1",
  model: "gpt-5.6-sol",
} as Agent;

const runtime = {
  id: "runtime-1",
  provider: "codex",
  status: "online",
  profile_id: "profile-1",
} as AgentRuntime;

function usage(overrides: Partial<RuntimeProviderUsage> = {}): RuntimeProviderUsage {
  return {
    provider: "codex",
    status: "available",
    source: "official",
    observed_at: "2026-08-31T10:00:00Z",
    windows: [
      {
        id: "codex-primary",
        group: "Codex",
        label: "Weekly limit",
        used_percent: 30,
        remaining_percent: 70,
        window_duration_mins: 10080,
        unit: "percent",
      },
    ],
    ...overrides,
  };
}

function view() {
  return (
    <I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
      <AgentUsageSummary agent={agent} runtime={runtime} />
    </I18nProvider>
  );
}

function antigravityView() {
  return (
    <I18nProvider locale="en" resources={{ en: { agents: enAgents } }}>
      <AgentUsageSummary
        agent={{ ...agent, model: "gemini-3.7-flash-medium" }}
        runtime={{ ...runtime, provider: "antigravity" }}
      />
    </I18nProvider>
  );
}

describe("AgentUsageSummary", () => {
  afterEach(() => {
    cleanup();
    providerState.current = null;
  });

  it("keeps the last successful quota snapshot when a refresh is rate-limited", () => {
    providerState.current = usage();
    const rendered = render(view());

    expect(screen.getByText("30% used")).toBeInTheDocument();
    expect(screen.getByText("Models used by runs: codex/gpt-5.6-sol")).toBeInTheDocument();

    providerState.current = usage({
      status: "rate_limited",
      source: "unavailable",
      windows: undefined,
      retry_after_seconds: 25,
      message: "cooling down",
    });
    rendered.rerender(view());

    expect(screen.getByText("30% used")).toBeInTheDocument();
    expect(
      screen.getByText(
        /Refresh failed; showing the last successful snapshot.*25 seconds/,
      ),
    ).toBeInTheDocument();
    expect(screen.getByText(/Model: gpt-5\.6-sol/)).toBeInTheDocument();
    expect(screen.getByText(/Profile: profile-1/)).toBeInTheDocument();
  });

  it("collapses Antigravity's shared quota buckets and keeps every current-model quota first", () => {
    providerState.current = usage({
      provider: "antigravity",
      windows: [
        {
          id: "gemini-3.7-flash-medium",
          group: "Gemini 3.7 Flash (Medium)",
          label: "Model quota",
          used_percent: 20,
          remaining_percent: 80,
          resets_at: "2026-09-01T12:00:00Z",
          unit: "percent",
        },
        {
          id: "gemini-3.7-pro-high",
          group: "Gemini 3.7 Pro (High)",
          label: "Model quota",
          used_percent: 20,
          remaining_percent: 80,
          resets_at: "2026-09-01T12:00:00Z",
          unit: "percent",
        },
        {
          id: "gemini-3.7-flash-medium-secondary",
          group: "Gemini 3.7 Flash (Medium)",
          label: "Model quota",
          used_percent: 45,
          remaining_percent: 55,
          resets_at: "2026-09-02T12:00:00Z",
          unit: "percent",
        },
        {
          id: "claude-opus-4-6-thinking",
          group: "Claude Opus 4.6 (Thinking)",
          label: "Model quota",
          used_percent: 65,
          remaining_percent: 35,
          resets_at: "2026-09-03T12:00:00Z",
          unit: "percent",
        },
        {
          id: "claude-sonnet-4-6",
          group: "Claude Sonnet 4.6",
          label: "Model quota",
          used_percent: 65,
          remaining_percent: 35,
          resets_at: "2026-09-03T12:00:00Z",
          unit: "percent",
        },
        {
          id: "chatgpt-128b-oss",
          group: "ChatGPT 128B OSS",
          label: "Model quota",
          used_percent: 65,
          remaining_percent: 35,
          resets_at: "2026-09-03T12:00:00Z",
          unit: "percent",
        },
        {
          id: "chat_20706",
          group: "chat_20706",
          label: "Model quota",
          used_percent: 5,
          remaining_percent: 95,
          resets_at: "2026-09-04T12:00:00Z",
          unit: "percent",
        },
      ],
    });

    render(antigravityView());

    const cards = Array.from(
      document.querySelectorAll("[data-current-model-usage]"),
    );
    expect(cards).toHaveLength(3);
    expect(cards[0]).toHaveAttribute("data-current-model-usage", "true");
    expect(cards[0]?.textContent).toContain("gemini-3.7-flash-medium");
    expect(cards[0]?.textContent).toContain("Current model");
    expect(cards[1]).toHaveAttribute("data-current-model-usage", "true");
    expect(cards[1]?.textContent).toContain("gemini-3.7-flash-medium");
    expect(cards[2]).toHaveAttribute("data-current-model-usage", "false");
    expect(cards[2]?.textContent).toContain("Claude Opus 4.6 (Thinking)");
    expect(cards[2]?.textContent).not.toContain("Claude Sonnet 4.6");
    expect(cards[2]?.textContent).not.toContain("ChatGPT 128B OSS");
    expect(document.body.textContent).not.toContain("chat_20706");

    const quotaStrip = document.querySelector("[data-provider-quota-scroll]");
    expect(quotaStrip).toHaveClass("overflow-x-auto");
    expect(document.querySelector("[data-multica-usage]")).toBeTruthy();
  });

  it("maps regular Codex models to the weekly shared quota and Spark to its own windows", () => {
    const windows: RuntimeProviderUsageWindow[] = [
      {
        id: "codex-primary",
        group: "Codex",
        label: "Weekly limit",
        used_percent: 18,
        window_duration_mins: 10080,
        unit: "percent",
      },
      {
        id: "model-0-primary",
        group: "GPT-5.3-Codex-Spark",
        label: "5 hour limit",
        used_percent: 25,
        window_duration_mins: 300,
        unit: "percent",
      },
      {
        id: "model-0-secondary",
        group: "GPT-5.3-Codex-Spark",
        label: "Weekly limit",
        used_percent: 40,
        window_duration_mins: 10080,
        unit: "percent",
      },
      {
        id: "code-review-primary",
        group: "Code Review",
        label: "Weekly limit",
        used_percent: 2,
        window_duration_mins: 10080,
        unit: "percent",
      },
    ];

    const regular = prioritizeUsageWindows(windows, "gpt-5.6-terra", "codex");
    expect(regular.map(({ current }) => current)).toEqual([true, false, false]);
    expect(regular.map(({ displayLabel }) => displayLabel)).toEqual([
      "gpt-5.6-terra",
      "GPT-5.3-Codex-Spark",
      "GPT-5.3-Codex-Spark",
    ]);
    expect(regular.map(({ window }) => window.window_duration_mins)).toEqual([
      10080,
      300,
      10080,
    ]);

    const spark = prioritizeUsageWindows(
      windows,
      "gpt-5.3-codex-spark",
      "codex",
    );
    expect(spark.map(({ current }) => current)).toEqual([true, true, false]);
    expect(spark.map(({ displayLabel }) => displayLabel)).toEqual([
      "gpt-5.3-codex-spark",
      "gpt-5.3-codex-spark",
      "Codex",
    ]);
    expect(spark.map(({ window }) => window.window_duration_mins)).toEqual([
      300,
      10080,
      10080,
    ]);
  });

  it("treats Claude Code's five-hour and weekly limits as shared across models", () => {
    const windows: RuntimeProviderUsageWindow[] = [
      {
        id: "seven-day",
        group: "Claude Code",
        label: "Weekly limit",
        used_percent: 41,
        window_duration_mins: 10080,
        unit: "percent",
      },
      {
        id: "five-hour",
        group: "Claude Code",
        label: "5 hour limit",
        used_percent: 23,
        window_duration_mins: 300,
        unit: "percent",
      },
    ];

    const result = prioritizeUsageWindows(
      windows,
      "claude-sonnet-4.6",
      "claude",
    );
    expect(result.map(({ current }) => current)).toEqual([true, true]);
    expect(result.map(({ displayLabel }) => displayLabel)).toEqual([
      "Claude Code",
      "Claude Code",
    ]);
    expect(result.map(({ window }) => window.window_duration_mins)).toEqual([
      300,
      10080,
    ]);
  });
});
