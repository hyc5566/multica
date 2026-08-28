// @vitest-environment jsdom

import { beforeEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import { I18nProvider } from "@multica/core/i18n/react";
import type { Agent, AgentRuntime, RuntimeProviderUsage } from "@multica/core/types";
import enCommon from "../../locales/en/common.json";
import enAgents from "../../locales/en/agents.json";

const TEST_RESOURCES = { en: { common: enCommon, agents: enAgents } };
const provider = vi.hoisted(() => ({ current: undefined as RuntimeProviderUsage | undefined }));

vi.mock("../../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => "Asia/Taipei",
}));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return {
    ...actual,
    useQuery: (options: { queryKey: readonly unknown[] }) => {
      if (options.queryKey[1] === "provider-usage") {
        return {
          data: provider.current,
          isPending: false,
          isFetching: false,
          isError: false,
          refetch: vi.fn(),
        };
      }
      return {
        data: [{ agent_id: "agent-1", input_tokens: 1000, output_tokens: 200, cache_read_tokens: 300, cache_write_tokens: 0, task_count: 2 }],
        isPending: false,
        isFetching: false,
        isError: false,
        refetch: vi.fn(),
      };
    },
  };
});

import { AgentUsageSummary } from "./agent-usage-summary";

const agent = { id: "agent-1" } as Agent;
const runtime = {
  id: "runtime-1",
  provider: "codex",
  status: "online",
} as AgentRuntime;

function renderSummary() {
  return render(
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      <AgentUsageSummary agent={agent} runtime={runtime} />
    </I18nProvider>,
  );
}

beforeEach(() => {
  cleanup();
  provider.current = undefined;
});

describe("AgentUsageSummary current context", () => {
  it("separates active context, account quota, and 7-day Multica usage", () => {
    provider.current = {
      provider: "codex",
      status: "available",
      source: "official",
      observed_at: "2026-08-28T10:00:00Z",
      context: {
        scope: "active_task",
        status: "available",
        source: "official",
        active_task_count: 1,
        used_tokens: 125274,
        max_tokens: 258400,
        remaining_tokens: 133126,
        used_percent: 48.48,
        observed_at: "2026-08-28T10:00:01Z",
      },
      windows: [{ id: "weekly", label: "Weekly limit", used_percent: 20, remaining_percent: 80, unit: "percent" }],
    };
    renderSummary();

    expect(screen.getByText(enAgents.detail.usage.current_context)).not.toBeNull();
    expect(screen.getByText(/Provider account quota/)).not.toBeNull();
    expect(screen.getByText(enAgents.detail.usage.multica_7d)).not.toBeNull();
    expect(screen.getByText("125.3K")).not.toBeNull();
    expect(screen.getByText("258.4K")).not.toBeNull();
    expect(screen.getByText("133.1K")).not.toBeNull();
    expect(screen.getByText("48%")).not.toBeNull();
    expect(screen.getAllByText("Updated 06:00 PM")).toHaveLength(2);
  });

  it("keeps current context visible when the account-quota probe fails", () => {
    provider.current = {
      provider: "codex",
      status: "error",
      source: "unavailable",
      observed_at: "2026-08-28T10:00:00Z",
      message: "Account quota probe failed.",
      context: {
        scope: "active_task",
        status: "available",
        source: "official",
        active_task_count: 1,
        used_tokens: 100000,
        max_tokens: 200000,
        remaining_tokens: 100000,
        used_percent: 50,
        observed_at: "2026-08-28T10:00:01Z",
      },
    };
    renderSummary();

    expect(screen.getByText("50%")).not.toBeNull();
    expect(screen.getByText(enAgents.detail.usage.probe_error)).not.toBeNull();
  });

  it("shows Antigravity's live-context limitation without a fabricated zero", () => {
    provider.current = {
      provider: "antigravity",
      status: "available",
      source: "official",
      observed_at: "2026-08-28T10:00:00Z",
      context: {
        scope: "active_task",
        status: "unavailable",
        source: "unavailable",
        reason: "provider_unsupported",
        active_task_count: 1,
        observed_at: "2026-08-28T10:00:01Z",
      },
    };
    renderSummary();

    expect(screen.getByText(enAgents.detail.usage.context_unsupported)).not.toBeNull();
    expect(screen.queryByText("0", { exact: true })).toBeNull();
  });

  it("shows Claude's used-only partial snapshot without inventing max or remaining", () => {
    provider.current = {
      provider: "claude",
      status: "partial",
      source: "official",
      observed_at: "2026-08-28T10:00:00Z",
      context: {
        scope: "active_task",
        status: "partial",
        source: "derived",
        reason: "max_unavailable",
        active_task_count: 1,
        used_tokens: 42000,
        observed_at: "2026-08-28T10:00:01Z",
      },
    };
    renderSummary();

    expect(screen.getByText("42K")).not.toBeNull();
    expect(screen.getByText(enAgents.detail.usage.context_max_unavailable)).not.toBeNull();
    expect(screen.getAllByText("—")).toHaveLength(2);
  });

  it("renders idle and stale reasons as states, never as zero context", () => {
    provider.current = {
      provider: "codex",
      status: "available",
      source: "official",
      observed_at: "2026-08-28T10:00:00Z",
      context: {
        scope: "active_task",
        status: "unavailable",
        source: "unavailable",
        reason: "idle",
        active_task_count: 0,
        observed_at: "2026-08-28T10:00:01Z",
      },
    };
    const view = renderSummary();
    expect(screen.getByText(enAgents.detail.usage.context_idle)).not.toBeNull();
    expect(screen.queryByText("0", { exact: true })).toBeNull();

    provider.current = {
      ...provider.current,
      context: {
        scope: "active_task",
        status: "partial",
        source: "official",
        reason: "stale",
        active_task_count: 1,
        used_tokens: 100000,
        max_tokens: 200000,
        remaining_tokens: 100000,
        used_percent: 50,
        observed_at: "2026-08-28T09:55:00Z",
      },
    };
    view.rerender(
      <I18nProvider locale="en" resources={TEST_RESOURCES}>
        <AgentUsageSummary agent={agent} runtime={runtime} />
      </I18nProvider>,
    );
    expect(screen.getByText(enAgents.detail.usage.context_stale)).not.toBeNull();
    expect(screen.getByText("50%")).not.toBeNull();
  });
});
