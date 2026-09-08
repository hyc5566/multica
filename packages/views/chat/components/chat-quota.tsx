"use client";

import { useMemo, useState } from "react";
import { Download, Gauge, ChevronDown } from "lucide-react";
import type { ChatSession, TaskQuotaCheckpoint, TaskUsage } from "@multica/core/types";
import { Button } from "@multica/ui/components/ui/button";
import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@multica/ui/components/ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "@multica/ui/components/ui/dialog";
import { compareTaskQuotaCheckpoints } from "../../issues/components/task-quota-comparison";
import { useT } from "../../i18n";
import { formatTokens, summarizeTaskUsage } from "../../runtimes/utils";

function shortTime(value: string | undefined): string {
  return value
    ? new Intl.DateTimeFormat(undefined, { dateStyle: "short", timeStyle: "short" }).format(new Date(value))
    : "—";
}

function pct(value: number | undefined): string {
  return value == null ? "—" : `${value.toFixed(1)}%`;
}

export function ChatTaskQuota({
  checkpoints,
  usage,
  showWhenMissing = false,
  defaultOpen = false,
}: {
  checkpoints?: TaskQuotaCheckpoint[];
  usage?: TaskUsage[];
  showWhenMissing?: boolean;
  defaultOpen?: boolean;
}) {
  const { t } = useT("chat");
  const { t: ti } = useT("issues");
  const [open, setOpen] = useState(defaultOpen);
  const summary = summarizeTaskUsage(usage);
  if (!showWhenMissing && !checkpoints?.length && !summary) return null;

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="flex items-center gap-1 text-caption text-muted-foreground hover:text-foreground">
        <Gauge className="size-3.5" aria-hidden />
        {t(($) => $.message_list.quota_title)}
        <span className="tabular-nums"> · {t(($) => $.message_list.token_count, { value: summary ? formatTokens(summary.tokens) : "—" })}</span>
        <ChevronDown className={`size-3 transition-transform ${open ? "rotate-180" : ""}`} aria-hidden />
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1.5">
        <div className="mb-2 space-y-1 rounded-md border p-2 text-caption">
          {summary ? (
            <>
              <p className="font-medium">{ti(($) => $.usage_detail.kpi_tokens)}: {summary.tokens.toLocaleString()}</p>
              <dl className="grid grid-cols-2 gap-x-3 gap-y-1">
                <dt>{ti(($) => $.usage_detail.col_input)}</dt><dd>{summary.input.toLocaleString()}</dd>
                <dt>{ti(($) => $.usage_detail.col_output)}</dt><dd>{summary.output.toLocaleString()}</dd>
                <dt>{ti(($) => $.usage_detail.col_cache_read)}</dt><dd>{summary.cacheRead.toLocaleString()}</dd>
                <dt>{ti(($) => $.usage_detail.col_cache_write)}</dt><dd>{summary.cacheWrite.toLocaleString()}</dd>
              </dl>
              {usage?.map((slice, index) => (
                <p key={index} className="break-words text-muted-foreground">
                  {slice.provider} / {slice.model}: {t(($) => $.message_list.token_count, { value: summarizeTaskUsage([slice])?.tokens.toLocaleString() })}
                </p>
              ))}
            </>
          ) : <p className="text-muted-foreground">{t(($) => $.message_list.usage_missing)}</p>}
        </div>
        <QuotaDetails checkpoints={checkpoints ?? []} />
      </CollapsibleContent>
    </Collapsible>
  );
}

function QuotaDetails({ checkpoints }: { checkpoints: TaskQuotaCheckpoint[] }) {
  const { t } = useT("chat");
  const comparisons = compareTaskQuotaCheckpoints(checkpoints);
  const before = checkpoints.find((checkpoint) => checkpoint.phase === "before");
  const after = checkpoints.find((checkpoint) => checkpoint.phase === "after");
  const overlap = Math.max(0, ...checkpoints.map((checkpoint) => checkpoint.overlapping_task_count));
  const stateLabel = (state: ReturnType<typeof compareTaskQuotaCheckpoints>[number]["state"]) => ({
    comparable: t(($) => $.message_list.quota_state.comparable),
    same_observation: t(($) => $.message_list.quota_state.same_observation),
    reset_crossed: t(($) => $.message_list.quota_state.reset_crossed),
    stale: t(($) => $.message_list.quota_state.stale),
    unavailable: t(($) => $.message_list.quota_state.unavailable),
    missing: t(($) => $.message_list.quota_state.missing),
  })[state];

  return (
    <div className="space-y-1.5 rounded-md border bg-muted/30 p-2 text-caption">
      <p className="text-micro text-muted-foreground">
        {t(($) => $.message_list.quota_attribution_note)}
      </p>
      {comparisons.length === 0 ? (
        <p className="text-muted-foreground">
          {t(($) => $.message_list.quota_no_window, {
            before: before?.error_code || before?.capture_state || "missing",
            after: after?.error_code || after?.capture_state || "missing",
          })}
        </p>
      ) : comparisons.map((window) => (
        <div key={window.id} className="grid grid-cols-[minmax(0,1fr)_auto] gap-x-3">
          <span className="truncate font-medium">{window.label}</span>
          <span className="tabular-nums">
            {pct(window.before)} → {pct(window.after)}
            {window.delta == null ? "" : ` (${window.delta >= 0 ? "+" : ""}${window.delta.toFixed(1)} pp)`}
          </span>
          <span className="text-micro text-muted-foreground">
            {shortTime(before?.snapshot?.observed_at)} → {shortTime(after?.snapshot?.observed_at)}
          </span>
          <span className="text-micro text-muted-foreground">{stateLabel(window.state)}</span>
        </div>
      ))}
      {overlap > 0 && (
        <p className="text-micro text-warning">
          {t(($) => $.message_list.quota_overlap, { count: overlap })}
        </p>
      )}
    </div>
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
  setTimeout(() => URL.revokeObjectURL(href), 0);
}

function csvCell(value: unknown): string {
  let text = value == null ? "" : String(value);
  if (/^[=+\-@]/.test(text)) text = `'${text}`;
  return `"${text.replaceAll('"', '""')}"`;
}

export function quotaCheckpointsCSV(checkpoints: TaskQuotaCheckpoint[]): string {
  const rows: unknown[][] = [[
    "task_id", "phase", "provider", "requested_model", "window_id", "window",
    "used_percent", "remaining_percent", "boundary_at_utc", "observed_at_utc",
    "resets_at_utc", "capture_state", "error_code", "observation_age_ms",
    "overlapping_tasks", "observation_id",
  ]];
  for (const checkpoint of checkpoints) {
    const windows = checkpoint.snapshot?.windows ?? [];
    const output = windows.length > 0 ? windows : [undefined];
    for (const window of output) {
      rows.push([
        checkpoint.task_id, checkpoint.phase, checkpoint.provider, checkpoint.requested_model,
        window?.id, window?.label, window?.used_percent, window?.remaining_percent,
        checkpoint.boundary_at, checkpoint.snapshot?.observed_at, window?.resets_at,
        checkpoint.capture_state, checkpoint.error_code, checkpoint.observation_age_ms,
        checkpoint.overlapping_task_count, checkpoint.observation_id,
      ]);
    }
  }
  return rows.map((row) => row.map(csvCell).join(",")).join("\r\n");
}

export function ChatSessionQuotaButton({
  title,
  checkpoints,
  taskUsage,
}: {
  title: string;
  checkpoints?: TaskQuotaCheckpoint[];
  taskUsage?: ChatSession["task_usage"];
}) {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  const groups = useMemo(() => {
    const grouped = new Map<string, TaskQuotaCheckpoint[]>();
    for (const run of taskUsage ?? []) grouped.set(run.task_id, []);
    for (const checkpoint of checkpoints ?? []) {
      const id = checkpoint.task_id || "unknown";
      grouped.set(id, [...(grouped.get(id) ?? []), checkpoint]);
    }
    return Array.from(grouped.entries());
  }, [checkpoints, taskUsage]);

  return (
    <>
      <Button
        type="button"
        variant="ghost"
        size="icon-sm"
        className="text-muted-foreground"
        title={t(($) => $.header.quota_summary)}
        aria-label={t(($) => $.header.quota_summary)}
        onClick={() => setOpen(true)}
      >
        <Gauge className="size-4" />
      </Button>
      <Dialog open={open} onOpenChange={setOpen}>
        <DialogContent className="!max-w-2xl !w-[calc(100vw-2rem)]">
          <DialogHeader>
            <DialogTitle>{t(($) => $.header.quota_summary)}</DialogTitle>
            <DialogDescription>
              {t(($) => $.header.quota_summary_description, { title })}
            </DialogDescription>
          </DialogHeader>
          {!groups.length ? (
            <p className="py-8 text-center text-body text-muted-foreground">
              {t(($) => $.header.quota_empty)}
            </p>
          ) : (
            <div className="min-w-0 space-y-4">
              <div className="flex justify-end gap-2">
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => downloadText(`${title}-quota.json`, JSON.stringify(checkpoints, null, 2), "application/json;charset=utf-8")}
                >
                  <Download className="mr-1 size-3.5" aria-hidden />
                  {t(($) => $.header.export_json)}
                </Button>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() => downloadText(`${title}-quota.csv`, quotaCheckpointsCSV(checkpoints ?? []), "text/csv;charset=utf-8")}
                >
                  <Download className="mr-1 size-3.5" aria-hidden />
                  {t(($) => $.header.export_csv)}
                </Button>
              </div>
              <div className="max-h-[55vh] space-y-3 overflow-auto">
                {groups.map(([taskID, taskCheckpoints], index) => (
                  <section key={taskID} className="space-y-1" aria-label={`${t(($) => $.header.run)} ${index + 1}`}>
                    <h3 className="text-caption font-medium">
                      {t(($) => $.header.run)} {index + 1}
                    </h3>
                    <ChatTaskQuota checkpoints={taskCheckpoints} usage={taskUsage?.find((run) => run.task_id === taskID)?.usage} showWhenMissing defaultOpen />
                  </section>
                ))}
              </div>
            </div>
          )}
        </DialogContent>
      </Dialog>
    </>
  );
}
