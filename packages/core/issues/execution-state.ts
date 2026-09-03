import type { Issue } from "../types";
import { issueStatusCategory } from "./status-category";

export const ISSUE_EXECUTION_STATE_KEY = "execution_state";

export const ISSUE_EXECUTION_STATES = [
  "active",
  "waiting",
  "blocked",
  "paused",
  "not_applicable",
] as const;

export type IssueExecutionState = (typeof ISSUE_EXECUTION_STATES)[number];

export function issueExecutionState(issue: Issue): IssueExecutionState {
  const stored = issue.metadata?.[ISSUE_EXECUTION_STATE_KEY];
  if (
    typeof stored === "string" &&
    ISSUE_EXECUTION_STATES.includes(stored as IssueExecutionState)
  ) {
    return stored as IssueExecutionState;
  }
  switch (issueStatusCategory(issue)) {
    case "in_progress":
      return "active";
    case "in_review":
      return "waiting";
    case "blocked":
      return "blocked";
    default:
      return "not_applicable";
  }
}
