import { queryOptions } from "@tanstack/react-query";
import { api } from "../api";
import type { RuntimeProviderUsage } from "../types";

export const runtimeProviderUsageKeys = {
  all: () => ["runtimes", "provider-usage"] as const,
  forRuntime: (runtimeId: string) =>
    [...runtimeProviderUsageKeys.all(), runtimeId] as const,
};

export async function resolveRuntimeProviderUsage(
  runtimeId: string,
): Promise<RuntimeProviderUsage> {
  return api.getProviderUsageSnapshot(runtimeId);
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
    refetchInterval: 60_000,
    retry: false,
  });
}
