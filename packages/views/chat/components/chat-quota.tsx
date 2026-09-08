"use client";

import { useMemo, useState } from "react";
import { Download, Gauge, ChevronDown } from "lucide-react";
import type { TaskQuotaCheckpoint } from "@multica/core/types";
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
  showWhenMissing = false,
}: {
  checkpoints?: TaskQuotaCheckpoint[];
  showWhenMissing?: boolean;
}) {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  if (!showWhenMissing && !checkpoints?.length) return null;

  return (
    <Collapsible open={open} onOpenChange={setOpen}>
      <CollapsibleTrigger className="flex items-center gap-1 text-caption text-muted-foreground hover:text-foreground">
        <Gauge className="size-3.5" aria-hidden />
        {t(($) => $.message_list.quota_title)}
        <ChevronDown className={`size-3 transition-transform ${open ? "rotate-180" : ""}`} aria-hidden />
      </CollapsibleTrigger>
      <CollapsibleContent className="mt-1.5">
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
}: {
  title: string;
  checkpoints?: TaskQuotaCheckpoint[];
}) {
  const { t } = useT("chat");
  const [open, setOpen] = useState(false);
  const groups = useMemo(() => {
    const grouped = new Map<string, TaskQuotaCheckpoint[]>();
    for (const checkpoint of checkpoints ?? []) {
      const id = checkpoint.task_id || "unknown";
      grouped.set(id, [...(grouped.get(id) ?? []), checkpoint]);
    }
    return Array.from(grouped.entries());
  }, [checkpoints]);

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
          {!checkpoints?.length ? (
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
                  onClick={() => downloadText(`${title}-quota.csv`, quotaCheckpointsCSV(checkpoints), "text/csv;charset=utf-8")}
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
                    <QuotaDetails checkpoints={taskCheckpoints} />
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
