import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import type { IssueWakeup, SystemWakeup } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";
import { WakeupsSection } from "./wakeups-section";
const mutate = vi.fn();
const enable = vi.fn();
const updateSystem = vi.fn();
const trigger = vi.fn();
const remove = vi.fn();
let runs: unknown[] = [];
let systemRules: SystemWakeup[] = [];
let pending = false;
let wakeup: IssueWakeup;
let status = "queued";
let viewTZ = "UTC";
vi.mock("./wakeup-condition-names", () => ({
  useConditionNames: () => ({ status: (key: string) => key, label: () => undefined, property: () => undefined, actor: (_type: string, id: string) => id }),
}));
vi.mock("@multica/core/paths", () => ({
  useCurrentWorkspace: () => ({ id: "ws" }),
}));
vi.mock("@multica/core/issues", () => ({
  issueWakeupsOptions: () => ({ queryKey: ["wakeups"] }),
  issueTasksOptions: () => ({ queryKey: ["tasks"] }),
  issueSystemWakeupsOptions: () => ({ queryKey: ["system"] }),
  useDisableIssueWakeup: () => ({ mutate, isPending: false }),
  useEnableIssueWakeup: () => ({ mutateAsync: enable, isPending: pending }),
  useCreateIssueWakeup: () => ({ mutateAsync: vi.fn(), isPending: false }),
  useUpdateIssueSystemWakeup: () => ({ mutateAsync: updateSystem, isPending: false }),
  useTriggerIssueWakeup: () => ({ mutate: trigger, isPending: false }),
  useDeleteIssueWakeup: () => ({ mutate: remove, isPending: false }),
  issueWakeupRunsOptions: () => ({ queryKey: ["runs"] }),
}));
vi.mock("@tanstack/react-query", () => ({
  useQuery: ({ queryKey }: { queryKey: string[] }) => ({
    data:
      queryKey[0] === "wakeups"
        ? [wakeup]
        : queryKey[0] === "system"
          ? systemRules
          : queryKey[0] === "tasks"
            ? [{ id: "task", status }]
            : queryKey[0] === "runs"
              ? runs
              : [],
  }),
}));
vi.mock("../../common/use-viewing-timezone", () => ({
  useViewingTimezone: () => viewTZ,
}));
vi.mock("../../common/task-transcript", () => ({
  TranscriptButton: () => <button>Transcript</button>,
}));
vi.mock("./wakeup-instruction-editor", () => ({
  WakeupInstructionEditor: () => <button>Edit prompt</button>,
}));
beforeEach(() => {
  mutate.mockReset();
  trigger.mockReset();
  remove.mockReset();
  runs = [];
  updateSystem.mockReset().mockResolvedValue(undefined);
  systemRules = [];
  enable.mockReset().mockResolvedValue(undefined);
  pending = false;
  status = "queued";
  viewTZ = "UTC";
  wakeup = {
    id: "wake",
    revision: 2,
    issue_id: "issue",
    agent_id: "agent",
    agent_name: "Emacs",
    instruction: "Check CI",
    kind: "every",
    mode: "continuous",
    event_types: [],
    interval_seconds: 3600,
    enabled: true,
    disabled_at: null,
    last_task_id: "task",
    last_error: null,
    timezone: "UTC",
    next_fire_at: null,
    cron_expression: null,
    filter_agent_id: null,
    filter_task_id: null,
  };
});
describe("Wakeups sidebar", () => {
  it("describes hourly schedules without exposing interval seconds", () => {
    renderWithI18n(<WakeupsSection issueId="issue" />, { locale: "zh-Hans" });
    expect(screen.getByRole("button", { name: /喚醒 Emacs/ })).toHaveTextContent("每小時喚醒");
    expect(screen.queryByText(/3600/)).toBeNull();
  });
  it("describes a daily cron and preserves its expression in details", async () => {
    wakeup.kind = "cron";
    wakeup.cron_expression = "0 9 * * *";
    renderWithI18n(<WakeupsSection issueId="issue" />);
    const row = screen.getByRole("button", { name: /Wake Emacs/ });
    expect(row).toHaveTextContent("09:00");
    expect(row).not.toHaveTextContent("0 9 * * *");
    fireEvent.click(row);
    await waitFor(() => expect(screen.getByText("0 9 * * * · UTC")).toBeVisible());
  });
  describe("one-time wakeup stored in UTC", () => {
    beforeEach(() => {
      vi.useFakeTimers({ toFake: ["Date"] });
      vi.setSystemTime(new Date("2026-09-23T10:00:00Z"));
      viewTZ = "Asia/Shanghai";
      wakeup.kind = "at";
      wakeup.mode = "once";
      wakeup.interval_seconds = null;
    });
    afterEach(() => vi.useRealTimers());

    it("shows the fire time in the viewer's timezone", async () => {
      wakeup.next_fire_at = "2026-09-23T14:05:00Z";
      renderWithI18n(<WakeupsSection issueId="issue" />);
      const row = screen.getByRole("button", { name: /Wake Emacs/ });
      expect(row).toHaveTextContent("Wake at Today 10:05 PM");
      fireEvent.click(row);
      await waitFor(() =>
        expect(screen.getByText(/· Asia\/Shanghai$/)).toBeVisible(),
      );
    });

    it("moves the fire time to the viewer's next day", () => {
      wakeup.next_fire_at = "2026-09-23T17:00:00Z";
      renderWithI18n(<WakeupsSection issueId="issue" />);
      const row = screen.getByRole("button", { name: /Wake Emacs/ });
      expect(row).toHaveTextContent("Wake at Sep 24, 01:00 AM");
      expect(row).not.toHaveTextContent("Today");
    });
  });
  it("exposes the toggle and reveals the full prompt only on opening details", async () => {
    renderWithI18n(<WakeupsSection issueId="issue" />);
    expect(screen.queryByText("Check CI")).not.toBeVisible();
    fireEvent.click(screen.getByRole("switch", { name: "Wakeup for Emacs" }));
    expect(mutate).toHaveBeenCalledWith("wake", expect.any(Object));
    fireEvent.click(screen.getByRole("button", { name: /Wake Emacs/ }));
    await waitFor(() => expect(screen.getByText("Check CI")).toBeVisible());
    expect(screen.getByRole("button", { name: "Transcript" })).toBeVisible();
    expect(screen.getByRole("button", { name: "Edit prompt" })).toBeVisible();
  });
  it("keeps a consumed one-shot queued run in the current list with withdrawal", () => {
    wakeup.enabled = false;
    wakeup.kind = "event";
    wakeup.mode = "once";
    wakeup.event_types = ["task.completed"];
    renderWithI18n(<WakeupsSection issueId="issue" />);
    expect(screen.queryByText(/Wakeup history/)).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Cancel this execution" }));
    expect(mutate).toHaveBeenCalled();
  });
  it("keeps an already claimed single run visible without offering withdrawal", () => {
    wakeup.enabled = false;
    status = "running";
    renderWithI18n(<WakeupsSection issueId="issue" />);
    expect(screen.queryByText(/Wakeup history/)).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Turn off wakeup/ }),
    ).toBeNull();
    expect(screen.getByRole("button", { name: /Wake Emacs/ })).toBeVisible();
  });
  it("folds ended configurations into history with an off toggle", () => {
    wakeup.enabled = false;
    status = "completed";
    renderWithI18n(<WakeupsSection issueId="issue" />);
    expect(screen.queryByRole("button", { name: /Wake Emacs/ })).toBeNull();
    expect(screen.queryByRole("switch")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
    expect(screen.getByRole("button", { name: /Wake Emacs/ })).toBeVisible();
  });
  it("uses server run status while the run detail query is still incomplete", () => {
    wakeup.enabled = false;
    wakeup.last_task_id = "missing";
    wakeup.last_task_status = "dispatched";
    renderWithI18n(<WakeupsSection issueId="issue" />);
    expect(screen.queryByText(/Wakeup history/)).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Turn off wakeup/ }),
    ).toBeNull();
  });
});

it("re-enables a manually disabled recurring configuration with its revision", async () => {
  wakeup.enabled = false;
  wakeup.disabled_at = "2026-09-16T00:00:00Z";
  status = "completed";
  renderWithI18n(<WakeupsSection issueId="issue" />);
  fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
  const toggle = screen.getByRole("switch", { name: "Wakeup for Emacs" });
  expect(toggle).not.toBeChecked();
  fireEvent.click(toggle);
  await waitFor(() =>
    expect(enable).toHaveBeenCalledWith({ id: "wake", revision: 2 }),
  );
  expect(toggle).not.toBeChecked(); // Wait for server state; no optimistic success.
});
it("blocks re-enabling on terminal issues", () => {
  wakeup.enabled = false;
  status = "completed";
  renderWithI18n(<WakeupsSection issueId="issue" closed />);
  fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
  expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
  fireEvent.click(screen.getByRole("switch"));
  expect(enable).not.toHaveBeenCalled();
  expect(mutate).not.toHaveBeenCalled();
  expect(screen.getByText(/cannot be enabled/)).toBeVisible();
});
it("requires a new future time for an expired one-shot", async () => {
  wakeup.enabled = false;
  wakeup.disabled_at = "2020-01-01T00:00:00Z";
  wakeup.kind = "at";
  wakeup.mode = "once";
  wakeup.next_fire_at = "2020-01-01T00:00:00Z";
  status = "completed";
  renderWithI18n(<WakeupsSection issueId="issue" />);
  fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
  expect(screen.queryByRole("switch")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Set a new time" }));
  const input = await screen.findByLabelText(/Time \(/);
  fireEvent.change(input, { target: { value: "2020-01-01T12:00" } });
  fireEvent.submit(input.closest("form")!);
  expect(await screen.findByRole("alert")).toHaveTextContent(
    "Choose a future time",
  );
  expect(enable).not.toHaveBeenCalled();
  fireEvent.change(input, { target: { value: "2099-01-01T12:00" } });
  fireEvent.submit(input.closest("form")!);
  await waitFor(() =>
    expect(enable).toHaveBeenCalledWith({
      id: "wake",
      revision: 2,
      at: new Date("2099-01-01T12:00").toISOString(),
      rearm: true,
    }),
  );
});
it("uses explicit resubscribe for a completed one-shot event", async () => {
  wakeup.enabled = false;
  wakeup.kind = "event";
  wakeup.mode = "once";
  status = "completed";
  renderWithI18n(<WakeupsSection issueId="issue" />);
  fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
  expect(screen.queryByRole("switch")).toBeNull();
  fireEvent.click(screen.getByRole("button", { name: "Enable again" }));
  await waitFor(() =>
    expect(enable).toHaveBeenCalledWith({
      id: "wake",
      revision: 2,
      rearm: true,
    }),
  );
});
it("disables controls during a pending mutation", () => {
  pending = true;
  renderWithI18n(<WakeupsSection issueId="issue" />);
  expect(screen.getByRole("switch")).toHaveAttribute("aria-disabled", "true");
  fireEvent.click(screen.getByRole("switch"));
  expect(enable).not.toHaveBeenCalled();
  expect(mutate).not.toHaveBeenCalled();
});

it("describes the source condition in Chinese, keeping instructions in details", async () => {
  wakeup.kind = "event";
  wakeup.mode = "once";
  wakeup.event_types = ["task.completed", "task.failed"];
  wakeup.filter_agent_name = "Grok";
  wakeup.filter_agent_id = "grok";
  renderWithI18n(<WakeupsSection issueId="issue" />, { locale: "zh-Hans" });
  const trigger = screen.getByRole("button", { name: /當 Grok 的執行成功結束時/ });
  expect(trigger).toHaveTextContent("喚醒 Emacs · 僅觸發一次");
  expect(screen.queryByText("Check CI")).not.toBeVisible();
  fireEvent.click(trigger);
  await waitFor(() => expect(screen.getByText("喚醒後要做什麼")).toBeVisible());
  expect(screen.getByText(/以下任一事件發生時/)).toHaveTextContent("當 Grok 的執行失敗時");
  expect(screen.getByText(/監看範圍/)).toHaveTextContent("目前任務");
});

it("shows a disabled rule independently from its last successful execution", () => {
  wakeup.enabled = false;
  wakeup.disabled_at = "2026-09-16T00:00:00Z";
  status = "completed";
  renderWithI18n(<WakeupsSection issueId="issue" />);
  fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
  const row = screen.getByRole("button", { name: /Wake Emacs/ });
  expect(row).toHaveTextContent("Turned off");
  expect(row).toHaveTextContent("Last execution: Run succeeded");
});

it("keeps a failed one-shot triggered without calling the rule completed", () => {
  wakeup.enabled = false;
  wakeup.kind = "event";
  wakeup.mode = "once";
  wakeup.event_types = ["comment.created"];
  status = "failed";
  renderWithI18n(<WakeupsSection issueId="issue" />);
  fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
  const row = screen.getByRole("button", { name: /Wake Emacs/ });
  expect(row).toHaveTextContent("Triggered");
  expect(row).toHaveTextContent("This execution: Run failed");
  expect(row).not.toHaveTextContent("Completed");
});

it("names the monitored member in the condition and details", async () => {
  wakeup.kind = "event";
  wakeup.event_types = ["comment.created", "comment.updated"];
  wakeup.filter_actor_type = "member";
  wakeup.filter_actor_id = "member-id";
  wakeup.filter_actor_name = "Jiayuan";
  renderWithI18n(<WakeupsSection issueId="issue" />, { locale: "zh-Hans" });
  const trigger = screen.getByRole("button", { name: /由 Jiayuan 觸發/ });
  expect(trigger).toHaveTextContent("Jiayuan");
  fireEvent.click(trigger);
  await waitFor(() => expect(screen.getByText(/監看的成員或 Agent/)).toBeVisible());
  expect(screen.getByText(/以下任一事件發生時/)).toHaveTextContent("由 Jiayuan 觸發");
});

it("keeps a redacted actor restriction visible without exposing an ID", () => {
  wakeup.kind = "event";
  wakeup.event_types = ["comment.created"];
  wakeup.filter_actor_type = "agent";
  wakeup.filter_actor_id = null;
  wakeup.filter_actor_name = null;
  renderWithI18n(<WakeupsSection issueId="issue" />);
  expect(screen.getByRole("button", { name: /triggered by Selected agent/ })).toBeInTheDocument();
});

describe("v2 sidebar", () => {
  it("offers a new-wakeup button on open issues only", () => {
    const { unmount } = renderWithI18n(<WakeupsSection issueId="issue" />);
    expect(screen.getByRole("button", { name: "New wakeup" })).toBeVisible();
    unmount();
    renderWithI18n(<WakeupsSection issueId="issue" closed />);
    expect(screen.queryByRole("button", { name: "New wakeup" })).toBeNull();
  });

  it("describes a wait's deadline in the row and who created it in details", async () => {
    vi.useFakeTimers({ toFake: ["Date"] });
    vi.setSystemTime(new Date("2026-09-24T08:00:00Z"));
    try {
      Object.assign(wakeup, {
        kind: "event", mode: "once", event_types: ["comment.created"], interval_seconds: null,
        filter_actor_type: "member", filter_actor_id: "u", filter_actor_name: "Jiayuan",
        expires_at: "2026-09-27T06:00:00Z", expiry_seconds: 259200, on_timeout: "wake",
        created_by_agent: true, created_by_name: "Jiayuan", source_agent_name: "Emacs",
      });
      renderWithI18n(<WakeupsSection issueId="issue" />, { locale: "zh-Hans" });
      const row = screen.getByRole("button", { name: /由 Jiayuan 觸發/ });
      expect(row).toHaveTextContent("喚醒 Emacs · 僅觸發一次 · 還剩 2 天 22 小時");
      fireEvent.click(row);
      await waitFor(() => expect(screen.getByText(/來源/)).toHaveTextContent("Emacs 建立（代表 Jiayuan）"));
      expect(screen.getByText(/有效期/)).toHaveTextContent("逾時後喚醒 Emacs 處理");
    } finally {
      vi.useRealTimers();
    }
  });

  it("marks a rule ended by its deadline as timed out", () => {
    Object.assign(wakeup, {
      kind: "event", mode: "once", event_types: ["comment.created"], enabled: false,
      expires_at: "2020-01-01T00:00:00Z", timed_out_at: "2020-01-01T00:00:30Z",
    });
    status = "completed";
    renderWithI18n(<WakeupsSection issueId="issue" />);
    fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
    expect(screen.getByRole("button", { name: /Wake Emacs/ })).toHaveTextContent("Timed out");
  });

  it("shows the child-done system rule and turns it off for this issue", async () => {
    systemRules = [{
      id: "rule", revision: 1, default_instruction: "Advance the next stage.", customized: false, paused_reason: null,
      rule: "child_done", workspace_default: true, enabled: true, instruction: "", staged: true, stage: 1, total: 2, remaining: 1,
      waiting: ["MUL-7704"], target: { type: "agent", id: "a", name: "Emacs" }, blocked: "",
    }];
    renderWithI18n(<WakeupsSection issueId="issue" />, { locale: "zh-Hans" });
    const row = screen.getByRole("button", { name: /當第 1 階段的子任務全部結束時/ });
    expect(row).toHaveTextContent("系統");
    expect(row).toHaveTextContent("喚醒負責人 Emacs · 還差 1 個");
    expect(screen.getByRole("button", { name: /喚醒 2/ })).toBeVisible();
    fireEvent.click(screen.getAllByRole("switch", { name: "子任務結束時喚醒負責人" })[0]!);
    await waitFor(() => expect(updateSystem).toHaveBeenCalledWith({ rule: "child_done", enabled: false, instruction: "" }));
  });

  it("says a member assignee is notified instead of woken", () => {
    systemRules = [{
      id: "rule", revision: 1, default_instruction: "Advance the next stage.", customized: false, paused_reason: null,
      rule: "child_done", workspace_default: true, enabled: true, instruction: "", staged: false, stage: null, total: 3, remaining: 2,
      waiting: [], target: { type: "member", id: "u", name: "Jiayuan" }, blocked: "member_assignee",
    }];
    renderWithI18n(<WakeupsSection issueId="issue" />);
    expect(screen.getByRole("button", { name: /When all sub-issues finish/ })).toHaveTextContent("Notify Jiayuan in their inbox · 2 to go");
  });

  it("shows the default instruction and saves one for this issue", async () => {
    systemRules = [{
      id: "rule", revision: 1, default_instruction: "Advance the next stage.", customized: false, paused_reason: null,
      rule: "child_done", workspace_default: true, enabled: true, instruction: "", staged: false, stage: null, total: 1, remaining: 1,
      waiting: ["MUL-2"], target: { type: "agent", id: "a", name: "Emacs" }, blocked: "",
    }];
    renderWithI18n(<WakeupsSection issueId="issue" />);
    fireEvent.click(screen.getByRole("button", { name: /When all sub-issues finish/ }));
    expect(await screen.findByText("Advance the next stage.")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Edit instruction" }));
    const input = screen.getByLabelText("What to do when woken");
    expect(input).toHaveAttribute("placeholder", "Advance the next stage.");
    fireEvent.change(input, { target: { value: "Ask Jiayuan first" } });
    fireEvent.submit(input.closest("form")!);
    await waitFor(() => expect(updateSystem).toHaveBeenCalledWith({ rule: "child_done", enabled: true, instruction: "Ask Jiayuan first" }));
  });
});

describe("conditions, limits and history", () => {
  it("reads a platform condition as its own sentence, without the hint events", async () => {
    Object.assign(wakeup, {
      kind: "event", mode: "once", event_types: ["issue.status_changed"], interval_seconds: null,
      condition: { type: "issue_field", field: "status", value: "in_review" },
    });
    renderWithI18n(<WakeupsSection issueId="issue" />, { locale: "zh-Hans" });
    const row = screen.getByRole("button", { name: /當狀態變為in_review時/ });
    fireEvent.click(row);
    await screen.findByRole("dialog");
    expect(screen.queryByText(/以下任一事件發生時/)).toBeNull();
  });

  it("says why the platform paused a rule and how many times it fired", async () => {
    Object.assign(wakeup, {
      kind: "event", mode: "continuous", event_types: ["comment.created"], interval_seconds: null,
      enabled: false, paused_reason: "max_fires", max_fires: 5, fire_count: 5,
    });
    status = "completed";
    renderWithI18n(<WakeupsSection issueId="issue" />);
    fireEvent.click(screen.getByRole("button", { name: "Wakeup history 1" }));
    const row = screen.getByRole("button", { name: /Wake Emacs/ });
    expect(row).toHaveTextContent("Paused automatically");
    expect(row).toHaveTextContent("Stopped after reaching 5 runs");
    fireEvent.click(row);
    expect(await screen.findByText("Triggered 5 of at most 5 times")).toBeVisible();
    expect(screen.getByText(/Turn it back on to resume/)).toBeVisible();
  });

  it("lists the rule's runs, with a silent check's note", async () => {
    runs = [
      { id: "r2", status: "completed", created_at: "2026-09-24T01:00:00Z", started_at: null, completed_at: null, checkin_note: "Progress 38%", triggers: ["time.due"], commented: false },
      { id: "r1", status: "completed", created_at: "2026-09-23T01:00:00Z", started_at: null, completed_at: null, checkin_note: "", triggers: ["time.due"], commented: true },
    ];
    renderWithI18n(<WakeupsSection issueId="issue" />, { locale: "zh-Hans" });
    fireEvent.click(screen.getByRole("button", { name: /每小時喚醒/ }));
    expect(await screen.findByText("觸發紀錄")).toBeVisible();
    expect(screen.getByText("靜默檢查")).toBeVisible();
    expect(screen.getByText(/Progress 38%/)).toBeVisible();
    expect(screen.getByText("執行成功 · 發表了留言")).toBeVisible();
  });

  it("wakes the agent now and deletes the rule after confirmation", async () => {
    renderWithI18n(<WakeupsSection issueId="issue" />);
    fireEvent.click(screen.getByRole("button", { name: /Wake every hour/ }));
    fireEvent.click(await screen.findByRole("button", { name: "Wake now" }));
    expect(trigger).toHaveBeenCalledWith("wake", expect.anything());
    fireEvent.click(screen.getByRole("button", { name: "Delete" }));
    const confirm = await screen.findByRole("alertdialog");
    expect(confirm).toHaveTextContent("Emacs is no longer woken by it");
    fireEvent.click(within(confirm).getByRole("button", { name: "Delete" }));
    expect(remove).toHaveBeenCalledWith("wake", expect.anything());
  });

  it("offers neither action on an ended issue", async () => {
    renderWithI18n(<WakeupsSection issueId="issue" closed />);
    fireEvent.click(screen.getByRole("button", { name: /Wake every hour/ }));
    await screen.findByRole("dialog");
    expect(screen.queryByRole("button", { name: "Wake now" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete" })).toBeNull();
  });
});
