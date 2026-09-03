// @vitest-environment node

import { describe, expect, it } from "vitest";
import type { AgentRuntime } from "../types";
import {
  deriveCCXRayDisplayStatus,
  deriveMachineCCXRayDisplayStatus,
  parseCCXRayHealthSummary,
} from "./ccxray";

const NOW = Date.parse("2026-09-03T10:00:00Z");

function metadata(overrides: Record<string, unknown> = {}) {
  return {
    ccxray: {
      enabled: true,
      installed: true,
      status: "observing",
      version: "0.1.0",
      observed_at: "2026-09-03T09:59:30Z",
      last_error_code: "",
      ...overrides,
    },
  };
}

function runtime(provider: string, meta = metadata()): AgentRuntime {
  return {
    id: provider,
    workspace_id: "ws-1",
    daemon_id: "daemon-1",
    name: provider,
    runtime_mode: "local",
    provider,
    launch_header: "",
    status: "online",
    device_info: "",
    metadata: meta,
    owner_id: "user-1",
    visibility: "private",
    last_seen_at: "2026-09-03T09:59:50Z",
    created_at: "2026-09-03T09:00:00Z",
    updated_at: "2026-09-03T09:00:00Z",
  };
}

describe("ccxray runtime metadata", () => {
  it("accepts only the bounded health allowlist", () => {
    expect(parseCCXRayHealthSummary(metadata())).toMatchObject({
      enabled: true,
      installed: true,
      status: "observing",
    });
    expect(
      parseCCXRayHealthSummary(metadata({ version: "v".repeat(65) })),
    ).toBeNull();
    expect(
      parseCCXRayHealthSummary(metadata({ status: "capturing_raw_prompts" })),
    ).toBeNull();
  });

  it("covers unsupported, disabled, missing, observing, degraded, stale, and offline states", () => {
    const derive = (
      provider: string,
      meta: Record<string, unknown>,
      runtimeHealth: "online" | "offline" = "online",
    ) =>
      deriveCCXRayDisplayStatus({ provider, metadata: meta, runtimeHealth, now: NOW });

    expect(derive("hermes", metadata())).toBe("unsupported");
    expect(derive("claude", {})).toBe("disabled");
    expect(derive("codex", metadata({ enabled: false, status: "disabled" }))).toBe(
      "disabled",
    );
    expect(
      derive("grok", metadata({ installed: false, status: "not_installed" })),
    ).toBe("not_installed");
    expect(derive("claude", metadata())).toBe("observing");
    expect(derive("claude", metadata({ status: "degraded" }))).toBe("degraded");
    expect(
      derive("claude", metadata({ observed_at: "2026-09-03T09:58:00Z" })),
    ).toBe("degraded");
    expect(derive("claude", metadata(), "offline")).toBe("offline");
  });

  it("uses an observing supported runtime for a mixed-provider machine", () => {
    expect(
      deriveMachineCCXRayDisplayStatus({
        runtimes: [runtime("hermes"), runtime("codex")],
        machineHealth: "online",
        now: NOW,
      }),
    ).toBe("observing");
  });

  it("surfaces the least healthy supported runtime at machine level", () => {
    expect(
      deriveMachineCCXRayDisplayStatus({
        runtimes: [
          runtime("codex"),
          runtime("claude", metadata({ status: "degraded" })),
        ],
        machineHealth: "online",
        now: NOW,
      }),
    ).toBe("degraded");
  });
});
