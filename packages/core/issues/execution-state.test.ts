import { describe, expect, it } from "vitest";
import type { Issue } from "../types";
import { issueExecutionState } from "./execution-state";

const issue = (status: string, executionState?: string) =>
  ({
    status,
    metadata: executionState ? { execution_state: executionState } : {},
  }) as Issue;

describe("issueExecutionState", () => {
  it("uses the explicit execution state independently of workflow status", () => {
    expect(issueExecutionState(issue("in_progress", "blocked"))).toBe("blocked");
    expect(issueExecutionState(issue("todo", "paused"))).toBe("paused");
  });

  it("maps legacy workflow statuses when metadata has not been set", () => {
    expect(issueExecutionState(issue("in_progress"))).toBe("active");
    expect(issueExecutionState(issue("in_review"))).toBe("waiting");
    expect(issueExecutionState(issue("blocked"))).toBe("blocked");
    expect(issueExecutionState(issue("todo"))).toBe("not_applicable");
  });
});
