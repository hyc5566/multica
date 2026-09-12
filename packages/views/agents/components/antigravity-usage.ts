import type { RuntimeProviderUsageWindow } from "@multica/core/types";

// Only provider-confirmed bucket IDs identify a shared pool. Other buckets
// remain independent, even when their labels or percentages happen to match.
export function antigravityQuotaPool(id: string) {
  if (id === "gemini-5h" || id === "gemini-weekly") return "google";
  if (id === "3p-5h" || id === "3p-weekly") return "other";
  return undefined;
}

export function prioritizeAntigravityWindows(
  windows: RuntimeProviderUsageWindow[],
  model: string | undefined,
) {
  const modelKey = model?.trim().toLowerCase() ?? "";
  const currentPool = modelKey.startsWith("gemini-")
    ? "google"
    : /^(claude-|gpt-)/.test(modelKey) ? "other" : undefined;
  const rows = [...windows];
  for (const [id, group, duration] of [
    ["gemini-5h", "Gemini Models", 300],
    ["gemini-weekly", "Gemini Models", 10080],
    ["3p-5h", "Claude and GPT models", 300],
    ["3p-weekly", "Claude and GPT models", 10080],
  ] as const) {
    if (!rows.some((row) => row.id === id)) {
      rows.push({ id, group, label: id, unit: "percent", window_duration_mins: duration });
    }
  }
  return rows.map((window, index) => {
    const pool = antigravityQuotaPool(window.id);
    return {
      window,
      current: pool ? pool === currentPool : modelKey !== "" && window.id.toLowerCase() === modelKey,
      displayLabel: window.group || window.id,
      index,
      order: pool === "google" ? 0 : pool === "other" ? 1 : 2,
    };
  }).sort((a, b) =>
    Number(b.current) - Number(a.current) || a.order - b.order ||
    (a.window.window_duration_mins ?? Infinity) - (b.window.window_duration_mins ?? Infinity) || a.index - b.index,
  ).map(({ window, current, displayLabel }) => ({ window, current, displayLabel }));
}
