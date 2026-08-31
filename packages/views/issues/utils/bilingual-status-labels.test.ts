import { describe, expect, it } from "vitest";
import zhTW from "../../locales/zh-Hans/issues.json";

describe("Taiwan issue status labels", () => {
  it("shows the built-in status key beside every Traditional Chinese label", () => {
    expect(zhTW.status).toEqual({
      backlog: "待規劃（backlog）",
      todo: "待辦（todo）",
      in_progress: "進行中（in progress）",
      in_review: "稽核中（in review）",
      done: "已完成（done）",
      blocked: "已阻塞（blocked）",
      cancelled: "已取消（cancelled）",
    });
  });
});
