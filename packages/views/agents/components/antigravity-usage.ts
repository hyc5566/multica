import type { RuntimeProviderUsageWindow } from "@multica/core/types";

// Only provider-confirmed bucket IDs identify a shared pool. Other buckets
// remain independent, even when their labels or percentages happen to match.
export function antigravityQuotaPool(id: string) {
  if (id === "gemini-5h" || id === "gemini-weekly") return "google";
  if (id === "3p-5h" || id === "3p-weekly") return "other";
  // Argon has its own weekly-only group, separate from the Gemini pool.
  if (id.startsWith("gemini-argon-")) return "argon";
  return undefined;
}

function antigravityModelPool(modelKey: string) {
  if (modelKey.startsWith("gemini-")) return modelKey.includes("-argon") ? "argon" : "google";
  if (/^(claude-|gpt-)/.test(modelKey)) return "other";
  return undefined;
}

const poolOrder = { google: 0, argon: 1, other: 2 } as const;

export function prioritizeAntigravityWindows(
  windows: RuntimeProviderUsageWindow[],
  model: string | undefined,
) {
  const modelKey = model?.trim().toLowerCase() ?? "";
  const exact = windows.some((row) => row.id.toLowerCase() === modelKey && !antigravityQuotaPool(row.id));
  const currentPool = exact ? undefined : antigravityModelPool(modelKey);
  const rows = [...windows];
  for (const [id, group, duration] of [
    ["gemini-5h", "Gemini Models", 300],
    ["gemini-weekly", "Gemini Models", 10080],
    ["3p-5h", "Claude and GPT models", 300],
    ["3p-weekly", "Claude and GPT models", 10080],
  ] as const) {
    if (windows.some((row) => antigravityQuotaPool(row.id)) && !rows.some((row) => row.id === id)) {
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
      order: pool ? poolOrder[pool] : 3,
    };
  }).sort((a, b) =>
    Number(b.current) - Number(a.current) || a.order - b.order ||
    (a.window.window_duration_mins ?? Infinity) - (b.window.window_duration_mins ?? Infinity) || a.index - b.index,
  ).map(({ window, current, displayLabel }) => ({ window, current, displayLabel }));
}
