import { lstat, realpath } from "node:fs/promises";
import { delimiter, join } from "node:path";

export async function verifyCLIEntries(binary: string, registered: string, managed: string, pathValue: string, platform: NodeJS.Platform): Promise<void> {
  const actual = await realpath(binary);
  if (!registered.startsWith("/") || await realpath(registered) !== actual) throw new Error("已登記的 CLI 入口不屬於目前 App，請人工處置。");
  const dirs = pathValue.split(delimiter);
  if (platform === "darwin") dirs.push("/opt/homebrew/bin", "/usr/local/bin");
  for (const candidate of [managed, ...dirs.map((dir) => join(dir || ".", "multica"))]) {
    try { await lstat(candidate); }
    catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") continue; throw error; }
    const target = await realpath(candidate).catch(() => "");
    if (target !== actual) throw new Error(`偵測到獨立 CLI 入口 ${candidate}；請人工處置後再更新。`);
  }
}
