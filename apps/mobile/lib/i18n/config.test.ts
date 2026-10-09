// @vitest-environment node
import { beforeEach, describe, expect, it, vi } from "vitest";

const device = vi.hoisted(() => ({ languages: [] as string[] }));

vi.mock("expo-localization", () => ({
  getLocales: () => device.languages.map((languageTag) => ({ languageTag })),
}));
vi.mock("expo-secure-store", () => ({
  getItemAsync: vi.fn(),
  setItemAsync: vi.fn(),
}));

beforeEach(() => vi.resetModules());

describe("mobile system language", () => {
  it.each(["zh-TW", "zh_Hant_TW", "zh-HK", "zh-MO", "zh-CN"])(
    "loads Traditional Chinese for %s using the compatible locale key",
    async (language) => {
      device.languages = [language];
      const { SYSTEM_LOCALE } = await import("./config");
      const { i18n } = await import("./singleton");

      expect(SYSTEM_LOCALE).toBe("zh-Hans");
      expect(i18n.t("issues:runs.status.completed")).toBe("執行成功");
    },
  );

  it.each([[], ["ja-JP"], ["en-US", "zh-TW"]])(
    "uses English for an unsupported or preferred English device locale: %j",
    async (...languages) => {
      device.languages = languages;
      const { SYSTEM_LOCALE } = await import("./config");
      expect(SYSTEM_LOCALE).toBe("en");
    },
  );
});
