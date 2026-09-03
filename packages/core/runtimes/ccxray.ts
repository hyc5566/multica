import type { AgentRuntime } from "../types";
import type { RuntimeHealth } from "./types";

export const CCXRAY_SUPPORTED_PROVIDERS = ["claude", "codex", "grok"] as const;

export type CCXRayHealthStatus =
  | "disabled"
  | "not_installed"
  | "observing"
  | "degraded";

export type CCXRayDisplayStatus =
  | "unsupported"
  | "disabled"
  | "not_installed"
  | "observing"
  | "degraded"
  | "offline";

export interface CCXRayHealthSummary {
  enabled: boolean;
  installed: boolean;
  status: CCXRayHealthStatus;
  version: string;
  observed_at: string;
  last_error_code: string;
}

export const CCXRAY_STALE_AFTER_MS = 60_000;
const MAX_VERSION_LENGTH = 64;
const MAX_ERROR_CODE_LENGTH = 64;
const HEALTH_STATUSES = new Set<CCXRayHealthStatus>([
  "disabled",
  "not_installed",
  "observing",
  "degraded",
]);

export function isCCXRaySupportedProvider(provider: string): boolean {
  return (CCXRAY_SUPPORTED_PROVIDERS as readonly string[]).includes(
    provider.trim().toLowerCase(),
  );
}

/**
 * Reads only the bounded ccxray allowlist from runtime metadata. Unknown,
 * malformed, or newer shapes return null so installed clients fail closed
 * without losing the rest of the runtime response.
 */
export function parseCCXRayHealthSummary(
  metadata: Record<string, unknown>,
): CCXRayHealthSummary | null {
  const value = metadata.ccxray;
  if (!value || typeof value !== "object" || Array.isArray(value)) return null;

  const raw = value as Record<string, unknown>;
  if (
    typeof raw.enabled !== "boolean" ||
    typeof raw.installed !== "boolean" ||
    typeof raw.status !== "string" ||
    !HEALTH_STATUSES.has(raw.status as CCXRayHealthStatus) ||
    typeof raw.version !== "string" ||
    raw.version.length > MAX_VERSION_LENGTH ||
    typeof raw.observed_at !== "string" ||
    !Number.isFinite(Date.parse(raw.observed_at)) ||
    typeof raw.last_error_code !== "string" ||
    raw.last_error_code.length > MAX_ERROR_CODE_LENGTH
  ) {
    return null;
  }

  return {
    enabled: raw.enabled,
    installed: raw.installed,
    status: raw.status as CCXRayHealthStatus,
    version: raw.version,
    observed_at: raw.observed_at,
    last_error_code: raw.last_error_code,
  };
}

export function deriveCCXRayDisplayStatus(args: {
  provider: string;
  runtimeHealth: RuntimeHealth;
  metadata: Record<string, unknown>;
  now: number;
}): CCXRayDisplayStatus {
  if (!isCCXRaySupportedProvider(args.provider)) return "unsupported";
  if (args.runtimeHealth !== "online") return "offline";

  const summary = parseCCXRayHealthSummary(args.metadata);
  if (!summary || !summary.enabled || summary.status === "disabled") {
    return "disabled";
  }
  if (!summary.installed || summary.status === "not_installed") {
    return "not_installed";
  }
  if (args.now - Date.parse(summary.observed_at) > CCXRAY_STALE_AFTER_MS) {
    return "degraded";
  }
  return summary.status === "observing" ? "observing" : "degraded";
}

const DISPLAY_SEVERITY: Record<CCXRayDisplayStatus, number> = {
  observing: 0,
  disabled: 1,
  not_installed: 2,
  unsupported: 3,
  degraded: 4,
  offline: 5,
};

export function deriveMachineCCXRayDisplayStatus(args: {
  runtimes: readonly AgentRuntime[];
  machineHealth: RuntimeHealth;
  now: number;
}): CCXRayDisplayStatus {
  if (args.machineHealth !== "online") return "offline";
  if (args.runtimes.length === 0) return "disabled";

  const statuses = args.runtimes.map((runtime) =>
    deriveCCXRayDisplayStatus({
      provider: runtime.provider,
      runtimeHealth: runtime.status === "online" ? "online" : "offline",
      metadata: runtime.metadata,
      now: args.now,
    }),
  );
  const supported = statuses.filter((status) => status !== "unsupported");
  if (supported.length === 0) return "unsupported";
  return supported.reduce((worst, current) =>
    DISPLAY_SEVERITY[current] > DISPLAY_SEVERITY[worst] ? current : worst,
  );
}
