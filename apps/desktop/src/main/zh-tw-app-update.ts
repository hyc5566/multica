import { app, dialog, type BrowserWindow } from "electron";
import { createHash } from "node:crypto";
import { execFile, spawn } from "node:child_process";
import { createWriteStream } from "node:fs";
import { access, constants, mkdir, readdir, rm, writeFile } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { Readable, Transform } from "node:stream";
import { pipeline } from "node:stream/promises";
import { z } from "zod";
import { openExternalSafely } from "./external-url";

// The Taiwan build is locally signed and not published to the upstream update
// feed, so electron-updater stays off. Instead the App reads the s90 release
// channel (latest.json, published with each release), asks the user, then
// downloads, verifies and swaps its own bundle — no agent involved.
export const DEFAULT_RELEASE_MANIFEST_URL = "https://10.1.24.90:45671/downloads/latest.json";
export const STARTUP_CHECK_DELAY_MS = 30_000;
export const CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000;
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

// Runs after the App exits: move the old bundle aside as the single rollback
// copy, move the verified bundle into place, and reopen it. Any failure puts
// the old bundle back, so the user is never left without an App.
export const SWAP_SCRIPT = `#!/bin/bash
set -u
pid=$1 current=$2 next=$3 rollback=$4 log=$5
exec >>"$log" 2>&1
echo "[$(date -u +%FT%TZ)] swap $current"
for _ in $(seq 1 240); do kill -0 "$pid" 2>/dev/null || break; sleep 0.5; done
if kill -0 "$pid" 2>/dev/null; then echo "App still running; update skipped"; exit 1; fi
rm -rf -- "$rollback"
mkdir -p -- "$(dirname "$rollback")"
if ! mv -- "$current" "$rollback"; then echo "cannot move current bundle"; open -- "$current"; exit 1; fi
if ! mv -- "$next" "$current"; then
  echo "cannot install new bundle; restoring"
  [ -e "$current" ] && rm -rf -- "$current"
  mv -- "$rollback" "$current"
  open -- "$current"
  exit 1
fi
xattr -dr com.apple.quarantine "$current" 2>/dev/null || true
if open -- "$current"; then echo "installed; rollback kept at $rollback"; exit 0; fi
echo "new bundle failed to launch; restoring the previous one"
failed="$(dirname "$rollback")/failed-$(basename "$current")"
rm -rf -- "$failed"
mv -- "$current" "$failed" && mv -- "$rollback" "$current"
open -- "$current"
exit 1
`;

function run(file: string, args: string[]): Promise<string> {
  return new Promise((resolve, reject) => {
    execFile(file, args, { timeout: 5 * 60_000 }, (err, stdout, stderr) => {
      if (err) reject(new Error(`${basename(file)} failed: ${stderr || err.message}`));
      else resolve(stdout.trim());
    });
  });
}

async function fetchManifest(url: string): Promise<ReleaseManifest> {
  // No redirects: the channel names its own origin and nothing else.
  const res = await fetch(url, { redirect: "error", signal: AbortSignal.timeout(15_000) });
  if (!res.ok) throw new Error(`release manifest HTTP ${res.status}`);
  return parseReleaseManifest(await res.text());
}

async function downloadVerified(update: AppUpdate, destination: string): Promise<void> {
  const res = await fetch(update.url, { redirect: "error", signal: AbortSignal.timeout(30 * 60_000) });
  if (!res.ok || !res.body) throw new Error(`下載失敗（HTTP ${res.status}）`);
  const hash = createHash("sha256");
  let size = 0;
  const meter = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      size += chunk.length;
      if (size > update.size) {
        callback(new Error("下載大小超過發行資訊"));
        return;
      }
      hash.update(chunk);
      callback(null, chunk);
    },
  });
  await pipeline(Readable.fromWeb(res.body as never), meter, createWriteStream(destination, { mode: 0o600 }));
  if (size !== update.size || hash.digest("hex") !== update.sha256) {
    throw new Error("下載檔案的大小或 SHA-256 與發行資訊不符");
  }
}

async function plistValue(bundle: string, key: string): Promise<string> {
  return run("/usr/libexec/PlistBuddy", ["-c", `Print :${key}`, join(bundle, "Contents/Info.plist")]);
}

/** Download, verify and stage the new bundle; returns the staged path. */
async function stageUpdate(update: AppUpdate, currentBundle: string, workDir: string): Promise<string> {
  const notUpdatable = await bundleNotUpdatableReason(currentBundle);
  if (notUpdatable) throw new Error(notUpdatable);
  await rm(join(workDir, "staging"), { recursive: true, force: true });
  await mkdir(join(workDir, "staging"), { recursive: true });
  const zip = join(workDir, `multica-desktop-${update.version}.zip`);
  await downloadVerified(update, zip);
  await run("/usr/bin/ditto", ["-x", "-k", zip, join(workDir, "staging")]);
  await rm(zip, { force: true });
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
  return staged;
}

export type ZhTWAppUpdateDeps = {
  getMainWindow: () => BrowserWindow | null;
  getApiBaseUrl: () => string | null;
  /** Keep the daemon (and its running tasks) alive through the update quit. */
  preserveDaemonOnQuit: () => void;
};

export function setupZhTWAppUpdate(deps: ZhTWAppUpdateDeps): void {
  const key = platformKey(process.platform, process.arch);
  const currentBundle = appBundlePath(app.getPath("exe"));
  if (!app.isPackaged || !key || !currentBundle) return;
  const workDir = join(app.getPath("userData"), "zh-tw-update");
  let busy = false;

  const showError = async (err: unknown): Promise<void> => {
    const message = err instanceof Error ? err.message : String(err);
    console.error("[zh-tw-update]", message);
    await dialog.showMessageBox({
      type: "error",
      title: "更新失敗",
      message: "Multica 繁中版更新失敗，目前版本沒有變更。",
      detail: `${message}\n\n可稍後再試，或到下載頁手動下載新版。`,
    });
  };

  const install = async (update: AppUpdate): Promise<void> => {
    const staged = await stageUpdate(update, currentBundle, workDir);
    const script = join(workDir, "swap.sh");
    await writeFile(script, SWAP_SCRIPT, { mode: 0o700 });
    const rollback = join(workDir, "rollback", basename(currentBundle));
    spawn("/bin/bash", [script, String(process.pid), currentBundle, staged, rollback, join(workDir, "swap.log")], {
      detached: true,
      stdio: "ignore",
    }).unref();
    // Running agent tasks belong to the daemon, not to this window: keep it
    // alive across the swap instead of stopping it (which would cancel them).
    deps.preserveDaemonOnQuit();
    app.quit();
  };

  const check = async (): Promise<void> => {
    if (busy) return;
    busy = true;
    try {
      const manifest = await fetchManifest(manifestUrlFor(deps.getApiBaseUrl()));
      const update = findAppUpdate(manifest, app.getVersion(), key);
      if (!update) return;
      const notUpdatable = await bundleNotUpdatableReason(currentBundle);
      if (notUpdatable) {
        await dialog.showMessageBox({
          type: "info",
          title: "Multica 繁中版有新版本",
          message: `有新版本 ${update.version}，但這個位置的 App 無法自動更新。`,
          detail: `${notUpdatable}\n請結束 App，把它移到「應用程式」資料夾後重新開啟，之後就能自動更新。`,
        });
        return;
      }
      const window = deps.getMainWindow();
      const options = {
        type: "info" as const,
        title: "Multica 繁中版有新版本",
        message: `可以更新到 ${update.version}（目前 ${app.getVersion()}）。`,
        detail:
          "App 會直接下載並核對 SHA-256 與簽章，完成後重新啟動。執行中的任務不會中斷；" +
          "內附的 CLI 與 daemon 會在任務結束後換成新版。",
        buttons: update.notesUrl ? ["立即更新", "稍後提醒", "查看版本說明"] : ["立即更新", "稍後提醒"],
        defaultId: 0,
        cancelId: 1,
      };
      const { response } = window && !window.isDestroyed()
        ? await dialog.showMessageBox(window, options)
        : await dialog.showMessageBox(options);
      if (response === 2 && update.notesUrl) void openExternalSafely(update.notesUrl);
      if (response !== 0) return;
      await install(update).catch(showError);
    } catch (err) {
      // A failed background check is retried at the next interval.
      console.warn("[zh-tw-update] check failed:", err);
    } finally {
      busy = false;
    }
  };

  setTimeout(() => void check(), STARTUP_CHECK_DELAY_MS);
  setInterval(() => void check(), CHECK_INTERVAL_MS);
}
