import { statusCategoryOfKey } from "@multica/core/issues";
import type { IssueStatus, IssueStatusCategory } from "@multica/core/types";
import { StatusIcon } from "./status-icon";

const STATUS_HEADING_LABELS: Record<IssueStatusCategory, string> = {
  backlog: "Backlog",
  todo: "Todo",
  in_progress: "In Progress",
  in_review: "In Review",
  done: "Finished",
  blocked: "Blocked",
  cancelled: "Cancelled",
};

export function StatusHeading({
  status,
  count,
}: {
  status: IssueStatus;
  count: number;
}) {
  const category = statusCategoryOfKey(status);
  return (
    <div className="flex items-center gap-2">
      <span className="inline-flex items-center gap-1.5 whitespace-nowrap text-caption font-semibold">
        <StatusIcon status={status} className="h-3 w-3" />
        {STATUS_HEADING_LABELS[category]}
      </span>
      <span className="text-caption text-muted-foreground">{count}</span>
    </div>
  );
}
