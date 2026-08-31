// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import type {
  Agent,
  AgentRuntime,
  RuntimeProviderUsage,
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

import { AgentUsageSummary } from "./agent-usage-summary";

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
        id: "five-hour",
        group: "Codex",
        label: "5 hour limit",
        used_percent: 30,
        remaining_percent: 70,
        window_duration_mins: 300,
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

  it("highlights the current model and places its quota before alternatives", () => {
    providerState.current = usage({
      provider: "antigravity",
      windows: [
        {
          id: "gemini-2.5-pro",
          group: "Gemini 2.5 Pro",
          label: "Daily limit",
          used_percent: 60,
          remaining_percent: 40,
          unit: "percent",
        },
        {
          id: "gpt-5.6-sol",
          group: "GPT 5.6 Sol",
          label: "Daily limit",
          used_percent: 20,
          remaining_percent: 80,
          unit: "percent",
        },
      ],
    });

    render(view());

    const cards = Array.from(
      document.querySelectorAll("[data-current-model-usage]"),
    );
    expect(cards[0]).toHaveAttribute("data-current-model-usage", "true");
    expect(cards[0]?.textContent).toContain("GPT 5.6 Sol");
    expect(cards[0]?.textContent).toContain("Current model");
    expect(cards[1]).toHaveAttribute("data-current-model-usage", "false");
    expect(cards[1]?.textContent).toContain("Gemini 2.5 Pro");
  });
});
