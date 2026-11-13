import { app, BrowserWindow, ipcMain } from "electron";
import { createHash, randomUUID } from "node:crypto";
import { execFile, spawn } from "node:child_process";
import { createReadStream, createWriteStream, existsSync, readFileSync } from "node:fs";
import { access, constants, mkdir, open, readFile, readdir, rename, rm } from "node:fs/promises";
import { basename, dirname, join } from "node:path";
import { Readable, Transform } from "node:stream";
import { pipeline } from "node:stream/promises";
import { z } from "zod";
import { loadUpdaterPreferences, saveUpdaterPreferences, updaterPreferencesPath } from "./updater-preferences";
import { createZhTWUpdateController } from "./zh-tw-update-controller";
import type { CoordinatedDaemonSnapshot } from "./daemon-manager";
import { managedCliPath } from "./cli-bootstrap";
import { verifyCLIEntries } from "./zh-tw-cli-entry";
export { SWAP_SCRIPT } from "./zh-tw-update-swap";
export { STARTUP_CHECK_DELAY_MS, CHECK_INTERVAL_MS } from "./zh-tw-update-controller";

// The locally signed Taiwan build must never consume electron-updater's feed.
export const DEFAULT_RELEASE_MANIFEST_URL = "https://10.1.24.90:45671/downloads/latest.json";
const MAX_MANIFEST_BYTES = 64 * 1024;
const MAX_APP_ZIP_BYTES = 1024 * 1024 * 1024;
const ZH_TW_VERSION = /^(\d+)\.(\d+)\.(\d+)-zh-tw\.(\d+)(?:\+up\.[0-9a-f]{7})?$/;

const httpsUrl = z.string().url().refine((value) => new URL(value).protocol === "https:", "https only");
const desktopAssetSchema = z.object({
  url: httpsUrl,
  sha256: z.string().regex(/^[0-9a-fA-F]{64}$/),
  size: z.number().int().positive().max(MAX_APP_ZIP_BYTES),
  binary_sha256: z.string().optional(),
  binary_version: z.string().optional(),
  binary_commit: z.string().optional(),
  data_compatibility: z.string().optional(),
  bundle_id: z.string().optional(),
});
const manifestSchema = z.object({
  version: z.string().regex(ZH_TW_VERSION),
  source_id: z.string().optional(),
  commit: z.string().optional(),
  data_compatibility: z.string().optional(),
  coordinated_update_protocol: z.number().optional(),
  notes_url: httpsUrl.optional(),
  desktop: z.record(z.string(), desktopAssetSchema).default({}),
  coordinated_desktop: z.record(z.string(), desktopAssetSchema).default({}),
});
export type ReleaseManifest = z.infer<typeof manifestSchema>;
export type AppUpdate = {
  version: string;
  url: string;
  sha256: string;
  size: number;
  notesUrl?: string;
  binarySha256?: string;
  binaryVersion?: string;
  binaryCommit?: string;
  dataCompatibility?: string;
  bundleId?: string;
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
  if (manifest.coordinated_update_protocol !== 1) throw new Error("此版本不支援整組協調更新。");
  const asset = manifest.coordinated_desktop[key];
  if (!asset) return null;
  if (manifest.source_id !== "hyc5566/multica" || manifest.data_compatibility !== "multica-profile-v1" || asset.bundle_id !== "ai.multica.desktop.zh-tw.local" ||
      asset.data_compatibility !== manifest.data_compatibility || !/^[a-f0-9]{40}$/i.test(manifest.commit ?? "") ||
      !/^[a-f0-9]{64}$/i.test(asset.binary_sha256 ?? "") || asset.binary_version !== manifest.version ||
      asset.binary_commit?.toLowerCase() !== manifest.commit?.toLowerCase()) {
    throw new Error("此版本缺少整組更新所需的來源與執行檔驗證資訊，請使用支援協調更新的發行版本。");
  }
  return {
    version: manifest.version,
    url: asset.url,
    sha256: asset.sha256.toLowerCase(),
    size: asset.size,
    notesUrl: manifest.notes_url,
    binarySha256: asset.binary_sha256!.toLowerCase(),
    binaryVersion: asset.binary_version,
    binaryCommit: asset.binary_commit!.toLowerCase(),
    dataCompatibility: asset.data_compatibility,
    bundleId: asset.bundle_id,
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
  const journal = await readFile(join(workDir, "journal.json"), "utf8").catch((error: NodeJS.ErrnoException) => {
    if (error.code === "ENOENT") return "";
    throw error;
  });
  if (journal) {
    const previous = z.object({ phase: z.string() }).parse(JSON.parse(journal));
    if (!["complete", "restored", "no_change"].includes(previous.phase)) throw new Error("前一次整組更新尚未完成，請先恢復或驗收。");
  }
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
  if (id !== currentId || id !== update.bundleId) throw new Error(`新版 bundle ID ${id} 與目前或發行資訊不同`);
  if (version !== update.version) throw new Error(`新版版本 ${version} 與發行資訊 ${update.version} 不同`);
  await run("/usr/bin/codesign", ["--verify", "--deep", "--strict", staged]);
  const embedded = join(staged, "Contents/Resources/app.asar.unpacked/resources/bin/multica");
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(embedded)) hash.update(chunk);
  if (!update.binarySha256 || hash.digest("hex") !== update.binarySha256) throw new Error("App 內附 CLI 的 SHA-256 與發行資訊不符。");
  const versionInfo = JSON.parse(await run(embedded, ["version", "--output", "json"])) as { version?: unknown; build_commit?: unknown; daemon_maintenance_gate?: unknown; daemon_maintenance_shutdown?: unknown; data_compatibility?: unknown };
  if (versionInfo.version !== update.binaryVersion || versionInfo.build_commit !== update.binaryCommit ||
      versionInfo.daemon_maintenance_gate !== "v1" || versionInfo.daemon_maintenance_shutdown !== "v1" ||
      versionInfo.data_compatibility !== update.dataCompatibility) {
    throw new Error("App 內附 CLI 的版本或來源與發行資訊不符。");
  }
  signal.throwIfAborted();
  return staged;
}

export type ZhTWAppUpdateDeps = {
  getMainWindow: () => BrowserWindow | null;
  getApiBaseUrl: () => string | null;
  /** Keep the daemon (and its running tasks) alive through the update quit. */
  preserveDaemonOnQuit: () => (() => void) | void;
  withCoordinatedDaemon: <T>(work: (snapshot: CoordinatedDaemonSnapshot) => Promise<T>) => Promise<T>;
};

const coordinatedReadySchema = z.object({
  token: z.string().uuid(), phase: z.literal("verifying_new"), new_version: z.string(),
  old_bundle: z.string(), new_app_pid: z.number().int().positive(), ready_file: z.string(),
});

/** A legacy renderer-only token cannot attest a coordinated update. */
export async function acknowledgeUpdateReady(
  workDir: string, version: string, bundle: string | null, argv: string[],
): Promise<void> {
  const token = argv.find((arg) => arg.startsWith("--zh-tw-coordinated-token="))?.slice("--zh-tw-coordinated-token=".length);
  if (!token || !z.string().uuid().safeParse(token).success) return;
  for (let attempt = 0; attempt < 50; attempt += 1) {
    const data = await readFile(join(workDir, "journal.json"), "utf8").catch(() => "");
    const parsed = coordinatedReadySchema.safeParse(data ? JSON.parse(data) : null);
    if (parsed.success) {
      const pending = parsed.data;
      if (pending.token !== token || pending.new_version !== version || pending.old_bundle !== bundle ||
          pending.ready_file !== join(workDir, `ready-${token}`)) return;
      if (pending.new_app_pid === process.pid) {
        await writeDurableJSON(pending.ready_file, { token, pid: process.pid });
        return;
      }
    }
    await new Promise((resolve) => setTimeout(resolve, 100));
  }
}

const ledgerSchema = z.object({
  data_compatibility: z.literal("multica-profile-v1"),
  cli_entry: z.string(),
  current: z.object({ version: z.string(), commit: z.string(), binary_sha256: z.string() }),
  previous: z.object({ binary_sha256: z.string().optional() }).default({}),
});

async function binaryHash(path: string): Promise<string> {
  const hash = createHash("sha256");
  for await (const chunk of createReadStream(path)) hash.update(chunk);
  return hash.digest("hex");
}

function bundledCLI(bundle: string): string {
  return join(bundle, "Contents/Resources/app.asar.unpacked/resources/bin/multica");
}

async function writeDurableJSON(path: string, value: unknown): Promise<void> {
  const temporary = `${path}.${randomUUID()}.tmp`;
  const file = await open(temporary, "wx", 0o600);
  try { await file.writeFile(JSON.stringify(value)); await file.sync(); } finally { await file.close(); }
  await rename(temporary, path);
  const dir = await open(dirname(path), "r");
  try { await dir.sync(); } finally { await dir.close(); }
}

async function abortBeforeHandoff(bundle: string, workDir: string, journal: string, token: string): Promise<void> {
  const log = await open(join(workDir, "coordinator.log"), "a", 0o600);
  const child = spawn(bundledCLI(bundle), ["daemon", "coordinated-update", "--journal", journal, "--token", token, "--abort-before-handoff-app-pid", String(process.pid)], { stdio: ["ignore", log.fd, log.fd] });
  const finished = new Promise<void>((resolve, reject) => {
    child.once("error", reject);
    child.once("exit", (code) => code === 0 ? resolve() : reject(new Error("更新中止程序未完成。")));
  });
  try { await Promise.all([log.close(), finished]); }
  finally {
    const raw = await readFile(journal, "utf8").catch(() => "");
    const parsed = z.object({ phase: z.string() }).safeParse(raw ? JSON.parse(raw) : null);
    if (parsed.success && ["rolling_back", "rollback_awaiting_acceptance"].includes(parsed.data.phase)) watchCoordinatedStartup(() => {});
  }
}

async function prepareJournal(
  workDir: string, currentBundle: string, currentVersion: string, staged: string, executable: string,
  update: AppUpdate, snapshot: CoordinatedDaemonSnapshot, token: string,
): Promise<string> {
  const ledger = ledgerSchema.parse(JSON.parse(await readFile(join(workDir, "ledger.json"), "utf8").catch(() => {
    throw new Error("缺少已核准的目前版本紀錄，無法更新。");
  })));
  if (ledger.current.version !== currentVersion || snapshot.version !== currentVersion || ledger.current.commit !== snapshot.commit ||
      ledger.current.binary_sha256 !== snapshot.binarySHA256 || await binaryHash(bundledCLI(currentBundle)) !== snapshot.binarySHA256 ||
      !update.binarySha256 || !update.binaryVersion || !update.binaryCommit || update.dataCompatibility !== ledger.data_compatibility) {
    throw new Error("目前 App、daemon 或資料相容性與已核准紀錄不符，無法更新。");
  }
  if (update.binarySha256 === snapshot.binarySHA256) throw new Error("新版與目前 CLI 雜湊相同，無法安全切換。");
  await verifyCLIEntries(bundledCLI(currentBundle), ledger.cli_entry, managedCliPath(), process.env.PATH ?? "", process.platform);
  const rollback = join(workDir, "rollback", basename(currentBundle));
  const previousHash = ledger.previous.binary_sha256 ?? "";
  if (previousHash && await binaryHash(bundledCLI(rollback)) !== previousHash) throw new Error("保留的前版與紀錄不符。");
  if (!previousHash && await access(rollback).then(() => true, () => false)) throw new Error("發現未記錄的舊版，請人工確認。");
  const journal = join(workDir, "journal.json");
  const prior = await readFile(journal, "utf8").catch((error: NodeJS.ErrnoException) => {
    if (error.code === "ENOENT") return "";
    throw error;
  });
  if (prior) {
    const old = z.object({ token: z.string().uuid(), phase: z.enum(["complete", "restored", "no_change"]) }).parse(JSON.parse(prior));
    await rename(journal, join(workDir, `journal-${old.token}.json`));
  }
  const record = {
    journal_version: 1, token, phase: "prepared", profile: snapshot.profile, cli_entry: ledger.cli_entry, daemon_id: snapshot.daemonId, server_url_sha256: snapshot.serverURLSHA256,
    daemon_pid: snapshot.pid, daemon_started_at: snapshot.startedAt, health_port: snapshot.port, app_pid: process.pid,
    gate_path: snapshot.gatePath, gate_identity: "", old_bundle: currentBundle, staged_bundle: staged,
    rollback_bundle: rollback, older_bundle: join(workDir, "older", basename(currentBundle)),
    old_binary_sha256: snapshot.binarySHA256, new_binary_sha256: update.binarySha256,
    old_version: currentVersion, new_version: update.binaryVersion, old_commit: snapshot.commit, new_commit: update.binaryCommit,
    data_compatibility: update.dataCompatibility, old_data_compatibility: ledger.data_compatibility,
    previous_rollback_sha256: previousHash, runtime_ids: snapshot.runtimeIDs, workspaces: snapshot.workspaces,
    new_app_executable: executable, ready_file: join(workDir, `ready-${token}`), handoff_file: join(workDir, `handoff-${token}`),
  };
  await writeDurableJSON(journal, record);
  return journal;
}

/** Resume pending journal work before allowing Desktop lifecycle operations. */
export function watchCoordinatedStartup(enableDaemon: () => void): void {
  const workDir = join(app.getPath("userData"), "zh-tw-update");
  const bundle = appBundlePath(app.getPath("exe"));
  const suppliedToken = process.argv.find((arg) => arg.startsWith("--zh-tw-coordinated-token="))?.slice("--zh-tw-coordinated-token=".length);
  if (!suppliedToken && !existsSync(join(workDir, "journal.json"))) { enableDaemon(); return; }
  let enabled = false;
  let lastResume = 0;
  let running = false;
  try {
    const initial = z.object({ token: z.string().uuid(), phase: z.string(), old_bundle: z.string(), old_version: z.string(), new_version: z.string() }).parse(JSON.parse(readFileSync(join(workDir, "journal.json"), "utf8")));
    const matches = initial.old_bundle === bundle && (!suppliedToken || initial.token === suppliedToken);
    if (matches && ((["awaiting_acceptance", "finalizing", "complete"].includes(initial.phase) && initial.new_version === app.getVersion()) ||
        (["restored", "rollback_awaiting_acceptance", "rollback_finalizing", "no_change"].includes(initial.phase) && initial.old_version === app.getVersion()))) { enabled = true; enableDaemon(); }
  } catch { /* An unreadable journal cannot authorize daemon management. */ }
  const poll = async () => {
    if (running) return;
    running = true;
    try {
      let raw: string;
      try { raw = await readFile(join(workDir, "journal.json"), "utf8"); }
      catch (error) {
        if ((error as NodeJS.ErrnoException).code === "ENOENT" && !suppliedToken && !enabled) { enabled = true; enableDaemon(); if (timer) clearInterval(timer); }
        return;
      }
      const journal = z.object({ token: z.string().uuid(), phase: z.string(), old_bundle: z.string(), old_version: z.string(), new_version: z.string(), new_binary_sha256: z.string().regex(/^[a-f0-9]{64}$/).optional(), legacy: z.unknown().optional() }).parse(JSON.parse(raw));
      if (!bundle || journal.old_bundle !== bundle || (suppliedToken && journal.token !== suppliedToken)) return;
      const stopPath = join(workDir, `stop-${journal.token}`);
      if (await readFile(stopPath, "utf8").catch(() => "") === journal.token) { app.quit(); return; }
      const completed = journal.phase === "complete" && journal.new_version === app.getVersion();
      const restored = ["restored", "no_change"].includes(journal.phase) && journal.old_version === app.getVersion();
      const rollbackPending = ["rollback_awaiting_acceptance", "rollback_finalizing"].includes(journal.phase) && journal.old_version === app.getVersion();
      const pending = ["awaiting_acceptance", "finalizing"].includes(journal.phase) && journal.new_version === app.getVersion();
      if ((completed || restored || pending || rollbackPending) && !enabled) { enabled = true; enableDaemon(); }
      if (completed || restored) { if (timer) clearInterval(timer); return; }
      if (Date.now() - lastResume >= 30_000) {
        lastResume = Date.now();
        const log = await open(join(workDir, "coordinator.log"), "a", 0o600);
        const legacy = journal.legacy !== undefined && journal.legacy !== null;
        const retained = join(workDir, "legacy-coordinator");
        const binary = legacy && existsSync(retained) ? retained : bundledCLI(bundle);
        if (legacy && (!journal.new_binary_sha256 || await binaryHash(binary).catch(() => "") !== journal.new_binary_sha256)) { await log.close(); throw new Error("整組更新的協調程式雜湊不符。"); }
        const child = spawn(binary, ["daemon", legacy ? "legacy-apply" : "coordinated-update", "--journal", join(workDir, "journal.json"), "--token", journal.token], { detached: true, stdio: ["ignore", log.fd, log.fd] });
        await log.close();
        child.on("error", console.error);
        child.unref();
      }
    } catch (error) { console.error("[update] cannot resume coordinated update", error); }
    finally { running = false; }
  };
  const timer = setInterval(() => { void poll(); }, 5_000);
  app.on("will-quit", () => { if (timer) clearInterval(timer); });
  void poll();
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
      await deps.withCoordinatedDaemon(async (snapshot) => {
        if (!shouldContinue()) throw new Error("自動安裝已暫停，App 尚未變更。");
        const token = randomUUID();
        const journal = await prepareJournal(workDir, currentBundle!, currentVersion, staged, executable, update, snapshot, token);
        const log = await open(join(workDir, "coordinator.log"), "a", 0o600);
        const child = spawn(bundledCLI(staged), ["daemon", "coordinated-update", "--journal", journal, "--token", token], {
          detached: true, stdio: ["ignore", log.fd, log.fd],
        });
        let exited = false;
        let spawned = false;
        child.once("exit", () => { exited = true; });
        const started = new Promise<void>((resolve, reject) => { child.once("spawn", () => { spawned = true; resolve(); }); child.once("error", reject); });
        const handoff = join(workDir, `handoff-${token}`);
        let confirmed = false;
        try {
          await Promise.all([log.close(), started]);
          for (let attempt = 0; attempt < 300; attempt += 1) {
            if (exited) throw new Error("整組更新準備失敗，請查看更新紀錄。");
            const raw = await readFile(handoff, "utf8").catch(() => "");
            if (raw) {
              const value = z.object({ token: z.string(), helper_pid: z.number() }).parse(JSON.parse(raw));
              if (value.token !== token || value.helper_pid !== child.pid) throw new Error("更新程序身分不符。");
              confirmed = true;
              break;
            }
            await new Promise((resolve) => setTimeout(resolve, 100));
          }
          if (!confirmed || exited) throw new Error("無法確認更新程序已接管，請查看更新紀錄。");
        } catch (error) {
          if (!spawned || exited) await abortBeforeHandoff(currentBundle!, workDir, journal, token).catch(console.error);
          throw error;
        }
        child.unref();
        quittingForUpdate = true;
        const releasePreservation = deps.preserveDaemonOnQuit();
        await new Promise<void>((resolve) => {
          const onExit = () => {
            quittingForUpdate = false;
            releasePreservation?.();
            void abortBeforeHandoff(currentBundle!, workDir, journal, token).catch(console.error).finally(() => {
              controller.reportInstallError("整組更新未完成，請查看更新紀錄。");
              resolve();
            });
          };
          child.once("exit", onExit);
          if (exited) { onExit(); return; }
          app.on("will-quit", () => resolve());
          app.quit();
        });
      });
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
  const pollAcceptance = async () => {
    const raw = await readFile(join(workDir, "journal.json"), "utf8").catch(() => "");
    if (!raw) return;
    const journal = z.object({ phase: z.string(), new_version: z.string(), old_version: z.string(), old_bundle: z.string() }).safeParse(JSON.parse(raw));
    if (!journal.success || journal.data.old_bundle !== currentBundle) return;
    const pendingNew = ["awaiting_acceptance", "finalizing"].includes(journal.data.phase) && journal.data.new_version === currentVersion;
    const pendingRollback = ["rollback_awaiting_acceptance", "rollback_finalizing"].includes(journal.data.phase) && journal.data.old_version === currentVersion;
    if ((pendingNew || pendingRollback) && controller.getStatus().phase !== "pending") controller.reportPending(currentVersion);
    if (["complete", "restored", "no_change"].includes(journal.data.phase) && controller.getStatus().phase === "pending") controller.reportAccepted();
  };
  const acceptanceTimer = setInterval(() => { void pollAcceptance().catch(console.error); }, 5_000);
  app.on("will-quit", () => clearInterval(acceptanceTimer));
  void controller.ready.then(pollAcceptance).catch(console.error);
  void controller.ready.then(async () => {
    const result = await readFile(join(workDir, "result"), "utf8").catch(() => "");
    if (!result.trim() || result.trim() === "installed") return;
    controller.reportStartupError(result.trim() === "restored"
      ? "新版 App 未在 60 秒內就緒或在啟動時結束，已還原舊版。可重試更新。"
      : "App 更新未完成，請查看更新紀錄與保留的舊版，確認後再重試。");
    await rm(join(workDir, "result"), { force: true });
  }).catch(console.error);
}
