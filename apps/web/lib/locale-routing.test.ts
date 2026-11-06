// @vitest-environment node
import { describe, expect, it } from "vitest";
import {
  isSupportedLocale,
  resolveLocaleFromSignals,
} from "./locale-routing";

describe("locale routing", () => {
  it("accepts only app-supported locale identifiers", () => {
    expect(isSupportedLocale("en")).toBe(true);
    expect(isSupportedLocale("zh-Hans")).toBe(true);
    expect(isSupportedLocale("ko")).toBe(true);
    expect(isSupportedLocale("ja")).toBe(true);
    expect(isSupportedLocale("zh")).toBe(false);
    expect(isSupportedLocale(null)).toBe(false);
  });

  it("defaults new visitors to Traditional Chinese", () => {
    expect(resolveLocaleFromSignals({})).toBe("zh-Hans");
    expect(resolveLocaleFromSignals({ cookieLocale: "" })).toBe("zh-Hans");
  });

  it("normalizes legacy landing zh cookies to the app locale", () => {
    expect(resolveLocaleFromSignals({ cookieLocale: "zh" })).toBe("zh-Hans");
  });

  it.each(["en", "zh-Hans", "ko", "ja"])(
    "preserves the saved %s preference",
    (locale) => {
      expect(resolveLocaleFromSignals({ cookieLocale: locale })).toBe(locale);
    },
  );
});
