import { beforeEach, describe, expect, it, vi } from "vitest";
import { resolveRuntimeProviderUsage } from "./provider-usage";

const getProviderUsageSnapshot = vi.fn();

vi.mock("../api", () => ({
  api: {
    getProviderUsageSnapshot: (runtimeId: string) =>
      getProviderUsageSnapshot(runtimeId),
  },
}));

beforeEach(() => {
  getProviderUsageSnapshot.mockReset();
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
