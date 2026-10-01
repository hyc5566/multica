import { describe, expect, it, vi } from "vitest";
import { renderHook } from "@testing-library/react";
import type { TimelineEntry } from "@multica/core/types";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "../../test/i18n";
import { useT } from "../../i18n";
import { useWakeupText } from "./wakeup-presentation";
import { formatWakeupActivity, wakeupActivityChip } from "./wakeup-activity";

vi.mock("./wakeup-condition-names", () => ({
  useConditionNames: () => ({ status: (key: string) => key, label: () => undefined, property: () => undefined, actor: (_type: string, id: string) => id }),
}));
vi.mock("../../common/use-viewing-timezone", () => ({ useViewingTimezone: () => "UTC" }));

const names: Record<string, string> = { a: "Emacs", u: "Jiayuan", g: "Grok" };
const getActorName = (_type: string, id: string) => names[id] ?? id;
const entry = (action: string, details: Record<string, unknown>, patch: Partial<TimelineEntry> = {}): TimelineEntry =>
  ({ type: "activity", id: action, actor_type: "system", actor_id: "", action, details, created_at: "2026-09-24T00:00:00Z", ...patch }) as TimelineEntry;
const reply = { id: "w", kind: "event", mode: "once", event_types: ["comment.created"], agent_id: "a", filter_actor_type: "member", filter_actor_id: "u", created_by: "u" };

function helpers() {
  const { result } = renderHook(() => ({ t: useT("issues").t, text: useWakeupText() }), {
    wrapper: ({ children }) => (
      <I18nProvider locale="zh-Hans" resources={RESOURCES}>
        {children}
      </I18nProvider>
    ),
  });
  return result.current;
}

describe("wakeup timeline entries", () => {
  it("reads each entry as one sentence", () => {
    const { t, text } = helpers();
    const read = (e: TimelineEntry) => formatWakeupActivity(e, t, text, getActorName);
    expect(read(entry("wakeup_created", { wakeup: reply }, { actor_type: "agent", actor_id: "a" }))).toBe(
      "新增了喚醒：當此任務有新留言時（由 Jiayuan 觸發）喚醒 Emacs",
    );
    expect(read(entry("wakeup_triggered", { wakeup: { ...reply, condition: { type: "pull_request", event: "checks_finished" } } }))).toBe(
      "當關聯 PR 的 CI 結束時，喚醒了 Emacs",
    );
    expect(read(entry("wakeup_triggered", { wakeup: reply, events: ["wakeup.manual"], actor_type: "member", actor_id: "u" }))).toBe(
      "Jiayuan 立即喚醒了 Emacs",
    );
    expect(read(entry("wakeup_triggered", { rule: "child_done", stage: 1, total: 2, target_type: "agent", target_id: "a", outcome: "woke" }))).toBe(
      "第 1 階段的 2 個子任務已全部結束，喚醒了 Emacs",
    );
    expect(read(entry("wakeup_triggered", { rule: "child_done", total: 3, target_type: "member", target_id: "u", outcome: "notified" }))).toBe(
      "3 個子任務已全部結束，已通知 Jiayuan",
    );
    expect(read(entry("wakeup_triggered", { rule: "child_done", total: 3, target_type: "agent", target_id: "a", outcome: "merged" }))).toBe(
      "3 個子任務已全部結束，併入了 Emacs 待開始的執行",
    );
    expect(read(entry("wakeup_triggered", { rule: "child_done", stage: 2, total: 1, outcome: "none" }))).toBe(
      "第 2 階段的 1 個子任務已全部結束",
    );
    expect(read(entry("wakeup_triggered", { rule: "child_done", stage: 1, total: 2, target_type: "agent", target_id: "a", outcome: "acknowledged" }))).toBe(
      "第 1 階段的 2 個子任務已全部結束，Emacs 正在處理，未重複喚醒",
    );
    expect(read(entry("wakeup_triggered", { wakeup: reply, outcome: "merged" }))).toBe(
      "當此任務有新留言時（由 Jiayuan 觸發），併入了 Emacs 待開始的執行",
    );
    expect(read(entry("wakeup_triggered", { wakeup: reply, outcome: "acknowledged" }))).toBe(
      "當此任務有新留言時（由 Jiayuan 觸發），由 Emacs 自己觸發，未重複喚醒",
    );
    expect(read(entry("wakeup_timed_out", { wakeup: reply, woke: true }))).toContain("喚醒了 Emacs 處理逾時");
    expect(read(entry("wakeup_paused", { wakeup: reply, reason: "loop" }))).toBe("已暫停：與其他喚醒規則互相觸發");
    expect(read(entry("wakeup_checkin", { wakeup: reply, note: "進度 72%" }, { actor_type: "agent", actor_id: "g", coalesced_count: 3 }))).toBe(
      "靜默檢查 3 次 · 最近一次：進度 72%",
    );
  });

  it("tags an entry with the rule it came from", () => {
    const { t, text } = helpers();
    const chip = (e: TimelineEntry) => wakeupActivityChip(e, t, text, getActorName);
    expect(chip(entry("wakeup_triggered", { rule: "child_done" }))).toBe("系統規則");
    expect(chip(entry("wakeup_triggered", { wakeup: reply }))).toBe("Jiayuan 建立");
    expect(chip(entry("wakeup_triggered", { wakeup: { ...reply, created_by_agent_id: "a" } }))).toBe("Emacs 建立");
    expect(chip(entry("wakeup_created", { wakeup: reply }))).toBeNull();
  });

});
