"use client";

import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertCircle, Database, Gauge, RefreshCw } from "lucide-react";
import type {
  Agent,
  AgentRuntime,
  RuntimeProviderUsageWindow,
} from "@multica/core/types";
import {
  runtimeProviderUsageOptions,
  useRefreshRuntimeProviderUsage,
  runtimeUsageByAgentOptions,
} from "@multica/core/runtimes";
import { Button } from "@multica/ui/components/ui/button";
import { Skeleton } from "@multica/ui/components/ui/skeleton";
import { cn } from "@multica/ui/lib/utils";
import { useViewingTimezone } from "../../common/use-viewing-timezone";
import { useT } from "../../i18n";
import { antigravityQuotaPool, prioritizeAntigravityWindows } from "./antigravity-usage";

export function AgentUsageSummary({
  agent,
  runtime,
}: {
  agent: Agent;
  runtime: AgentRuntime | null;
}) {
  const { t, i18n } = useT("agents");
  const tz = useViewingTimezone();
  const providerRefresh = useRefreshRuntimeProviderUsage();
  const providerQuery = useQuery({
    ...runtimeProviderUsageOptions(runtime?.id),
    enabled: Boolean(runtime),
  });
  const multicaQuery = useQuery({
    ...runtimeUsageByAgentOptions(runtime?.id ?? "", 7, tz),
    enabled: Boolean(runtime),
  });

  const multicaUsage = useMemo(() => {
    const rows = (multicaQuery.data ?? []).filter(
      (row) => row.agent_id === agent.id,
    );
    return rows.reduce(
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
  }, [agent.id, multicaQuery.data]);

  const [now, setNow] = useState(Date.now);
  const deadline = Date.parse(providerQuery.data?.refresh_available_at ?? "");
  const cooldown = Number.isFinite(deadline) ? Math.max(0, Math.ceil((deadline - now) / 1000)) : 0;
  useEffect(() => {
    setNow(Date.now());
    if (!Number.isFinite(deadline) || deadline <= Date.now()) return;
    const timer = setInterval(() => {
      const current = Date.now();
      setNow(current);
      if (current >= deadline) clearInterval(timer);
    }, 1000);
    return () => clearInterval(timer);
  }, [deadline]);

  const refresh = () => {
    if (cooldown > 0 || providerRefresh.isPending) return;
    if (runtime) providerRefresh.mutate(runtime.id);
    if (runtime) void multicaQuery.refetch();
  };
  const refreshing =
    providerRefresh.isPending || providerQuery.isFetching || multicaQuery.isFetching;
  const locale = i18n.resolvedLanguage ?? i18n.language;
  const usage = providerQuery.data;
  const sourceLabel =
    usage?.source === "official"
      ? t(($) => $.detail.usage.source_official)
      : usage?.source === "derived"
        ? t(($) => $.detail.usage.source_derived)
        : t(($) => $.detail.usage.source_unavailable);
  const refreshFailed =
    (providerRefresh.isError && providerRefresh.variables === runtime?.id) ||
    Boolean(usage?.last_error_code && usage.last_error_code !== "rate_limited");
  const stale = usage?.stale === true || Boolean(
    usage?.observed_at && Date.now() - Date.parse(usage.observed_at) > 15 * 60_000,
  );
  const observedLabel = formatDate(usage?.observed_at, locale, tz, {
    year: "numeric",
    month: "short",
    day: "numeric",
    hour: "2-digit",
    minute: "2-digit",
  });
  const providerUnavailable =
    !runtime ||
    (providerQuery.isError && !usage) ||
    (usage && !["available", "partial"].includes(usage.status) && !usage.windows?.length);
  const quotaWindows = useMemo(
    () =>
      prioritizeUsageWindows(
        usage?.windows ?? [],
        agent.model,
        usage?.provider ?? runtime?.provider,
      ),
    [agent.model, runtime?.provider, usage?.provider, usage?.windows],
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
        <div className="flex items-center gap-2">
          {cooldown > 0 ? (
            <span role="status" className="text-[11px] text-muted-foreground">
              {t(($) => $.detail.usage.refresh_countdown, { seconds: cooldown })}
            </span>
          ) : null}
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            onClick={refresh}
            disabled={!runtime || refreshing || cooldown > 0}
            aria-label={t(($) => $.detail.usage.refresh)}
            title={t(($) => $.detail.usage.refresh)}
          >
            <RefreshCw className={cn("h-3.5 w-3.5", refreshing && "animate-spin")} />
          </Button>
        </div>
      </div>

      <p className="mt-1 text-[11px] text-muted-foreground">
        {t(($) => $.detail.usage.refresh_hint)}
      </p>
      {refreshFailed ? <UnavailableState message={t(($) => $.detail.usage.refresh_failed)} /> : null}
      {stale ? <UnavailableState message={t(($) => $.detail.usage.stale)} /> : null}

      <div className="mt-3 grid gap-3 lg:grid-cols-[minmax(0,2fr)_minmax(220px,1fr)]">
        <div className="min-w-0">
          <div className="flex flex-wrap items-center justify-between gap-2">
            <div className="flex flex-wrap items-center gap-2">
              <p className="text-caption font-medium">
                {runtime?.provider ?? t(($) => $.detail.usage.provider_quota)}
              </p>
              <span className="rounded-full border border-brand/30 bg-brand/10 px-2 py-0.5 text-[11px] font-medium text-brand">
                {t(($) => $.detail.usage.model, {
                  model: agent.model || t(($) => $.detail.usage.model_default),
                })}
              </span>
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

          {providerQuery.isPending && runtime ? (
            <div className="mt-2 grid gap-2 sm:grid-cols-2">
              <Skeleton className="h-14 w-full" />
              <Skeleton className="h-14 w-full" />
            </div>
          ) : providerUnavailable ? (
            <UnavailableState
              message={
                !runtime
                  ? t(($) => $.detail.usage.no_runtime)
                  : runtime.status !== "online" && !usage
                    ? t(($) => $.detail.usage.runtime_offline)
                    : usage?.status === "auth_required"
                      ? t(($) => $.detail.usage.auth_required)
                      : providerQuery.isError || usage?.status === "error"
                          ? t(($) => $.detail.usage.probe_error)
                        : runtime.provider === "claude" && usage?.status === "unavailable"
                          ? t(($) => $.detail.usage.claude_unavailable)
                          : usage?.message || t(($) => $.detail.usage.unavailable)
              }
            />
          ) : (
            <div
              data-provider-quota-scroll
              className="mt-2 flex gap-2 overflow-x-auto pb-1"
            >
              {quotaWindows.map(({ window, current, displayLabel }) => (
                <QuotaWindow
                  key={`${window.id}:${window.resets_at ?? window.label}`}
                  window={window}
                  stale={stale}
                  grouped={usage?.provider === "antigravity"}
                  current={current}
                  displayLabel={usage?.provider === "antigravity"
                    ? antigravityQuotaPool(window.id) === "google"
                      ? t(($) => $.detail.usage.antigravity_google)
                      : antigravityQuotaPool(window.id) === "other"
                        ? t(($) => $.detail.usage.antigravity_third_party)
                        : displayLabel
                    : displayLabel}
                  locale={locale}
                  tz={tz}
                />
              ))}
              {(usage?.windows ?? []).length === 0 ? (
                <UnavailableState message={usage?.message || t(($) => $.detail.usage.unavailable)} />
              ) : null}
            </div>
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
        </div>
      </div>
    </section>
  );
}

function QuotaWindow({
  window,
  current,
  stale,
  displayLabel,
  grouped,
  locale,
  tz,
}: {
  window: RuntimeProviderUsageWindow;
  current: boolean;
  stale: boolean;
  displayLabel: string;
  grouped?: boolean;
  locale: string;
  tz: string;
}) {
  const { t } = useT("agents");
  const expired = Boolean(
    window.resets_at && Date.parse(window.resets_at) <= Date.now(),
  );
  const used = expired ? undefined : window.used_percent;
  const remaining = expired ? undefined : window.remaining_percent;
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
        "w-[220px] shrink-0 rounded-md border px-2.5 py-2",
        stale && "opacity-60",
        current
          ? "border-brand/50 bg-brand/10 ring-1 ring-brand/20"
          : "bg-background",
      )}
    >
      {grouped ? (
        <p className="min-h-8 break-words text-[11px] leading-4 text-muted-foreground" title={displayLabel}>
          {displayLabel}
        </p>
      ) : null}
      <div className="flex min-w-0 items-center gap-1.5">
        <p
          className="min-w-0 flex-1 truncate text-[11px] text-muted-foreground"
          title={[displayLabel, windowLabel].filter(Boolean).join(" · ")}
        >
          {!grouped && displayLabel ? `${displayLabel} · ` : ""}{windowLabel}
        </p>
        {current ? (
          <span className="shrink-0 rounded-full bg-brand/15 px-1.5 py-0.5 text-[10px] font-semibold text-brand">
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
      {used == null && !expired ? (
        <p className="mt-1 text-[10px] text-muted-foreground">
          {t(($) => $.detail.usage.window_value_unavailable)}
        </p>
      ) : null}
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
        {expired
          ? t(($) => $.detail.usage.window_expired)
          : window.resets_at && resetLabel
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
): Array<{
  window: RuntimeProviderUsageWindow;
  current: boolean;
  displayLabel: string;
}> {
  if (windows.length === 0) return [];
  const modelKey = normalizeUsageKey(model);
  const providerKey = normalizeUsageKey(provider);

  if (providerKey === "codex") {
    return prioritizeCodexWindows(windows, model, modelKey);
  }
  if (providerKey === "antigravity") {
    return prioritizeAntigravityWindows(windows, model);
  }
  if (providerKey === "claude") {
    return windows
      .map((window, index) => ({ window, index }))
      .sort(
        (a, b) =>
          usageWindowOrder(a.window) - usageWindowOrder(b.window) ||
          a.index - b.index,
      )
      .map(({ window }) => ({
        window,
        current: true,
        displayLabel: model?.trim() || "Claude Code",
      }));
  }

  const rows = windows.map((window, index) => ({
    window,
    current: usageWindowMatchesModel(window, modelKey),
    displayLabel: window.group || window.id,
    index,
  }));
  if (!rows.some((row) => row.current) && rows[0]) {
    rows[0].current = true;
    if (model?.trim()) rows[0].displayLabel = model.trim();
  }
  return rows
    .sort((a, b) => Number(b.current) - Number(a.current) || a.index - b.index)
    .map(({ window, current, displayLabel }) => ({ window, current, displayLabel }));
}

function prioritizeCodexWindows(
  windows: RuntimeProviderUsageWindow[],
  model: string | undefined,
  modelKey: string,
) {
  const visibleRows = windows
    .map((window, index) => {
      const key = normalizeUsageKey(`${window.id} ${window.group}`);
      const codeReview = key.includes("codereview");
      const sharedCodex = key.includes("codex") &&
        !key.includes("spark") &&
        !codeReview;
      return {
        window,
        sharedCodex,
        explicitModelMatch: !sharedCodex && usageWindowMatchesModel(window, modelKey),
        index,
        visible: !codeReview,
      };
    })
    .filter((row) => row.visible);
  const usesIndependentModelLimit = visibleRows.some(
    (row) => row.explicitModelMatch,
  );
  return visibleRows
    .map((row) => {
      const current = usesIndependentModelLimit
        ? row.explicitModelMatch
        : row.sharedCodex;
      return {
        ...row,
        current,
        displayLabel:
          current && model?.trim()
            ? model.trim()
            : row.window.group || row.window.id,
      };
    })
    .sort(
      (a, b) =>
        Number(b.current) - Number(a.current) ||
        usageWindowOrder(a.window) - usageWindowOrder(b.window) ||
        a.index - b.index,
    )
    .map(({ window, current, displayLabel }) => ({ window, current, displayLabel }));
}

function usageWindowMatchesModel(
  window: RuntimeProviderUsageWindow,
  modelKey: string,
) {
  if (!modelKey) return false;
  return [window.id, window.group, window.label].some((value) => {
    const candidate = normalizeUsageKey(value);
    return (
      candidate.length > 0 &&
      (candidate.includes(modelKey) || modelKey.includes(candidate))
    );
  });
}

function usageWindowOrder(window: RuntimeProviderUsageWindow) {
  if (window.window_duration_mins === 300) return 0;
  if (window.window_duration_mins === 10080) return 1;
  return 2;
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
