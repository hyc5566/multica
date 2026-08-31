"use client";

import { useEffect, useMemo, useRef } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, Database, Gauge, RefreshCw } from "lucide-react";
import type {
  Agent,
  AgentRuntime,
  RuntimeProviderUsage,
  RuntimeProviderUsageWindow,
} from "@multica/core/types";
import {
  runtimeProviderUsageOptions,
  runtimeUsageByAgentOptions,
} from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import { useT } from "../../i18n";

export function AgentUsageSummary({
  agent,
  runtime,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
}) {
  const { t, i18n } = useT("agents");
  const tz = useViewingTimezone();
  const online = runtime?.status === "online";
  const providerQuery = useQuery({
    ...runtimeProviderUsageOptions(online ? runtime.id : null),
    enabled: Boolean(runtime && online),
  });
  const multicaQuery = useQuery({
    ...runtimeUsageByAgentOptions(runtime?.id ?? "", 7, tz),
    enabled: Boolean(runtime),
  });

  const multicaUsage = useMemo(() => {
    const rows = (multicaQuery.data ?? []).filter(
      (row) => row.agent_id === agent.id,
    );
    const totals = rows.reduce(
      (total, row) => ({
        tokens:
          total.tokens +
          row.input_tokens +
          row.output_tokens +
          row.cache_read_tokens +
          row.cache_write_tokens,
        tasks: total.tasks + row.task_count,
      }),
      { tokens: 0, tasks: 0 },
    );
    const models = Array.from(
      new Set(
        rows
          .filter((row) => row.model.trim().length > 0)
          .map((row) =>
            row.provider ? `${row.provider}/${row.model}` : row.model,
          ),
      ),
    ).sort();
    return { ...totals, models };
  }, [agent.id, multicaQuery.data]);

  const refresh = () => {
    if (online) void providerQuery.refetch();
    if (runtime) void multicaQuery.refetch();
  };
  const refreshing = providerQuery.isFetching || multicaQuery.isFetching;
  const locale = i18n.resolvedLanguage ?? i18n.language;
  const latestUsage = providerQuery.data;
  const latestIsGood =
    latestUsage != null &&
    ["available", "partial"].includes(latestUsage.status) &&
    (latestUsage.windows?.length ?? 0) > 0;
  const lastGoodRef = useRef<{
    runtimeId: string;
    usage: RuntimeProviderUsage;
  } | null>(null);
  useEffect(() => {
    if (runtime && latestUsage && latestIsGood) {
      lastGoodRef.current = { runtimeId: runtime.id, usage: latestUsage };
    }
  }, [latestIsGood, latestUsage, runtime]);
  const previousGood =
    runtime && lastGoodRef.current?.runtimeId === runtime.id
      ? lastGoodRef.current.usage
      : null;
  const usage = latestIsGood ? latestUsage : previousGood;
  const showingLastGood = usage != null && usage !== latestUsage;
  const refreshFailureMessage =
    latestUsage?.status === "rate_limited"
      ? t(($) => $.detail.usage.rate_limited, {
          seconds: latestUsage.retry_after_seconds ?? 1,
        })
      : latestUsage?.status === "auth_required"
        ? t(($) => $.detail.usage.auth_required)
        : providerQuery.isError || latestUsage?.status === "error"
          ? t(($) => $.detail.usage.probe_error)
          : latestUsage?.message || t(($) => $.detail.usage.unavailable);
  const sourceLabel =
    usage?.source === "official"
      ? t(($) => $.detail.usage.source_official)
      : usage?.source === "derived"
        ? t(($) => $.detail.usage.source_derived)
        : t(($) => $.detail.usage.source_unavailable);
  const observedLabel = formatDate(usage?.observed_at, locale, tz, {
    hour: "2-digit",
    minute: "2-digit",
  });
  const providerUnavailable =
    !runtime ||
    !online ||
    (!usage &&
      (providerQuery.isError ||
        (latestUsage != null &&
          !["available", "partial"].includes(latestUsage.status))));
  const quotaWindows = useMemo(
    () =>
      prioritizeUsageWindows(
        usage?.windows ?? [],
        agent.model,
        runtime?.provider,
      ),
    [agent.model, runtime?.provider, usage?.windows],
  );

  return (
    <section
      className="mt-4 rounded-lg border bg-muted/20 p-3"
      aria-label={t(($) => $.detail.usage.title)}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="flex min-w-0 items-center gap-2">
          <Gauge className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
          <h2 className="text-caption font-semibold">
            {t(($) => $.detail.usage.title)}
          </h2>
          {usage?.source ? (
            <span className="rounded-full border bg-background px-1.5 py-0.5 text-[10px] uppercase tracking-wide text-muted-foreground">
              {t(($) => $.detail.usage.source, { source: sourceLabel })}
            </span>
          ) : null}
        </div>
        <Button
          type="button"
          variant="ghost"
          size="icon-sm"
          onClick={refresh}
          disabled={!runtime || refreshing}
          aria-label={t(($) => $.detail.usage.refresh)}
          title={t(($) => $.detail.usage.refresh)}
        >
          <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} />
        </Button>
      </div>

      <div className="mt-3 grid gap-3 lg:grid-cols-[minmax(0,2fr)_minmax(220px,1fr)]">
        <div className="min-w-0">
          <div className="flex flex-wrap items-baseline justify-between gap-1">
            <div className="min-w-0">
              <p className="text-caption font-medium">
                {runtime?.provider ?? t(($) => $.detail.usage.provider_quota)}
              </p>
              {runtime ? (
                <p className="truncate text-[11px] text-muted-foreground">
                  {t(($) => $.detail.usage.model, {
                    model:
                      agent.model || t(($) => $.detail.usage.model_default),
                  })}
                  {runtime.profile_id
                    ? ` · ${t(($) => $.detail.usage.profile, {
                        profile: runtime.profile_id,
                      })}`
                    : ""}
                  {usage?.account_scope
                    ? ` · ${t(($) => $.detail.usage.account_scope, {
                        scope: usage.account_scope,
                      })}`
                    : ` · ${t(($) => $.detail.usage.provider_scope)}`}
                </p>
              ) : null}
            </div>
            {usage?.observed_at && observedLabel ? (
              <time
                dateTime={usage.observed_at}
                title={usage.observed_at}
                className="text-[11px] text-muted-foreground"
              >
                {t(($) => $.detail.usage.updated_at, {
                  when: observedLabel,
                })}
              </time>
            ) : null}
          </div>

          {providerQuery.isPending && online ? (
            <div className="mt-2 grid gap-2 sm:grid-cols-2">
              <Skeleton className="h-14 w-full" />
              <Skeleton className="h-14 w-full" />
            </div>
          ) : providerUnavailable ? (
            <UnavailableState
              message={
                !runtime
                  ? t(($) => $.detail.usage.no_runtime)
                  : !online
                    ? t(($) => $.detail.usage.runtime_offline)
                    : refreshFailureMessage
              }
            />
          ) : (
            <>
              {showingLastGood ? (
                <div className="mt-2 flex items-start gap-2 rounded-md border border-amber-500/30 bg-amber-500/10 px-2.5 py-2 text-[11px] leading-relaxed text-amber-900 dark:text-amber-100">
                  <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
                  <span>
                    {t(($) => $.detail.usage.showing_last_good, {
                      reason: refreshFailureMessage,
                    })}
                  </span>
                </div>
              ) : null}
              <div className="mt-2 grid gap-2 sm:grid-cols-2 xl:grid-cols-4">
                {quotaWindows.map(({ window, current }) => (
                  <QuotaWindow
                    key={window.id}
                    window={window}
                    locale={locale}
                    tz={tz}
                    current={current}
                  />
                ))}
                {(usage?.windows ?? []).length === 0 ? (
                  <UnavailableState message={usage?.message || t(($) => $.detail.usage.unavailable)} />
                ) : null}
              </div>
            </>
          )}
        </div>

        <div className="border-t pt-3 lg:border-l lg:border-t-0 lg:pl-3 lg:pt-0">
          <div className="flex items-center gap-1.5 text-caption font-medium">
            <Database className="h-3.5 w-3.5 text-muted-foreground" aria-hidden="true" />
            {t(($) => $.detail.usage.multica_7d)}
          </div>
          {multicaQuery.isPending && runtime ? (
            <Skeleton className="mt-2 h-10 w-full" />
          ) : (
            <div className="mt-2 flex flex-wrap gap-x-5 gap-y-1">
              <Metric value={formatCompact(multicaUsage.tokens, locale)} label={t(($) => $.detail.usage.tokens)} />
              <Metric value={formatCompact(multicaUsage.tasks, locale)} label={t(($) => $.detail.usage.runs)} />
            </div>
          )}
          <p className="mt-1.5 text-[11px] leading-relaxed text-muted-foreground">
            {t(($) => $.detail.usage.multica_scope_hint)}
          </p>
          <p className="mt-1 text-[11px] leading-relaxed text-muted-foreground">
            {t(($) => $.detail.usage.actual_models_7d, {
              models:
                multicaUsage.models.length > 0
                  ? multicaUsage.models.join(", ")
                  : t(($) => $.detail.usage.no_actual_model),
            })}
          </p>
        </div>
      </div>
    </section>
  );
}

function QuotaWindow({
  window,
  locale,
  tz,
  current,
}: {
  window: RuntimeProviderUsageWindow;
  locale: string;
  tz: string;
  current: boolean;
}) {
  const { t } = useT("agents");
  const used = window.used_percent;
  const remaining = window.remaining_percent;
  const resetLabel = formatDate(window.resets_at, locale, tz, {
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
  const windowLabel =
    window.window_duration_mins === 300
      ? t(($) => $.detail.usage.window_5h)
      : window.window_duration_mins === 10080
        ? t(($) => $.detail.usage.window_weekly)
        : window.label;
  return (
    <div
      data-current-model-usage={current ? "true" : "false"}
      className={cn(
        "rounded-md border px-2.5 py-2",
        current
          ? "border-brand/50 bg-brand/10 ring-1 ring-brand/20"
          : "bg-background",
      )}
    >
      <div className="flex min-w-0 items-center gap-1.5">
        <p className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground" title={[window.group, windowLabel].filter(Boolean).join(" · ")}>
          {window.group ? `${window.group} · ` : ""}{windowLabel}
        </p>
        {current ? (
          <span className="shrink-0 rounded-full bg-brand/15 px-1.5 py-0.5 text-[9px] font-semibold text-brand">
            {t(($) => $.detail.usage.current_model)}
          </span>
        ) : null}
      </div>
      <div className="mt-1 flex items-baseline justify-between gap-2">
        <span className="text-body font-semibold tabular-nums">
          {used == null
            ? "—"
            : t(($) => $.detail.usage.used, { value: Math.round(used) })}
        </span>
        <span className="text-[11px] text-muted-foreground">
          {remaining == null
            ? t(($) => $.detail.usage.remaining_unknown)
            : t(($) => $.detail.usage.remaining, { value: Math.round(remaining) })}
        </span>
      </div>
      <div className="mt-1.5 h-1.5 overflow-hidden rounded-full bg-muted">
        {used == null ? null : (
          <div
            className={cn(
              "h-full rounded-full",
              used >= 90 ? "bg-destructive" : used >= 70 ? "bg-amber-500" : "bg-primary",
            )}
            style={{ width: `${Math.max(1, Math.min(100, used))}%` }}
          />
        )}
      </div>
      <p className="mt-1.5 truncate text-[10px] text-muted-foreground">
        {window.resets_at && resetLabel
          ? t(($) => $.detail.usage.resets, {
              when: resetLabel,
            })
          : t(($) => $.detail.usage.reset_unknown)}
      </p>
    </div>
  );
}

export function prioritizeUsageWindows(
  windows: RuntimeProviderUsageWindow[],
  model: string | undefined,
  provider: string | undefined,
): Array<{ window: RuntimeProviderUsageWindow; current: boolean }> {
  if (windows.length === 0) return [];
  const modelKey = normalizeUsageKey(model);
  const explicit = windows.map((window) =>
    modelKey.length > 0 &&
    [window.id, window.group, window.label].some((value) => {
      const candidate = normalizeUsageKey(value);
      return candidate.length > 0 &&
        (candidate.includes(modelKey) || modelKey.includes(candidate));
    }),
  );
  const hasExplicit = explicit.some(Boolean);
  const providerKey = normalizeUsageKey(provider);
  const currentFlags = hasExplicit
    ? explicit
    : windows.map((window, index) => {
        if (!providerKey) return index === 0;
        return [window.id, window.group].some((value) =>
          normalizeUsageKey(value).includes(providerKey),
        );
      });
  if (!currentFlags.some(Boolean)) currentFlags[0] = true;

  return windows
    .map((window, index) => ({ window, current: currentFlags[index] ?? false, index }))
    .sort((a, b) => Number(b.current) - Number(a.current) || a.index - b.index)
    .map(({ window, current }) => ({ window, current }));
}

function normalizeUsageKey(value: string | undefined) {
  return (value ?? "").toLocaleLowerCase().replace(/[^a-z0-9]+/g, "");
}

function UnavailableState({ message }: { message: string }) {
  return (
    <div className="mt-2 flex min-h-14 items-start gap-2 rounded-md border border-dashed bg-background px-2.5 py-2 text-[11px] leading-relaxed text-muted-foreground sm:col-span-2">
      <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>{message}</span>
    </div>
  );
}

function Metric({ value, label }: { value: string; label: string }) {
  return (
    <div>
      <p className="text-body font-semibold tabular-nums">{value}</p>
      <p className="text-[11px] text-muted-foreground">{label}</p>
    </div>
  );
}

function formatCompact(value: number, locale: string) {
  return new Intl.NumberFormat(locale, {
    notation: "compact",
    maximumFractionDigits: 1,
  }).format(value);
}

function formatDate(
  value: string | undefined,
  locale: string,
  timeZone: string,
  options: Intl.DateTimeFormatOptions,
) {
  if (!value) return null;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return null;
  return new Intl.DateTimeFormat(locale, { ...options, timeZone }).format(date);
}
