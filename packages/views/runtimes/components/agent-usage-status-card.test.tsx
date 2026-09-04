// @vitest-environment jsdom

import type { ReactNode } from "react";
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import type { Agent, AgentRuntime, AgentTask, RuntimeUsageByAgent } from "@multica/core/types";
import type { AgentPresenceDetail } from "@multica/core/agents";
import { I18nProvider } from "@multica/core/i18n/react";
import enCommon from "../../locales/en/common.json";
import enRuntimes from "../../locales/en/runtimes.json";

const TEST_RESOURCES = { en: { common: enCommon, runtimes: enRuntimes } };
const usageState = vi.hoisted(() => ({ rows: [] as RuntimeUsageByAgent[] }));

vi.mock("@tanstack/react-query", async () => {
  const actual = await vi.importActual<typeof import("@tanstack/react-query")>(
    "@tanstack/react-query",
  );
  return {
    ...actual,
    useQuery: () => ({ data: usageState.rows, isLoading: false }),
  };
});

vi.mock("../../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => "UTC",
}));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => null }));
vi.mock("./provider-logo", () => ({ ProviderLogo: () => null }));
vi.mock("../../navigation", () => ({
  AppLink: ({ children }: { children: ReactNode }) => <>{children}</>,
}));

import { AgentUsageStatusCard, aggregateAgentUsage } from "./agent-usage-status-card";

const RUNTIME: AgentRuntime = {
  id: "runtime-1",
  workspace_id: "ws-1",
  daemon_id: "daemon-1",
  name: "Codex",
  runtime_mode: "local",
  provider: "codex",
  launch_header: "",
  status: "online",
  device_info: "host.local",
  metadata: {},
  owner_id: "user-1",
  visibility: "private",
  last_seen_at: "2026-09-03T09:59:50Z",
  created_at: "2026-09-03T09:00:00Z",
  updated_at: "2026-09-03T09:00:00Z",
};

const AGENT = {
  id: "agent-1",
  runtime_id: "runtime-1",
  name: "Mika",
  model: "gpt-5",
} as Agent;

const TASK = {
  id: "task-123456789",
  agent_id: "agent-1",
  runtime_id: "runtime-1",
  issue_id: "issue-abcdefgh",
  status: "running",
  priority: 0,
  dispatched_at: "2026-09-03T09:58:00Z",
  started_at: "2026-09-03T09:59:00Z",
  completed_at: null,
  result: null,
  error: null,
  created_at: "2026-09-03T09:57:00Z",
} satisfies AgentTask;

const PRESENCE: AgentPresenceDetail = {
  availability: "online",
  workload: "working",
  runningCount: 1,
  queuedCount: 0,
  capacity: 1,
};

function Wrapper({ children }: { children: ReactNode }) {
  return (
    <I18nProvider locale="en" resources={TEST_RESOURCES}>
      {children}
    </I18nProvider>
  );
}

function renderCard(runtime = RUNTIME, presence = PRESENCE) {
  return render(
    <AgentUsageStatusCard
      runtime={runtime}
      agents={[AGENT]}
      presenceMap={new Map([[AGENT.id, presence]])}
      tasks={[TASK]}
      agentHref={(id) => `/agents/${id}`}
      issueHref={(id) => `/issues/${id}`}
    />,
    { wrapper: Wrapper },
  );
}

describe("AgentUsageStatusCard", () => {
  it("renders native per-agent usage, current work, and reliable provider cost", () => {
    usageState.rows = [
      {
        agent_id: "agent-1",
        provider: "openai",
        model: "gpt-5",
        input_tokens: 1_000,
        output_tokens: 200,
        cache_read_tokens: 300,
        cache_write_tokens: 50,
        cost_usd_ticks: 4_200_000_000,
        uncosted_input_tokens: 0,
        uncosted_output_tokens: 0,
        uncosted_cache_read_tokens: 0,
        uncosted_cache_write_tokens: 0,
        task_count: 1,
      },
    ];

    renderCard();

    expect(screen.getByText("Agent usage status")).toBeInTheDocument();
    expect(screen.getByText("Running")).toBeInTheDocument();
    expect(screen.getByText("Issue issue-ab")).toBeInTheDocument();
    expect(screen.getByText("1K")).toBeInTheDocument();
    expect(screen.getByText("$0.42")).toBeInTheDocument();
  });

  it("marks unreachable presence stale and withholds estimated cost", () => {
    usageState.rows = [
      {
        agent_id: "agent-1",
        provider: "openai",
        model: "gpt-5",
        input_tokens: 50,
        output_tokens: 10,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        task_count: 1,
      },
    ];
    renderCard(RUNTIME, { ...PRESENCE, availability: "offline" });

    expect(screen.getByText("Stale data")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
  });

  it("aggregates cache categories but reports cost only for fully priced rows", () => {
    expect(
      aggregateAgentUsage([
        {
          agent_id: "agent-1",
          provider: "openai",
          model: "gpt-5",
          input_tokens: 10,
          output_tokens: 20,
          cache_read_tokens: 30,
          cache_write_tokens: 40,
          cost_usd_ticks: 100,
          uncosted_input_tokens: 1,
          uncosted_output_tokens: 0,
          uncosted_cache_read_tokens: 0,
          uncosted_cache_write_tokens: 0,
          task_count: 1,
        },
      ]),
    ).toEqual({ input: 10, output: 20, cache: 70, reliableCost: null });
  });
});
