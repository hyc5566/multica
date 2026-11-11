import { app, BrowserWindow, ipcMain } from "electron";
import { createHash, randomUUID } from "node:crypto";
import { execFile, spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { access, constants, mkdir, readFile, readdir, rm, writeFile } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { Readable, Transform } from "node:stream";
import { pipeline } from "node:stream/promises";
import { z } from "zod";
import { loadUpdaterPreferences, saveUpdaterPreferences, updaterPreferencesPath } from "./updater-preferences";
import { createZhTWUpdateController } from "./zh-tw-update-controller";
import { SWAP_SCRIPT } from "./zh-tw-update-swap";
export { SWAP_SCRIPT } from "./zh-tw-update-swap";
export { STARTUP_CHECK_DELAY_MS, CHECK_INTERVAL_MS } from "./zh-tw-update-controller";

// The locally signed Taiwan build must never consume electron-updater's feed.
export const DEFAULT_RELEASE_MANIFEST_URL = "https://10.1.24.90:45671/downloads/latest.json";
const MAX_MANIFEST_BYTES = 64 * 1024;
const MAX_APP_ZIP_BYTES = 1024 * 1024 * 1024;
const ZH_TW_VERSION = /^(\d+)\.(\d+)\.(\d+)-zh-tw\.(\d+)$/;

const httpsUrl = z.string().url().refine((value) => new URL(value).protocol === "https:", "https only");
const manifestSchema = z.object({
  version: z.string().regex(ZH_TW_VERSION),
  notes_url: httpsUrl.optional(),
  desktop: z.record(z.string(), z.object({
    url: httpsUrl,
    sha256: z.string().regex(/^[0-9a-fA-F]{64}$/),
    size: z.number().int().positive().max(MAX_APP_ZIP_BYTES),
  })).default({}),
});
export type ReleaseManifest = z.infer<typeof manifestSchema>;
export type AppUpdate = {
  version: string;
  url: string;
  sha256: string;
  size: number;
  notesUrl?: string;
};

export function parseReleaseManifest(text: string): ReleaseManifest {
  if (Buffer.byteLength(text) > MAX_MANIFEST_BYTES) throw new Error("release manifest too large");
  return manifestSchema.parse(JSON.parse(text));
}

function versionParts(version: string): number[] | null {
  const match = ZH_TW_VERSION.exec(version.trim());
  return match ? match.slice(1).map(Number) : null;
}

/** Only two Taiwan release versions compare; dev builds never auto-update. */
export function isNewerZhTWVersion(latest: string, current: string): boolean {
  const next = versionParts(latest);
  const now = versionParts(current);
  if (!next || !now) return false;
  for (let i = 0; i < next.length; i += 1) {
    if (next[i] !== now[i]) return next[i]! > now[i]!;
  }
  return false;
}

/** The channel lives on the server the App is connected to. */
export function manifestUrlFor(apiBaseUrl: string | null): string {
  if (!apiBaseUrl) return DEFAULT_RELEASE_MANIFEST_URL;
  try {
    const origin = new URL(apiBaseUrl);
    if (origin.protocol !== "https:") return DEFAULT_RELEASE_MANIFEST_URL;
    return `${origin.origin}/downloads/latest.json`;
  } catch {
    return DEFAULT_RELEASE_MANIFEST_URL;
  }
}

export function platformKey(platform: NodeJS.Platform, arch: string): string | null {
  return platform === "darwin" && arch === "arm64" ? "darwin-arm64" : null;
}

export function findAppUpdate(
  manifest: ReleaseManifest,
  currentVersion: string,
  key: string | null,
): AppUpdate | null {
  if (!key || !isNewerZhTWVersion(manifest.version, currentVersion)) return null;
  const asset = manifest.desktop[key];
  if (!asset) return null;
  return {
    version: manifest.version,
    url: asset.url,
    sha256: asset.sha256.toLowerCase(),
    size: asset.size,
    notesUrl: manifest.notes_url,
  };
}

/**
 * Why this bundle cannot replace itself, or null when it can. A translocated
 * or mounted copy is read-only, so asking before a long download would only
 * end in an error.
 */
export async function bundleNotUpdatableReason(bundle: string): Promise<string | null> {
  if (bundle.includes("/AppTranslocation/") || bundle.startsWith("/Volumes/")) {
    return "App 目前是從「下載項目」或磁碟映像直接開啟。";
  }
  try {
    await access(dirname(bundle), constants.W_OK);
    return null;
  } catch {
    return `沒有權限寫入 ${dirname(bundle)}。`;
  }
}

/** `/Applications/X.app/Contents/MacOS/X` → `/Applications/X.app`. */
export function appBundlePath(exePath: string): string | null {
  const macos = dirname(exePath);
  const contents = dirname(macos);
  const bundle = dirname(contents);
  if (basename(macos) !== "MacOS" || basename(contents) !== "Contents" || !bundle.endsWith(".app")) {
    return null;
  }
  return bundle;
}

function run(file: string, args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(file, args, { timeout: 5 * 60_000 }, (err, stdout, stderr) => {
      if (err) reject(new Error(`${basename(file)} failed: ${stderr || err.message}`));
      else resolve(stdout.trim());
    });
  });
}

export async function fetchManifest(url: string, signal: AbortSignal): Promise<ReleaseManifest> {
  // No redirects: the channel names its own origin and nothing else.
  const res = await fetch(url, { redirect: "error", signal: AbortSignal.any([signal, AbortSignal.timeout(15_000)]) });
  if (!res.ok) throw new Error(`release manifest HTTP ${res.status}`);
  if (!res.body) throw new Error("release manifest has no body");
  const chunks: Uint8Array[] = [];
  let bytes = 0;
  for await (const chunk of Readable.fromWeb(res.body as never)) {
    bytes += chunk.length;
    if (bytes > MAX_MANIFEST_BYTES) throw new Error("release manifest too large");
    chunks.push(chunk);
  }
  return parseReleaseManifest(Buffer.concat(chunks).toString("utf8"));
}

export async function downloadVerified(
  update: AppUpdate, destination: string, onProgress: (percent: number) => void, signal: AbortSignal,
): Promise<void> {
  const res = await fetch(update.url, { redirect: "error", signal: AbortSignal.any([signal, AbortSignal.timeout(30 * 60_000)]) });
  if (!res.ok || !res.body) throw new Error(`下載失敗（HTTP ${res.status}）`);
  const hash = createHash("sha256");
  let size = 0;
  let lastPercent = -1;
  const meter = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      size += chunk.length;
      if (size > update.size) {
        callback(new Error("下載大小超過發行資訊"));
        return;
      }
      const percent = Math.min(99, Math.floor(size * 100 / update.size));
      if (percent !== lastPercent) { lastPercent = percent; onProgress(percent); }
      hash.update(chunk);
      callback(null, chunk);
    },
  });
  await pipeline(Readable.fromWeb(res.body as never), meter, createWriteStream(destination, { mode: 0o600 }), { signal });
  if (size !== update.size || hash.digest("hex") !== update.sha256) {
    throw new Error("下載檔案的大小或 SHA-256 與發行資訊不符");
  }
}

async function plistValue(bundle: string, key: string): Promise<string> {
  return run("/usr/libexec/PlistBuddy", ["-c", `Print :${key}`, join(bundle, "Contents/Info.plist")]);
}

/** Download, verify and stage the new bundle; returns the staged path. */
export async function stageUpdate(
  update: AppUpdate, currentBundle: string, workDir: string,
  onProgress: (percent: number) => void, signal: AbortSignal,
): Promise<string> {
  const notUpdatable = await bundleNotUpdatableReason(currentBundle);
  if (notUpdatable) throw new Error(notUpdatable);
  await rm(join(workDir, "staging"), { recursive: true, force: true });
  await mkdir(join(workDir, "staging"), { recursive: true });
  const zip = join(workDir, `multica-desktop-${update.version}.zip`);
  try {
    await downloadVerified(update, zip, onProgress, signal);
    signal.throwIfAborted();
    await run("/usr/bin/ditto", ["-x", "-k", zip, join(workDir, "staging")]);
  } finally {
    await rm(zip, { force: true });
  }
  signal.throwIfAborted();
  // The archive's bundle name need not match a renamed local copy; the swap
  // installs it under the current path. Bundle ID and version decide.
  const bundles = (await readdir(join(workDir, "staging"))).filter((name) => name.endsWith(".app"));
  if (bundles.length !== 1) throw new Error("更新檔內應該剛好有一個 App");
  const staged = join(workDir, "staging", bundles[0]!);
  const [id, currentId, version] = await Promise.all([
    plistValue(staged, "CFBundleIdentifier"),
    plistValue(currentBundle, "CFBundleIdentifier"),
    plistValue(staged, "CFBundleShortVersionString"),
  ]);
  if (id !== currentId) throw new Error(`新版 bundle ID ${id} 與目前 ${currentId} 不同`);
  if (version !== update.version) throw new Error(`新版版本 ${version} 與發行資訊 ${update.version} 不同`);
  await run("/usr/bin/codesign", ["--verify", "--deep", "--strict", staged]);
  signal.throwIfAborted();
  return staged;
}

export type ZhTWAppUpdateDeps = {
  getMainWindow: () => BrowserWindow | null;
  getApiBaseUrl: () => string | null;
  /** Keep the daemon (and its running tasks) alive through the update quit. */
  preserveDaemonOnQuit: () => (() => void) | void;
};

const pendingLaunchSchema = z.object({
  token: z.string().uuid(), version: z.string().regex(ZH_TW_VERSION), bundle: z.string(),
});

/** Only the matching installed App can acknowledge its own renderer readiness. */
export async function acknowledgeUpdateReady(
  workDir: string, version: string, bundle: string | null, argv: string[],
): Promise<void> {
  const token = argv.find((arg) => arg.startsWith("--zh-tw-update-token="))?.split("=")[1];
  if (!token) return;
  const pending = pendingLaunchSchema.parse(JSON.parse(await readFile(join(workDir, "pending-launch.json"), "utf8")));
  if (pending.token !== token || pending.version !== version || pending.bundle !== bundle) return;
  await writeFile(join(workDir, `ready-${pending.token}`), "ready\n", { mode: 0o600 });
}

export function setupZhTWAppUpdate(deps: ZhTWAppUpdateDeps): void {
  const key = platformKey(process.platform, process.arch);
  const currentBundle = appBundlePath(app.getPath("exe"));
  const currentVersion = app.getVersion();
  const supported = app.isPackaged && key && currentBundle && versionParts(currentVersion);
  const workDir = join(app.getPath("userData"), "zh-tw-update");
  const preferencesFile = updaterPreferencesPath(app.getPath("userData"));
  let quittingForUpdate = false;
  const controller = createZhTWUpdateController({
    currentVersion,
    unsupportedReason: supported ? null : "自動更新目前僅支援已安裝的 macOS Apple Silicon 繁中正式版。",
    loadPreferences: () => loadUpdaterPreferences(preferencesFile),
    savePreferences: (preferences) => saveUpdaterPreferences(preferencesFile, preferences),
    fetchManifest: (signal) => fetchManifest(manifestUrlFor(deps.getApiBaseUrl()), signal),
    findUpdate: (manifest) => findAppUpdate(manifest, currentVersion, key),
    stage: (update, progress, signal) => stageUpdate(update, currentBundle!, workDir, progress, signal),
    publish: (status) => {
      for (const window of BrowserWindow.getAllWindows()) {
        if (!window.isDestroyed() && !window.webContents.isDestroyed()) window.webContents.send("updater:status", status);
      }
      if (status.phase === "ready") queueMicrotask(() => {
        if (BrowserWindow.getAllWindows().length === 0) void controller.install(true).catch(console.error);
      });
    },
    install: async (staged, update, shouldContinue) => {
      const reason = await bundleNotUpdatableReason(currentBundle!);
      if (reason) throw new Error(reason);
      const executable = await plistValue(staged, "CFBundleExecutable");
      if (!executable || basename(executable) !== executable || executable === "." || executable === "..") {
        throw new Error("新版 App 的執行檔名稱無效。");
      }
      await access(join(staged, "Contents/MacOS", executable), constants.X_OK);
      const token = randomUUID();
      const script = join(workDir, "swap.sh");
      await writeFile(script, SWAP_SCRIPT, { mode: 0o700 });
      await rm(join(workDir, "result"), { force: true });
      await writeFile(join(workDir, "pending-launch.json"), JSON.stringify({ token, version: update.version, bundle: currentBundle }), { mode: 0o600 });
      if (!shouldContinue()) throw new Error("自動安裝已暫停，App 尚未變更。");
      const rollback = join(workDir, "rollback", basename(currentBundle!));
      const child = spawn("/bin/bash", [script, String(process.pid), currentBundle!, staged, rollback,
        join(workDir, "swap.log"), executable, token, join(workDir, `ready-${token}`), join(workDir, "result")], {
        detached: true, stdio: "ignore",
      });
      await new Promise<void>((resolve, reject) => { child.once("spawn", resolve); child.once("error", reject); });
      if (!shouldContinue()) { child.kill(); throw new Error("自動安裝已暫停，App 尚未變更。"); }
      child.unref();
      quittingForUpdate = true;
      const releasePreservation = deps.preserveDaemonOnQuit();
      child.once("exit", () => {
        // A live parent means quit was cancelled: the helper never swapped it.
        quittingForUpdate = false;
        releasePreservation?.();
        controller.reportInstallError("App 尚未結束，更新未安裝。請儲存內容後重試。");
      });
      app.quit();
    },
  });

  // Register even on unsupported platforms so settings always have an answer.
  ipcMain.handle("updater:get-preferences", () => controller.getPreferences());
  ipcMain.handle("updater:set-automatic-updates", (_event, enabled: unknown) => controller.setAutomaticUpdates(enabled));
  ipcMain.handle("updater:check", () => controller.check());
  ipcMain.handle("updater:download", async () => { await controller.check(); });
  ipcMain.handle("updater:install", () => controller.install());
  ipcMain.handle("updater:get-status", async () => { await controller.ready; return controller.getStatus(); });
  ipcMain.handle("updater:defer", () => controller.defer());
  ipcMain.on("updater:renderer-ready", (event) => {
    const window = deps.getMainWindow();
    if (!supported || !window || event.sender !== window.webContents) return;
    void acknowledgeUpdateReady(workDir, currentVersion, currentBundle, process.argv).catch(console.error);
  });

  // Only user-initiated quit / closing every window is a safe automatic restart.
  // This handler must precede daemon-manager's quit handler.
  app.on("before-quit", (event) => {
    if (quittingForUpdate) return;
    if (controller.getStatus().phase === "installing") { event.preventDefault(); return; }
    if (!controller.canAutoInstall()) return;
    event.preventDefault();
    void controller.install(true).catch(console.error);
  });
  app.on("window-all-closed", () => { void controller.install(true).catch(console.error); });
  app.on("will-quit", () => controller.dispose());
  void controller.ready.then(async () => {
    const result = await readFile(join(workDir, "result"), "utf8").catch(() => "");
    if (!result.trim() || result.trim() === "installed") return;
    controller.reportStartupError(result.trim() === "restored"
      ? "新版 App 未在 60 秒內就緒或在啟動時結束，已還原舊版。可重試更新。"
      : "App 更新未完成，請查看更新紀錄與保留的舊版，確認後再重試。");
    await rm(join(workDir, "result"), { force: true });
  }).catch(console.error);
}
