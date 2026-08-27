import type { SupportedLocale } from "@multica/core/i18n";

// HTML lang uses BCP-47 region tags widely recognized by screen readers and
// font stacks. This build keeps zh-Hans as the internal resource key for
// compatibility while serving Taiwan Traditional Chinese copy.
export const HTML_LANG: Record<SupportedLocale, string> = {
  en: "en",
  "zh-Hans": "zh-TW",
  ko: "ko-KR",
  ja: "ja-JP",
};
