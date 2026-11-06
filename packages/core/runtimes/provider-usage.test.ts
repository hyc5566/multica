// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { resolveRuntimeProviderUsage, refreshRuntimeProviderUsage } from "./provider-usage";

const getProviderUsageSnapshot = vi.fn();
const initiateProviderUsage = vi.fn();
const getProviderUsageResult = vi.fn();

vi.mock("../api", () => ({
  api: {
    initiateProviderUsage: (id: string) => initiateProviderUsage(id),
    getProviderUsageResult: (id: string, request: string) => getProviderUsageResult(id, request),
    getProviderUsageSnapshot: (runtimeId: string) =>
      getProviderUsageSnapshot(runtimeId),
  },
}));

beforeEach(() => {
  vi.resetAllMocks();
});

describe("resolveRuntimeProviderUsage", () => {
  it("reads the durable server snapshot without initiating a probe", async () => {
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
    };
    getProviderUsageSnapshot.mockResolvedValue(snapshot);

    await expect(resolveRuntimeProviderUsage("rt-1")).resolves.toEqual(snapshot);
    expect(getProviderUsageSnapshot).toHaveBeenCalledWith("rt-1");
  });

  it("preserves unavailable as data instead of fabricating zero", async () => {
    const snapshot = {
      provider: "claude",
      status: "unavailable" as const,
      source: "unavailable" as const,
      observed_at: "2026-08-27T01:00:00Z",
      message: "No structured account quota source.",
    };
    getProviderUsageSnapshot.mockResolvedValue(snapshot);

    const result = await resolveRuntimeProviderUsage("rt-1");
    expect(result).toEqual(snapshot);
    expect(result.windows).toBeUndefined();
  });

  it("surfaces cache read failures", async () => {
    getProviderUsageSnapshot.mockRejectedValue(new Error("snapshot failed"));
    await expect(resolveRuntimeProviderUsage("rt-1")).rejects.toThrow("snapshot failed");
  });
});

afterEach(() => vi.useRealTimers());

describe("refreshRuntimeProviderUsage", () => {
  const snapshot = { provider: "codex", status: "available", source: "official", observed_at: "2026-09-10T05:00:00Z" };
  it("posts a request, waits for completion, then reads durable failure/freshness metadata", async () => {
    vi.useFakeTimers();
    initiateProviderUsage.mockResolvedValue({ id: "req-1", status: "pending" });
    getProviderUsageResult.mockResolvedValueOnce({ id: "req-1", status: "running" })
      .mockResolvedValueOnce({ id: "req-1", status: "completed", provider_usage: snapshot });
    getProviderUsageSnapshot.mockResolvedValue(snapshot);
    const result = refreshRuntimeProviderUsage("rt-1");
    await vi.runAllTimersAsync();
    await expect(result).resolves.toEqual(snapshot);
    expect(initiateProviderUsage).toHaveBeenCalledWith("rt-1");
    expect(getProviderUsageResult).toHaveBeenLastCalledWith("rt-1", "req-1");
    expect(getProviderUsageSnapshot).toHaveBeenCalledTimes(1);
  });
  it("preserves throttled stale results without fabricating a fresh observation", async () => {
    const stale = { ...snapshot, stale: true, last_error_code: "error" };
    initiateProviderUsage.mockResolvedValue({ status: "completed", provider_usage: stale });
    getProviderUsageSnapshot.mockResolvedValue(stale);
    await expect(refreshRuntimeProviderUsage("rt-1")).resolves.toEqual(stale);
    expect(getProviderUsageResult).not.toHaveBeenCalled();
  });
  it.each(["failed", "timeout", "unknown", "completed"])("rejects %s or malformed completion", async (status) => {
    initiateProviderUsage.mockResolvedValue({ status });
    await expect(refreshRuntimeProviderUsage("rt-1")).rejects.toThrow("refresh failed");
    expect(getProviderUsageSnapshot).not.toHaveBeenCalled();
  });
  it("stops polling a request that never completes", async () => {
    vi.useFakeTimers();
    initiateProviderUsage.mockResolvedValue({ id: "req-1", status: "pending" });
    getProviderUsageResult.mockResolvedValue({ id: "req-1", status: "running" });
    const assertion = expect(refreshRuntimeProviderUsage("rt-1")).rejects.toThrow("timed out");
    await vi.runAllTimersAsync();
    await assertion;
  });
  it("propagates transport errors", async () => {
    initiateProviderUsage.mockRejectedValue(new Error("offline"));
    await expect(refreshRuntimeProviderUsage("rt-1")).rejects.toThrow("offline");
  });
});

import { RuntimeProviderUsageSchema } from "../api/schemas";

describe("provider usage cooldown contract", () => {
  const snapshot = { provider: "codex", status: "available", source: "official", observed_at: "2026-09-10T05:00:00Z" };
  it("accepts a server deadline and rejects malformed timestamps", () => {
    expect(RuntimeProviderUsageSchema.safeParse({ ...snapshot, refresh_available_at: "2026-09-10T05:01:00Z" }).success).toBe(true);
    for (const deadline of ["tomorrow", "", 60, null]) {
      expect(RuntimeProviderUsageSchema.safeParse({ ...snapshot, refresh_available_at: deadline }).success).toBe(false);
    }
  });
  it("returns a shared cooldown with the retained snapshot without polling", async () => {
    const limited = { ...snapshot, refresh_available_at: "2026-09-10T05:01:00Z", last_error_code: "rate_limited" };
    initiateProviderUsage.mockResolvedValue({ status: "completed", provider_usage: limited });
    getProviderUsageSnapshot.mockResolvedValue(limited);
    await expect(refreshRuntimeProviderUsage("rt-1")).resolves.toEqual(limited);
    expect(getProviderUsageResult).not.toHaveBeenCalled();
  });
});
