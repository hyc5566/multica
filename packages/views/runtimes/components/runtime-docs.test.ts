// @vitest-environment node
import { describe, expect, it } from "vitest";
import { customRuntimeDocsHref, daemonRuntimesDocsHref } from "./runtime-docs";

describe("runtime docs links", () => {
  it.each(["en", "zh-Hans", "zh-Hant", "ja", "ko", "fr"])("uses the self-hosted docs repository for %s", (language) => {
    expect(daemonRuntimesDocsHref(language)).toBe("https://github.com/hyc5566/multica/tree/zh-tw/apps/docs/content/docs");
    expect(customRuntimeDocsHref(language)).toBe("https://github.com/hyc5566/multica/tree/zh-tw/apps/docs/content/docs");
  });
});
