import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, screen, waitFor } from "@testing-library/react";
import { ApiError } from "@multica/core/api";
import { configureShortcutPlatform } from "@multica/core/shortcuts";
import { renderWithI18n } from "../../test/i18n";
import { WakeupCreate } from "./wakeup-create";

const create = vi.fn();
let assigneeAgent = "emacs";
vi.mock("./wakeup-condition-names", () => ({
  useConditionNames: () => ({ status: (key: string) => key, label: () => undefined, property: () => undefined, actor: (_type: string, id: string) => id }),
}));
vi.mock("@multica/core/issues", () => ({
  useCreateIssueWakeup: () => ({ mutateAsync: create, isPending: false }),
  childIssuesOptions: () => ({ queryKey: ["children"] }),
  issueDetailOptions: () => ({ queryKey: ["issue-detail"] }),
}));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: ({ queryKey }: { queryKey: unknown[] }) => ({
    data: JSON.stringify(queryKey).includes("members")
      ? [{ user_id: "user-j", name: "Jiayuan" }]
      : JSON.stringify(queryKey).includes("issue-detail")
        ? { id: "issue", assignee_type: "agent", assignee_id: assigneeAgent }
        : [
          { id: "emacs", name: "Emacs", archived_at: null, runtime_id: "rt" },
          { id: "grok", name: "Grok", archived_at: null, runtime_id: "rt" },
        ],
  }),
}));
vi.mock("@multica/core/agents", () => ({ isAgentRuntimeBound: () => true }));
vi.mock("../../common/actor-avatar", () => ({ ActorAvatar: () => <span /> }));
vi.mock("../../common/use-viewing-timezone", () => ({ useViewingTimezone: () => "Asia/Shanghai" }));

async function renderForm(defaultAgentId = "emacs") {
  renderWithI18n(<WakeupCreate workspaceId="ws" issueId="issue" defaultAgentId={defaultAgentId} />, { locale: "zh-Hans" });
  fireEvent.click(screen.getByRole("button", { name: "新增喚醒" }));
  await screen.findByRole("form", { name: "新增喚醒" });
}

async function chooseCondition(label: string) {
  fireEvent.click(screen.getByRole("button", { name: /選擇條件/ }));
  fireEvent.click(await screen.findByRole("menuitem", { name: new RegExp(label) }));
}

beforeEach(() => {
  assigneeAgent = "emacs";
  create.mockReset().mockResolvedValue(undefined);
  // The send shortcut is the primary modifier + Enter; pin the platform so
  // Cmd means primary on every CI runner.
  configureShortcutPlatform("macos");
});
afterEach(() => configureShortcutPlatform(null));

describe("WakeupCreateForm", () => {
  it("says a reply wait for the assignee joins the run a comment already starts", async () => {
    await renderForm();
    await chooseCondition("有人回覆");
    expect(screen.getByText(/Emacs 是負責人，成員發留言時本來就會被觸發/)).toBeVisible();
  });

  it("says nothing when the rule wakes someone other than the assignee", async () => {
    assigneeAgent = "grok";
    await renderForm();
    await chooseCondition("有人回覆");
    expect(screen.queryByText(/是負責人，成員發留言時本來就會被觸發/)).toBeNull();
  });

  it("asks for a condition before creating anything", async () => {
    await renderForm();
    fireEvent.click(screen.getByRole("button", { name: /建立/ }));
    expect(screen.getByRole("alert")).toHaveTextContent("請選擇條件");
    expect(create).not.toHaveBeenCalled();
  });

  it("creates a reply wait for the assignee with a deadline and timeout action", async () => {
    await renderForm();
    await chooseCondition("有人回覆");
    expect(screen.getByRole("radiogroup", { name: "觸發次數" })).toBeVisible();
    expect(screen.getByRole("button", { name: /最多等待: 7 天/ })).toBeVisible();
    expect(screen.getByRole("button", { name: /逾時後: 喚醒 Emacs 處理/ })).toBeVisible();
    fireEvent.change(screen.getByLabelText("喚醒後要做什麼"), { target: { value: "按回覆繼續實現階段 2" } });
    fireEvent.click(screen.getByRole("radio", { name: "重複喚醒" }));
    // A repeating wait gets a cap on how many runs it may start.
    expect(screen.getByRole("button", { name: /最多觸發: 20 次/ })).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: /建立/ }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith({
        agent_id: "emacs",
        instruction: "按回覆繼續實現階段 2",
        kind: "event",
        mode: "continuous",
        max_fires: 20,
        event_types: ["comment.created"],
        expires_in_seconds: 604800,
        on_timeout: "wake",
      }),
    );
    await waitFor(() => expect(screen.queryByRole("form", { name: "新增喚醒" })).toBeNull());
  });

  it("requires an agent when the issue has no agent assignee", async () => {
    await renderForm("");
    await chooseCondition("在某個時間");
    fireEvent.change(screen.getByLabelText("喚醒後要做什麼"), { target: { value: "看一下 CI" } });
    fireEvent.click(screen.getByRole("button", { name: /建立/ }));
    expect(screen.getByRole("alert")).toHaveTextContent("請選擇要喚醒的 Agent");
  });

  it("gives a recurring check an end date and no timeout choice", async () => {
    await renderForm();
    await chooseCondition("定期檢查");
    expect(screen.getByLabelText("截止")).toBeVisible();
    expect(screen.queryByRole("radiogroup")).toBeNull();
    fireEvent.change(screen.getByLabelText("喚醒後要做什麼"), { target: { value: "巡檢遷移任務" } });
    fireEvent.click(screen.getByRole("button", { name: /建立/ }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(expect.objectContaining({ kind: "cron", cron_expression: "0 9 * * *", timezone: "Asia/Shanghai", mode: "continuous" })),
    );
  });

  it("keeps the draft and explains a capacity refusal", async () => {
    create.mockRejectedValue(new ApiError("full", 400, "Bad Request", { code: "wakeup_capacity_exceeded" }));
    await renderForm();
    await chooseCondition("在某個時間");
    fireEvent.change(screen.getByLabelText("喚醒後要做什麼"), { target: { value: "看一下 CI" } });
    fireEvent.click(screen.getByRole("button", { name: /建立/ }));
    expect(await screen.findByRole("alert")).toHaveTextContent("已達上限");
    expect(screen.getByLabelText("喚醒後要做什麼")).toHaveValue("看一下 CI");
    expect(screen.getByRole("form", { name: "新增喚醒" })).toBeVisible();
  });

  it("submits with Cmd+Enter", async () => {
    await renderForm();
    await chooseCondition("在某個時間");
    const input = screen.getByLabelText("喚醒後要做什麼");
    fireEvent.change(input, { target: { value: "看一下 CI" } });
    fireEvent.keyDown(input, { key: "Enter", metaKey: true });
    await waitFor(() => expect(create).toHaveBeenCalledWith(expect.objectContaining({ kind: "at", mode: "once" })));
  });
});

describe("platform conditions in the form", () => {
  it("groups conditions by what they wait for", async () => {
    await renderForm();
    fireEvent.click(screen.getByRole("button", { name: /選擇條件/ }));
    for (const label of ["欄位變為某個值", "子任務完成", "關聯 PR 的 CI 結束", "其他任務的狀態變化"]) {
      expect(await screen.findByRole("menuitem", { name: new RegExp(label) })).toBeVisible();
    }
    for (const group of ["協作", "執行與子任務", "關聯"]) expect(screen.getByText(group)).toBeVisible();
  });

  it("creates a sub-issue wait that the platform checks", async () => {
    await renderForm();
    await chooseCondition("子任務完成");
    expect(screen.getByRole("button", { name: /子任務完成: 全部子任務/ })).toBeVisible();
    fireEvent.change(screen.getByLabelText("喚醒後要做什麼"), { target: { value: "彙總子任務結果" } });
    fireEvent.click(screen.getByRole("button", { name: /建立/ }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(
        expect.objectContaining({ kind: "event", mode: "once", condition: { type: "children_done" }, expires_in_seconds: 604800 }),
      ),
    );
    expect(create.mock.calls[0]![0]).not.toHaveProperty("event_types");
  });

  it("waits for a linked PR's CI by default and can wait for the merge instead", async () => {
    await renderForm();
    await chooseCondition("關聯 PR 的 CI 結束");
    fireEvent.click(screen.getByRole("button", { name: /關聯 PR 的 CI 結束: CI 結束/ }));
    fireEvent.click(await screen.findByRole("menuitemradio", { name: "合併" }));
    fireEvent.change(screen.getByLabelText("喚醒後要做什麼"), { target: { value: "釋出" } });
    fireEvent.click(screen.getByRole("button", { name: /建立/ }));
    await waitFor(() =>
      expect(create).toHaveBeenCalledWith(expect.objectContaining({ condition: { type: "pull_request", event: "merged" } })),
    );
  });

  it("asks for the value a field has to reach", async () => {
    await renderForm();
    await chooseCondition("欄位變為某個值");
    fireEvent.change(screen.getByLabelText("喚醒後要做什麼"), { target: { value: "繼續" } });
    fireEvent.click(screen.getByRole("button", { name: /建立/ }));
    expect(screen.getByRole("alert")).toHaveTextContent("請選擇一個值");
    expect(create).not.toHaveBeenCalled();
  });
});
