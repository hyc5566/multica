import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { RuntimeProviderUsage } from "../types";

const POLL_INTERVAL_MS = 500;
const POLL_TIMEOUT_MS = 100_000;

export const runtimeProviderUsageKeys = {
  all: () => ["runtimes", "provider-usage"] as const,
  forRuntime: (runtimeId: string) =>
    [...runtimeProviderUsageKeys.all(), runtimeId] as const,
};

export async function resolveRuntimeProviderUsage(
  runtimeId: string,
): Promise<RuntimeProviderUsage> {
  const initial = await api.initiateProviderUsage(runtimeId);
  const startedAt = Date.now();
  let current = initial;
  while (current.status === "pending" || current.status === "running") {
    if (Date.now() - startedAt > POLL_TIMEOUT_MS) {
      throw new Error("provider usage request timed out");
    }
    await new Promise((resolve) => setTimeout(resolve, POLL_INTERVAL_MS));
    current = await api.getProviderUsageResult(runtimeId, initial.id);
  }
  if (current.status !== "completed" || !current.provider_usage) {
    throw new Error(
      current.error || `provider usage failed (status: ${current.status})`,
    );
  }
  return current.provider_usage;
}

export function runtimeProviderUsageOptions(
  runtimeId: string | null | undefined,
) {
  return queryOptions({
    queryKey: runtimeId
      ? runtimeProviderUsageKeys.forRuntime(runtimeId)
      : runtimeProviderUsageKeys.all(),
    queryFn: () => resolveRuntimeProviderUsage(runtimeId as string),
    enabled: Boolean(runtimeId),
    staleTime: 60_000,
    gcTime: 10 * 60_000,
    retry: false,
  });
}
