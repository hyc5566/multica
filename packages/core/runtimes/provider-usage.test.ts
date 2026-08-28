import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RuntimeProviderUsageRequest } from "../types";
import { resolveRuntimeProviderUsage } from "./provider-usage";

const initiateProviderUsage = vi.fn();
const getProviderUsageResult = vi.fn();

vi.mock("../api", () => ({
  api: {
    initiateProviderUsage: (runtimeId: string, agentId: string) =>
      initiateProviderUsage(runtimeId, agentId),
    getProviderUsageResult: (runtimeId: string, requestId: string) =>
      getProviderUsageResult(runtimeId, requestId),
  },
}));

function request(
  overrides: Partial<RuntimeProviderUsageRequest>,
): RuntimeProviderUsageRequest {
  return {
    id: "req-usage",
    runtime_id: "rt-1",
    status: "pending",
    created_at: "2026-08-27T00:00:00Z",
    updated_at: "2026-08-27T00:00:00Z",
    ...overrides,
  };
}

beforeEach(() => {
  initiateProviderUsage.mockReset();
  getProviderUsageResult.mockReset();
});

describe("resolveRuntimeProviderUsage", () => {
  it("polls until a structured provider snapshot is available", async () => {
    const snapshot = {
      provider: "codex",
      status: "available" as const,
      source: "official" as const,
      observed_at: "2026-08-27T01:00:00Z",
      windows: [
        {
          id: "weekly",
          label: "Weekly limit",
          used_percent: 20,
          remaining_percent: 80,
          unit: "percent",
        },
      ],
      context: {
        scope: "active_task" as const,
        status: "available" as const,
        source: "official" as const,
        active_task_count: 1,
        used_tokens: 125_274,
        max_tokens: 258_400,
        remaining_tokens: 133_126,
        used_percent: 48.48,
        observed_at: "2026-08-27T01:00:01Z",
      },
    };
    initiateProviderUsage.mockResolvedValue(request({ status: "pending" }));
    getProviderUsageResult.mockResolvedValue(
      request({ status: "completed", provider_usage: snapshot }),
    );

    await expect(resolveRuntimeProviderUsage("rt-1", "agent-1")).resolves.toEqual(snapshot);
    expect(initiateProviderUsage).toHaveBeenCalledWith("rt-1", "agent-1");
    expect(getProviderUsageResult).toHaveBeenCalledWith("rt-1", "req-usage");
  });

  it("preserves unavailable as data instead of fabricating zero", async () => {
    const snapshot = {
      provider: "claude",
      status: "unavailable" as const,
      source: "unavailable" as const,
      observed_at: "2026-08-27T01:00:00Z",
      message: "No structured account quota source.",
    };
    initiateProviderUsage.mockResolvedValue(
      request({ status: "completed", provider_usage: snapshot }),
    );

    const result = await resolveRuntimeProviderUsage("rt-1", "agent-1");
    expect(result).toEqual(snapshot);
    expect(result.windows).toBeUndefined();
  });

  it("surfaces request and malformed-daemon failures", async () => {
    initiateProviderUsage.mockResolvedValue(
      request({ status: "failed", error: "runtime report failed" }),
    );
    await expect(resolveRuntimeProviderUsage("rt-1", "agent-1")).rejects.toThrow(
      "runtime report failed",
    );

    initiateProviderUsage.mockResolvedValue(
      request({ status: "completed", provider_usage: undefined }),
    );
    await expect(resolveRuntimeProviderUsage("rt-1", "agent-1")).rejects.toThrow(
      /provider usage failed/,
    );
  });
});
