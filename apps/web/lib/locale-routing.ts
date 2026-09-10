import {
  matchLocale,
  SUPPORTED_LOCALES,
  type SupportedLocale,
} from "@multica/core/i18n";

export const MULTICA_LOCALE_HEADER = "x-multica-locale";

export function isSupportedLocale(
  value: string | null,
): value is SupportedLocale {
  return (
    value !== null &&
    (SUPPORTED_LOCALES as readonly string[]).includes(value)
  );
}

export function resolveLocaleFromSignals({
  cookieLocale,
}: {
  cookieLocale?: string | null;
}): SupportedLocale {
  // The zh-tw build serves Traditional Chinese under the compatibility key.
  // Only an explicit saved preference overrides the initial language.
  return cookieLocale ? matchLocale([cookieLocale]) : "zh-Hans";
}
