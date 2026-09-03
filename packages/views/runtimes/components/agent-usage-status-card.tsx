"use client";

import { useMemo } from "react";
import { useQuery } from "@tanstack/react-query";
import type {
  Agent,
  AgentRuntime,
  AgentTask,
  RuntimeUsageByAgent,
} from "@multica/core/types";
import type { AgentPresenceDetail } from "@multica/core/agents";
import {
  deriveCCXRayDisplayStatus,
  deriveRuntimeHealth,
  runtimeUsageByAgentOptions,
} from "@multica/core/runtimes";
import { cn } from "@multica/ui/lib/utils";
import { Activity, Clock3 } from "lucide-react";
import { ActorAvatar } from "../../common/actor-avatar";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import { useT, useTimeAgo } from "../../i18n";
import { AppLink } from "../../navigation";
import { formatTokens, formatUsd } from "../utils";
import { ProviderLogo } from "./provider-logo";

type AgentObservationStatus = "running" | "waiting" | "idle" | "stale";

interface AgentUsageTotals {
  input: number;
  output: number;
  cache: number;
  reliableCost: number | null;
}

export function aggregateAgentUsage(
  rows: readonly RuntimeUsageByAgent[],
): AgentUsageTotals {
  let input = 0;
  let output = 0;
  let cache = 0;
  let costTicks = 0;
  let costReliable = rows.length > 0;

  for (const row of rows) {
    input += row.input_tokens;
    output += row.output_tokens;
    cache += row.cache_read_tokens + row.cache_write_tokens;
    costTicks += row.cost_usd_ticks ?? 0;
    costReliable =
      costReliable &&
      row.cost_usd_ticks !== undefined &&
      row.uncosted_input_tokens === 0 &&
      row.uncosted_output_tokens === 0 &&
      row.uncosted_cache_read_tokens === 0 &&
      row.uncosted_cache_write_tokens === 0;
  }

  return {
    input,
    output,
    cache,
    reliableCost: costReliable ? costTicks / 10_000_000_000 : null,
  };
}

function observationStatus(
  detail: AgentPresenceDetail | undefined,
): AgentObservationStatus {
  if (!detail || detail.availability !== "online") return "stale";
  if (detail.workload === "working") return "running";
  if (detail.workload === "queued") return "waiting";
  return "idle";
}

function currentTask(tasks: readonly AgentTask[]): AgentTask | null {
  return (
    tasks.find((task) => task.status === "running") ??
    tasks.find(
      (task) =>
        task.status === "dispatched" ||
        task.status === "waiting_local_directory" ||
        task.status === "queued",
    ) ??
    null
  );
}

function lastTaskEvent(tasks: readonly AgentTask[]): string | null {
  let latest: string | null = null;
  let latestMs = Number.NEGATIVE_INFINITY;
  for (const task of tasks) {
    for (const value of [
      task.completed_at,
      task.started_at,
      task.dispatched_at,
      task.created_at,
    ]) {
      if (!value) continue;
      const ms = Date.parse(value);
      if (Number.isFinite(ms) && ms > latestMs) {
        latest = value;
        latestMs = ms;
      }
    }
  }
  return latest;
}

const STATUS_CLASSES: Record<AgentObservationStatus, string> = {
  running: "bg-success/10 text-success",
  waiting: "bg-warning/10 text-warning",
  idle: "bg-muted text-muted-foreground",
  stale: "bg-destructive/10 text-destructive",
};

export function AgentUsageStatusCard({
  runtime,
  agents,
  presenceMap,
  tasks,
  agentHref,
  issueHref,
  now = Date.now(),
}: {
  runtime: AgentRuntime;
  agents: Agent[];
  presenceMap: Map<string, AgentPresenceDetail>;
  tasks: AgentTask[];
  agentHref: (agentId: string) => string;
  issueHref: (issueId: string) => string;
  now?: number;
}) {
  const { t } = useT("runtimes");
  const timeAgo = useTimeAgo();
  const tz = useViewingTimezone();
  const { data: usage = [], isLoading } = useQuery(
    runtimeUsageByAgentOptions(runtime.id, 1, tz),
  );
  const ccxrayStatus = deriveCCXRayDisplayStatus({
    provider: runtime.provider,
    runtimeHealth: deriveRuntimeHealth(runtime, now),
    metadata: runtime.metadata,
    now,
  });

  const usageByAgent = useMemo(() => {
    const rows = new Map<string, RuntimeUsageByAgent[]>();
    for (const row of usage) {
      const current = rows.get(row.agent_id);
      if (current) current.push(row);
      else rows.set(row.agent_id, [row]);
    }
    return rows;
  }, [usage]);

  const tasksByAgent = useMemo(() => {
    const rows = new Map<string, AgentTask[]>();
    for (const task of tasks) {
      const current = rows.get(task.agent_id);
      if (current) current.push(task);
      else rows.set(task.agent_id, [task]);
    }
    return rows;
  }, [tasks]);

  return (
    <section className="rounded-lg border bg-card">
      <div className="flex flex-wrap items-center justify-between gap-2 border-b px-4 py-3">
        <div>
          <h3 className="text-body font-semibold">
            {t(($) => $.agent_usage.title)}
          </h3>
          <p className="mt-0.5 text-caption text-muted-foreground">
            {t(($) => $.agent_usage.today_caption)}
          </p>
        </div>
        <span className="inline-flex items-center gap-1.5 rounded-md bg-muted px-2 py-1 text-caption text-muted-foreground">
          <Activity aria-hidden className="size-3.5" />
          {t(($) => $.observation.label)} · {t(($) => $.observation.status[ccxrayStatus])}
        </span>
      </div>

      {agents.length === 0 ? (
        <p className="px-4 py-6 text-center text-caption text-muted-foreground">
          {t(($) => $.detail.no_agents)}
        </p>
      ) : (
        <div className="divide-y">
          {agents.map((agent) => {
            const agentTasks = tasksByAgent.get(agent.id) ?? [];
            const activeTask = currentTask(agentTasks);
            const status = observationStatus(presenceMap.get(agent.id));
            const totals = aggregateAgentUsage(usageByAgent.get(agent.id) ?? []);
            const lastEvent = lastTaskEvent(agentTasks) ?? runtime.last_seen_at;
            return (
              <div
                key={agent.id}
                className="grid gap-3 px-4 py-3 md:grid-cols-[minmax(180px,1.2fr)_minmax(160px,1fr)_minmax(280px,1.5fr)_auto] md:items-center"
              >
                <div className="flex min-w-0 items-center gap-2.5">
                  <ActorAvatar actorType="agent" actorId={agent.id} size="sm" showStatusDot />
                  <div className="min-w-0">
                    <AppLink
                      href={agentHref(agent.id)}
                      className="block truncate text-body font-medium hover:underline"
                    >
                      {agent.name}
                    </AppLink>
                    <span className="mt-0.5 flex items-center gap-1.5 text-caption text-muted-foreground">
                      <ProviderLogo provider={runtime.provider} className="size-3" />
                      <span className="capitalize">{runtime.provider}</span>
                      {agent.model && <span className="truncate">· {agent.model}</span>}
                    </span>
                  </div>
                </div>

                <div className="min-w-0 text-caption">
                  <span
                    className={cn(
                      "inline-flex rounded px-1.5 py-0.5 font-medium",
                      STATUS_CLASSES[status],
                    )}
                  >
                    {t(($) => $.agent_usage.status[status])}
                  </span>
                  {activeTask && (
                    <div className="mt-1 truncate text-muted-foreground">
                      {activeTask.issue_id ? (
                        <AppLink
                          href={issueHref(activeTask.issue_id)}
                          className="hover:text-foreground hover:underline"
                        >
                          {t(($) => $.agent_usage.issue_ref, {
                            id: activeTask.issue_id.slice(0, 8),
                          })}
                        </AppLink>
                      ) : (
                        t(($) => $.agent_usage.task_ref, {
                          id: activeTask.id.slice(0, 8),
                        })
                      )}
                    </div>
                  )}
                </div>

                <dl className="grid grid-cols-4 gap-2 text-caption">
                  <TokenMetric label={t(($) => $.agent_usage.input)} value={totals.input} loading={isLoading} />
                  <TokenMetric label={t(($) => $.agent_usage.output)} value={totals.output} loading={isLoading} />
                  <TokenMetric label={t(($) => $.agent_usage.cache)} value={totals.cache} loading={isLoading} />
                  <div className="min-w-0 text-right">
                    <dt className="text-micro uppercase tracking-wide text-muted-foreground">
                      {t(($) => $.agent_usage.cost)}
                    </dt>
                    <dd className="mt-0.5 font-mono text-caption">
                      {isLoading
                        ? "…"
                        : totals.reliableCost == null
                          ? "—"
                          : formatUsd(totals.reliableCost)}
                    </dd>
                  </div>
                </dl>

                <span className="inline-flex items-center gap-1 text-caption text-muted-foreground md:justify-end">
                  <Clock3 aria-hidden className="size-3" />
                  {lastEvent
                    ? timeAgo(lastEvent)
                    : t(($) => $.agent_usage.never_observed)}
                </span>
              </div>
            );
          })}
        </div>
      )}
    </section>
  );
}

function TokenMetric({
  label,
  value,
  loading,
}: {
  label: string;
  value: number;
  loading: boolean;
}) {
  return (
    <div className="min-w-0 text-right">
      <dt className="text-micro uppercase tracking-wide text-muted-foreground">
        {label}
      </dt>
      <dd className="mt-0.5 font-mono text-caption">
        {loading ? "…" : formatTokens(value)}
      </dd>
    </div>
  );
}
