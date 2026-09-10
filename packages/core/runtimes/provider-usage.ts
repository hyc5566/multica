import { queryOptions, useMutation, useQueryClient } from "@tanstack/react-query";
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

// Manual refresh uses the existing throttled daemon request. Normal reads stay
// cache-only; a completed request can still contain an older retained snapshot.
export async function refreshRuntimeProviderUsage(
  runtimeId: string,
): Promise<RuntimeProviderUsage> {
  let request = await api.initiateProviderUsage(runtimeId);
  const started = Date.now();
  while (request.status === "pending" || request.status === "running") {
    // Match the shared introspection store's 30s pending + 60s running budget.
    if (Date.now() - started >= 100_000) {
      throw new Error("provider usage refresh timed out");
    }
    await new Promise((resolve) => setTimeout(resolve, 500));
    request = await api.getProviderUsageResult(runtimeId, request.id);
  }
  if (request.status !== "completed" || !request.provider_usage) {
    throw new Error("provider usage refresh failed");
  }
  // Read the durable result so retained data includes last_error_code/stale.
  return api.getProviderUsageSnapshot(runtimeId);
}

export function useRefreshRuntimeProviderUsage() {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: refreshRuntimeProviderUsage,
    onSuccess: (snapshot, runtimeId) => {
      qc.setQueryData(runtimeProviderUsageKeys.forRuntime(runtimeId), snapshot);
    },
    onSettled: (_data, _error, runtimeId) => qc.invalidateQueries({
      queryKey: runtimeProviderUsageKeys.forRuntime(runtimeId),
    }),
  });
}
