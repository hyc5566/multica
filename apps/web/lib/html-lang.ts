import type { SupportedLocale } from "@multica/core/i18n";

// HTML lang uses BCP-47 region tags widely recognized by screen readers and
// font stacks. This build retains zh-Hans as its compatibility resource key,
// but that bundle and its typography are Taiwan Traditional Chinese.
export const HTML_LANG: Record<SupportedLocale, string> = {
  en: "en",
  "zh-Hans": "zh-TW",
  ko: "ko-KR",
  ja: "ja-JP",
};
