"use client";

import { useMemo, useState } from "react";
import { Download } from "lucide-react";
import type { AgentTask } from "@multica/core/types";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { Button } from "@multica/ui/components/ui/button";
import { useActorName } from "@multica/core/workspace/hooks";
import { useCustomPricingStore } from "@multica/core/runtimes/custom-pricing-store";
import { ActorAvatar } from "../../common/actor-avatar";
import { useT } from "../../i18n";
import { formatDuration } from "../../agents/components/agent-activity-hover-content";
import {
  collectUnmappedModels,
  formatTokens,
  formatUsd,
  summarizeTaskUsage,
  summarizeTaskUsageAcross,
  type TaskUsageSummary,
} from "../../runtimes/utils";
import { KpiCard } from "../../runtimes/components/shared";
import { useStatusLabel, useTriggerText } from "./task-run-labels";
import { TaskStatusIcon } from "./task-status-icon";
import { compareTaskQuotaCheckpoints } from "./task-quota-comparison";

// Per-run cost breakdown for one issue — the surface the execution log's
// header total opens.
//
// The execution log answers "how much did this run cost" one row at a time;
// this answers "which run cost the most, and why". That comparison needs the
// input / output / cache split side by side, which the 288px sidebar cannot
// hold, so it lives here instead of expanding the rows.
//
// Every figure comes from the same `summarizeTaskUsage` helpers the sidebar
// uses, so the total here can never disagree with the total that opened it.

export function IssueUsageDialog({
  open,
  onOpenChange,
  identifier,
  tasks,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  identifier: string;
  tasks: AgentTask[];
}) {
  const { t } = useT("issues");
  // `estimateCost` reads custom rates imperatively out of the Zustand store,
  // so nothing re-renders this dialog when the user saves a new rate. Subscribe
  // to the snapshot and carry it into every memo that prices usage — otherwise
  // the totals, the per-run costs, and the "unmapped model" notice all keep
  // showing the old price until the task list happens to refetch. Same reason
  // the runtime usage page subscribes in usage-section.tsx.
  const pricings = useCustomPricingStore((s) => s.pricings);

  // Only runs that actually recorded usage earn a row: a run with no figure
  // contributes nothing to compare and would just add an all-em-dash line.
  // Their existence is still accounted for in the footnote below.
  const priced = useMemo(
    () => tasks.filter((task) => (task.usage?.length ?? 0) > 0),
    [tasks],
  );
  const unpricedCount = tasks.length - priced.length;
  const quotaTasks = useMemo(
    () => tasks.filter((task) => (task.quota_checkpoints?.length ?? 0) > 0),
    [tasks],
  );

  const total = useMemo(
    () => summarizeTaskUsageAcross(priced.map((task) => task.usage)),
    [priced, pricings],
  );

  const agentIds = useMemo(
    () => Array.from(new Set(priced.map((task) => task.agent_id).filter(Boolean))),
    [priced],
  );

  // Models with no rate-table entry and no provider-reported cost: their
  // tokens are counted but their spend is not, so the totals below understate
  // reality. Saying so is the difference between an estimate and a wrong number.
  const unmapped = useMemo(
    () => collectUnmappedModels(priced.flatMap((task) => task.usage ?? [])),
    [priced, pricings],
  );

  // Floor, not round: on a cache-heavy issue 99.55% rounds to "100% hit rate",
  // which claims every single token came from cache. Flooring only ever says
  // 100% when it is actually 100%, and one percentage point of pessimism is
  // cheaper than an impossible-looking number.
  const cacheHitRate =
    total && total.input + total.cacheRead > 0
      ? Math.floor((total.cacheRead / (total.input + total.cacheRead)) * 100)
      : 0;

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      {/* DialogContent's base is `sm:max-w-sm`, which the same-specificity
          `max-w-4xl` does not beat — the table then overflows the box instead
          of the box growing. `!` wins it, matching the transcript dialog.
          5xl rather than 4xl because nine columns plus the token bar need
          ~920px: at 4xl the Cost column — the one people open this for —
          landed outside the scroll viewport. */}
      <DialogContent className="!max-h-[calc(100svh-4rem)] !w-[calc(100vw-4rem)] !max-w-5xl overflow-y-auto">
        <DialogHeader>
          <DialogTitle>{t(($) => $.usage_detail.title)}</DialogTitle>
          <DialogDescription>
            {t(($) => $.usage_detail.subtitle, {
              identifier,
              count: priced.length,
            })}
          </DialogDescription>
        </DialogHeader>

        {total == null && quotaTasks.length === 0 ? (
          <p className="py-8 text-center text-body text-muted-foreground">
            {t(($) => $.usage_detail.empty)}
          </p>
        ) : total == null ? (
          <QuotaReport tasks={tasks} identifier={identifier} />
        ) : (
          /* `min-w-0`: DialogContent is a grid, and a grid item defaults to
             `min-width: auto` — it sizes to its content's minimum rather than
             to the track. Without this the wide run table pushes this column
             past the dialog's own max-width, and every sibling (KPI cards,
             by-agent bars, footnotes) stretches with it and paints outside
             the box. */
          <div className="flex min-w-0 flex-col gap-5">
            <div className="grid grid-cols-3 divide-x rounded-lg border bg-card">
              <KpiCard
                label={t(($) => $.usage_detail.kpi_cost)}
                value={formatUsd(total.cost)}
                hint={<CostConcentrationHint tasks={priced} total={total} />}
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_cache)}
                value={formatUsd(total.cacheSavings)}
                accent={total.cacheSavings > 0 ? "success" : "default"}
                hint={t(($) => $.usage_detail.kpi_cache_hint, {
                  pct: cacheHitRate,
                  reads: formatTokens(total.cacheRead),
                })}
              />
              <KpiCard
                label={t(($) => $.usage_detail.kpi_tokens)}
                value={formatTokens(total.tokens)}
                hint={t(($) => $.usage_detail.kpi_tokens_hint, {
                  input: formatTokens(total.input),
                  output: formatTokens(total.output),
                })}
              />
            </div>

            {agentIds.length > 1 && (
              <CostByAgent tasks={priced} agentIds={agentIds} total={total} />
            )}

            <RunTable tasks={priced} total={total} />

            {quotaTasks.length > 0 && (
              <QuotaReport tasks={tasks} identifier={identifier} />
            )}

            <div className="space-y-1 text-micro text-muted-foreground">
              {unpricedCount > 0 && (
                <p>{t(($) => $.usage_detail.note_unpriced, { count: unpricedCount })}</p>
              )}
              {unmapped.length > 0 && (
                <p>
                  {t(($) => $.usage_detail.note_unmapped, { models: unmapped.join(", ") })}
                </p>
              )}
              <p>{t(($) => $.usage_detail.note_estimate)}</p>
            </div>
          </div>
        )}
      </DialogContent>
    </Dialog>
  );
}

function downloadText(filename: string, content: string, type: string) {
  const href = URL.createObjectURL(new Blob([content], { type }));
  const link = document.createElement("a");
  link.href = href;
  link.download = filename;
  link.rel = "noopener";
  document.body.appendChild(link);
  link.click();
  link.remove();
  // Safari aborts the download if the object URL is revoked in the same task.
  setTimeout(() => URL.revokeObjectURL(href), 0);
}

function csvCell(value: unknown): string {
  let text = value == null ? "" : String(value);
  // Spreadsheet programs execute leading formula characters when a CSV is
  // opened. Provider/model/window labels are external strings, so neutralize
  // them before quoting the field.
  if (/^[=+\-@]/.test(text)) text = `'${text}`;
  return `"${text.replaceAll('"', '""')}"`;
}

function runsCSV(tasks: AgentTask[]): string {
  const rows: unknown[][] = [[
    "task_id", "provider", "model", "status", "started_at_utc", "completed_at_utc",
    "timezone", "input_tokens", "output_tokens", "cache_read_tokens", "cache_write_tokens",
  ]];
  for (const task of tasks) {
    for (const usage of task.usage ?? []) {
      rows.push([
        task.id, usage.provider, usage.model, task.status, task.started_at,
        task.completed_at, "UTC", usage.input_tokens, usage.output_tokens,
        usage.cache_read_tokens, usage.cache_write_tokens,
      ]);
    }
  }
  return rows.map((row) => row.map(csvCell).join(",")).join("\r\n");
}

function quotaCheckpointCSV(tasks: AgentTask[]): string {
  const rows: unknown[][] = [[
    "task_id", "phase", "provider", "requested_model", "window_id", "window",
    "scope", "model_match", "used_percent", "remaining_percent", "boundary_at_utc",
    "observed_at_utc", "resets_at_utc", "timezone", "capture_state", "error_code",
    "observation_age_ms", "overlapping_tasks", "observation_id",
  ]];
  for (const task of tasks) {
    for (const checkpoint of task.quota_checkpoints ?? []) {
      const windows = checkpoint.snapshot?.windows ?? [];
      if (windows.length === 0) {
        rows.push([
          task.id, checkpoint.phase, checkpoint.provider, checkpoint.requested_model,
          "", "", "", "", "", "", checkpoint.boundary_at, checkpoint.snapshot?.observed_at,
          "", "UTC", checkpoint.capture_state, checkpoint.error_code,
          checkpoint.observation_age_ms, checkpoint.overlapping_task_count, checkpoint.observation_id,
        ]);
      }
      for (const window of windows) {
        rows.push([
          task.id, checkpoint.phase, checkpoint.provider, checkpoint.requested_model,
          window.id, window.label, window.scope, window.model_match, window.used_percent,
          window.remaining_percent, checkpoint.boundary_at, checkpoint.snapshot?.observed_at,
          window.resets_at, "UTC", checkpoint.capture_state, checkpoint.error_code,
          checkpoint.observation_age_ms, checkpoint.overlapping_task_count, checkpoint.observation_id,
        ]);
      }
    }
  }
  return rows.map((row) => row.map(csvCell).join(",")).join("\r\n");
}

function QuotaReport({ tasks, identifier }: { tasks: AgentTask[]; identifier: string }) {
  const { t } = useT("issues");
  const [showAll, setShowAll] = useState(false);
  const rows = tasks.flatMap((task) =>
    compareTaskQuotaCheckpoints(task.quota_checkpoints).map((window) => ({ task, window })),
  );
  const visibleRows = showAll
    ? rows
    : rows.filter(({ window }) => window.modelMatch === "exact" || window.modelMatch === "shared" || window.scope !== "model");
  const hiddenCount = rows.length - visibleRows.length;
  return (
    <section className="space-y-2" aria-labelledby="task-quota-heading">
      <div className="flex items-center justify-between gap-3">
        <div>
          <h3 id="task-quota-heading" className="text-caption font-medium">
            {t(($) => $.usage_detail.quota_title)}
          </h3>
          <p className="text-micro text-muted-foreground">
            {t(($) => $.usage_detail.quota_attribution_note)}
          </p>
          {!showAll && hiddenCount > 0 && (
            <button
              type="button"
              className="text-micro text-link hover:underline"
              onClick={() => setShowAll(true)}
            >
              {t(($) => $.usage_detail.quota_show_all, { count: hiddenCount })}
            </button>
          )}
        </div>
        <div className="flex shrink-0 gap-1.5">
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => downloadText(
              `${identifier}-quota.json`,
              JSON.stringify(tasks.map(({ id, usage, quota_checkpoints }) => ({ id, usage, quota_checkpoints })), null, 2),
              "application/json;charset=utf-8",
            )}
          >
            <Download className="mr-1 size-3.5" aria-hidden />JSON
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => downloadText(
              `${identifier}-runs.csv`, runsCSV(tasks), "text/csv;charset=utf-8",
            )}
          >
            <Download className="mr-1 size-3.5" aria-hidden />
            {t(($) => $.usage_detail.export_runs_csv)}
          </Button>
          <Button
            type="button"
            variant="outline"
            size="sm"
            onClick={() => downloadText(
              `${identifier}-quota-checkpoints.csv`, quotaCheckpointCSV(tasks), "text/csv;charset=utf-8",
            )}
          >
            <Download className="mr-1 size-3.5" aria-hidden />
            {t(($) => $.usage_detail.export_quota_csv)}
          </Button>
        </div>
      </div>
      <div className="max-h-[32vh] min-w-0 overflow-auto rounded-md border">
        <table className="w-full min-w-[68rem]">
          <thead className="sticky top-0 bg-popover">
            <tr className="text-micro text-muted-foreground [&>th]:whitespace-nowrap [&>th]:px-2 [&>th]:py-1.5 [&>th]:text-right [&>th]:font-normal">
              <th className="!text-left">{t(($) => $.usage_detail.col_run)}</th>
              <th className="!text-left">{t(($) => $.usage_detail.quota_window)}</th>
              <th>{t(($) => $.usage_detail.quota_before)}</th>
              <th>{t(($) => $.usage_detail.quota_after)}</th>
              <th>{t(($) => $.usage_detail.quota_delta)}</th>
              <th className="!text-left">{t(($) => $.usage_detail.quota_observed)}</th>
              <th className="!text-left">{t(($) => $.usage_detail.quota_reset)}</th>
              <th className="!text-left">{t(($) => $.usage_detail.quota_state)}</th>
            </tr>
          </thead>
          <tbody>
            {visibleRows.map(({ task, window }) => (
              <QuotaRow key={`${task.id}:${window.id}`} task={task} window={window} />
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}

function QuotaRow({
  task,
  window,
}: {
  task: AgentTask;
  window: ReturnType<typeof compareTaskQuotaCheckpoints>[number];
}) {
  const { t } = useT("issues");
  const trigger = useTriggerText(task);
  const state = {
    comparable: t(($) => $.usage_detail.quota_state_comparable),
    same_observation: t(($) => $.usage_detail.quota_state_same_observation),
    reset_crossed: t(($) => $.usage_detail.quota_state_reset_crossed),
    stale: t(($) => $.usage_detail.quota_state_stale),
    unavailable: t(($) => $.usage_detail.quota_state_unavailable),
    missing: t(($) => $.usage_detail.quota_state_missing),
  }[window.state];
  const pct = (value: number | undefined) => value == null ? "—" : `${value.toFixed(1)}%`;
  const before = task.quota_checkpoints?.find((checkpoint) => checkpoint.phase === "before");
  const after = task.quota_checkpoints?.find((checkpoint) => checkpoint.phase === "after");
  const overlap = Math.max(
    0,
    ...(task.quota_checkpoints ?? []).map((checkpoint) => checkpoint.overlapping_task_count),
  );
  const shortTime = (value: string | undefined) => value
    ? new Intl.DateTimeFormat(undefined, { dateStyle: "short", timeStyle: "short" }).format(new Date(value))
    : "—";
  return (
    <tr className="text-caption [&>td]:border-t [&>td]:px-2 [&>td]:py-1.5 [&>td]:text-right">
      <td className="max-w-[14rem] !text-left"><span className="block truncate">{trigger}</span></td>
      <td className="!text-left">
        {window.label}
        {window.modelMatch === "exact" && (
          <span className="ml-1 rounded bg-info/10 px-1 py-0.5 text-micro text-info">
            {t(($) => $.usage_detail.quota_requested_model)}
          </span>
        )}
      </td>
      <td className="tabular-nums">{pct(window.before)}</td>
      <td className="tabular-nums">{pct(window.after)}</td>
      <td className="tabular-nums">
        {window.delta == null ? "—" : `${window.delta >= 0 ? "+" : ""}${window.delta.toFixed(1)} pp`}
      </td>
      <td className="!text-left text-micro text-muted-foreground">
        {shortTime(before?.snapshot?.observed_at)} → {shortTime(after?.snapshot?.observed_at)}
      </td>
      <td className="!text-left text-micro text-muted-foreground">{shortTime(window.resetsAt)}</td>
      <td className="!text-left text-muted-foreground">
        {state}
        {overlap > 0 && (
          <span className="ml-1 text-warning">
            · {t(($) => $.usage_detail.quota_overlap, { count: overlap })}
          </span>
        )}
      </td>
    </tr>
  );
}

// "N failed runs account for X%" — the single most useful thing the total can
// say about itself, and the reason this dialog exists. Rendered only when
// there IS a failed run with a cost, so a healthy issue gets no scolding hint.
function CostConcentrationHint({
  tasks,
  total,
}: {
  tasks: AgentTask[];
  total: TaskUsageSummary;
}) {
  const { t } = useT("issues");
  const failed = tasks.filter((task) => task.status === "failed");
  if (failed.length === 0 || total.cost <= 0) return null;

  const failedCost = failed.reduce(
    (sum, task) => sum + (summarizeTaskUsage(task.usage)?.cost ?? 0),
    0,
  );
  if (failedCost <= 0) return null;

  return (
    <span className="text-warning">
      {t(($) => $.usage_detail.kpi_cost_hint, {
        count: failed.length,
        pct: Math.round((failedCost / total.cost) * 100),
      })}
    </span>
  );
}

// Same bar language as the runtime usage page's "Cost by agent" block: one row
// per agent, bar scaled to the biggest spender. Hidden entirely for a
// single-agent issue, where a one-bar chart says nothing the total didn't.
function CostByAgent({
  tasks,
  agentIds,
  total,
}: {
  tasks: AgentTask[];
  agentIds: string[];
  total: TaskUsageSummary;
}) {
  const { t } = useT("issues");
  const { getActorName } = useActorName();
  const pricings = useCustomPricingStore((s) => s.pricings);

  const rows = useMemo(() => {
    return agentIds
      .map((agentId) => {
        const own = tasks.filter((task) => task.agent_id === agentId);
        const summary = summarizeTaskUsageAcross(own.map((task) => task.usage));
        return { agentId, cost: summary?.cost ?? 0, tokens: summary?.tokens ?? 0 };
      })
      .toSorted((a, b) => b.cost - a.cost);
  }, [agentIds, tasks, pricings]);

  const maxCost = rows.reduce((m, r) => Math.max(m, r.cost), 0);

  return (
    <div>
      <div className="mb-2 text-caption font-medium">
        {t(($) => $.usage_detail.by_agent)}
      </div>
      <div className="space-y-2">
        {rows.map((row) => (
          <div
            key={row.agentId}
            className="grid grid-cols-[minmax(0,1fr)_minmax(0,2fr)_5rem_4rem] items-center gap-3"
          >
            <div className="flex min-w-0 items-center gap-2">
              <ActorAvatar actorType="agent" actorId={row.agentId} size="sm" enableHoverCard />
              <span className="truncate text-caption">
                {getActorName("agent", row.agentId)}
              </span>
            </div>
            <div className="relative h-2 overflow-hidden rounded-full bg-muted">
              <div
                className="h-full rounded-full bg-chart-1"
                style={{ width: `${maxCost > 0 ? (row.cost / maxCost) * 100 : 0}%` }}
              />
            </div>
            <div className="text-right text-caption tabular-nums text-muted-foreground">
              {formatTokens(row.tokens)}
            </div>
            <div className="text-right text-caption font-medium tabular-nums">
              {formatUsd(row.cost)}
            </div>
          </div>
        ))}
      </div>
      <div className="sr-only">
        {t(($) => $.usage_detail.by_agent_total, { cost: formatUsd(total.cost) })}
      </div>
    </div>
  );
}

function RunTable({ tasks, total }: { tasks: AgentTask[]; total: TaskUsageSummary }) {
  const { t } = useT("issues");
  const maxTokens = tasks.reduce(
    (m, task) => Math.max(m, summarizeTaskUsage(task.usage)?.tokens ?? 0),
    0,
  );

  return (
    // Nine columns of numbers have a floor width; on a narrow window they
    // scroll sideways rather than squeezing the trigger text to nothing or
    // pushing the totals outside the dialog.
    //
    // `min-w-0` is what makes `overflow-auto` actually clip: as a flex item
    // this box defaults to `min-width: auto`, so it grows to the table's
    // min-content width and never scrolls. Removing it puts the table back
    // outside the dialog.
    <div className="max-h-[45vh] min-w-0 overflow-auto">
      <table className="w-full min-w-[46rem]">
        <thead className="sticky top-0 bg-popover">
          <tr className="text-micro text-muted-foreground [&>th]:whitespace-nowrap [&>th]:px-2 [&>th]:pb-1.5 [&>th]:text-right [&>th]:font-normal">
            <th className="!pl-0 !text-left">{t(($) => $.usage_detail.col_run)}</th>
            <th>{t(($) => $.usage_detail.col_model)}</th>
            <th>{t(($) => $.usage_detail.col_duration)}</th>
            <th>{t(($) => $.usage_detail.col_input)}</th>
            <th>{t(($) => $.usage_detail.col_output)}</th>
            <th>{t(($) => $.usage_detail.col_cache_read)}</th>
            <th>{t(($) => $.usage_detail.col_cache_write)}</th>
            <th className="!pr-16">{t(($) => $.usage_detail.col_tokens)}</th>
            <th className="!pr-0">{t(($) => $.usage_detail.col_cost)}</th>
          </tr>
        </thead>
        <tbody>
          {tasks.map((task) => (
            <RunRow key={task.id} task={task} maxTokens={maxTokens} />
          ))}
        </tbody>
        <tfoot>
          <tr className="text-caption font-medium tabular-nums [&>td]:whitespace-nowrap [&>td]:border-t [&>td]:border-input [&>td]:px-2 [&>td]:py-2 [&>td]:text-right">
            <td className="!pl-0 !text-left">{t(($) => $.usage_detail.total)}</td>
            <td colSpan={2} />
            <td>{formatTokens(total.input)}</td>
            <td>{formatTokens(total.output)}</td>
            <td>{formatTokens(total.cacheRead)}</td>
            <td>{formatTokens(total.cacheWrite)}</td>
            <td className="!pr-16">{formatTokens(total.tokens)}</td>
            <td className="!pr-0">{formatUsd(total.cost)}</td>
          </tr>
        </tfoot>
      </table>
    </div>
  );
}

function RunRow({ task, maxTokens }: { task: AgentTask; maxTokens: number }) {
  const { t } = useT("issues");
  const trigger = useTriggerText(task);
  const statusLabel = useStatusLabel(task.status);
  const summary = summarizeTaskUsage(task.usage);
  if (!summary) return null;

  const duration =
    task.started_at && task.completed_at
      ? formatDuration(task.started_at, new Date(task.completed_at).getTime())
      : "";

  return (
    <tr className="text-caption tabular-nums transition-colors hover:bg-accent/40 [&>td]:whitespace-nowrap [&>td]:border-t [&>td]:px-2 [&>td]:py-1.5 [&>td]:text-right">
      <td className="!pl-0 !text-left">
        <div className="flex items-center gap-2">
          <ActorAvatar actorType="agent" actorId={task.agent_id} size="sm" enableHoverCard />
          <span className="max-w-[13rem] truncate">{trigger}</span>
          {task.status === "running" ? (
            <span className="inline-flex shrink-0 items-center gap-1 text-micro text-info">
              <span className="h-1.5 w-1.5 rounded-full bg-info" />
              {t(($) => $.execution_log.status_running)}
            </span>
          ) : (
            <>
              {/* TaskStatusIcon is aria-hidden, so the glyph alone leaves a
                  screen reader with no way to tell a failed run from a
                  completed one. Same sr-only pairing the execution log rows
                  use. */}
              <TaskStatusIcon status={task.status} />
              <span className="sr-only">{statusLabel}</span>
            </>
          )}
        </div>
      </td>
      {/* A run that spilled across models lists them all, and real model ids
          are long (`claude-haiku-4-5-20251001, claude-opus-5[1m]`). Left
          uncapped that single cell sets the table's width and forces every
          other issue's dialog to scroll sideways, so cap it and keep the full
          list in the title. `whitespace-nowrap` comes from the row, so the
          cell needs its own width bound for `truncate` to have anything to
          truncate against. */}
      <td className="max-w-[11rem] text-micro text-muted-foreground">
        <span className="block truncate" title={summary.models.join(", ")}>
          {summary.models.join(", ") || "—"}
        </span>
      </td>
      <td className="text-muted-foreground">{duration || "—"}</td>
      <td>{formatTokens(summary.input)}</td>
      <td>{formatTokens(summary.output)}</td>
      <td>{formatTokens(summary.cacheRead)}</td>
      <td>{formatTokens(summary.cacheWrite)}</td>
      <td className="!pr-2">
        <div className="flex items-center justify-end gap-2">
          <span>{formatTokens(summary.tokens)}</span>
          <span className="relative h-1 w-14 overflow-hidden rounded-full bg-muted">
            <span
              className="absolute inset-y-0 left-0 rounded-full bg-chart-1"
              style={{ width: `${maxTokens > 0 ? (summary.tokens / maxTokens) * 100 : 0}%` }}
            />
          </span>
        </div>
      </td>
      <td className="!pr-0 font-medium">{formatUsd(summary.cost)}</td>
    </tr>
  );
}
