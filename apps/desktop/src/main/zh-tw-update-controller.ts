import type { ManualUpdateCheckResult, UpdaterPreferences, UpdaterStatus } from "../shared/updater-types";
import type { AppUpdate, ReleaseManifest } from "./zh-tw-app-update";

export const STARTUP_CHECK_DELAY_MS = 30_000;
export const CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000;
export const RETRY_DELAY_MS = 60_000;
const MAX_RETRY_DELAY_MS = 15 * 60_000;

export interface ZhTWUpdateRuntime {
  currentVersion: string;
  unsupportedReason: string | null;
  loadPreferences: () => Promise<UpdaterPreferences>;
  savePreferences: (preferences: UpdaterPreferences) => Promise<void>;
  fetchManifest: (signal: AbortSignal) => Promise<ReleaseManifest>;
  findUpdate: (manifest: ReleaseManifest) => AppUpdate | null;
  stage: (update: AppUpdate, progress: (percent: number) => void, signal: AbortSignal) => Promise<string>;
  install: (staged: string, update: AppUpdate, shouldContinue: () => boolean) => Promise<void>;
  publish: (status: UpdaterStatus) => void;
}

/** One flow for the Taiwan channel's timers, settings and manual actions. */
export function createZhTWUpdateController(runtime: ZhTWUpdateRuntime) {
  let preferences: UpdaterPreferences = { automaticUpdates: true };
  let status: UpdaterStatus = {
    phase: runtime.unsupportedReason ? "unsupported" : "idle",
    currentVersion: runtime.currentVersion,
    ...(runtime.unsupportedReason ? { error: runtime.unsupportedReason } : {}),
  };
  let staged: { path: string; update: AppUpdate } | null = null;
  let flight: Promise<ManualUpdateCheckResult> | null = null;
  let abort: AbortController | null = null;
  let manualFlight = false;
  let startupTimer: ReturnType<typeof setTimeout> | null = null;
  let periodicTimer: ReturnType<typeof setInterval> | null = null;
  let retryTimer: ReturnType<typeof setTimeout> | null = null;
  let retryDelay = RETRY_DELAY_MS;
  let disposed = false;
  let preferenceWrites = Promise.resolve();
  let pendingPreferenceWrites = 0;

  const publish = (next: UpdaterStatus) => {
    status = next;
    runtime.publish({ ...status });
  };
  const cancelTimers = () => {
    if (startupTimer) clearTimeout(startupTimer);
    if (periodicTimer) clearInterval(periodicTimer);
    if (retryTimer) clearTimeout(retryTimer);
    startupTimer = periodicTimer = retryTimer = null;
  };
  const runAutomaticCheck = () => { void check(false); };
  const schedule = () => {
    if (disposed || runtime.unsupportedReason || !preferences.automaticUpdates) return;
    if (!startupTimer) startupTimer = setTimeout(() => {
      startupTimer = null;
      runAutomaticCheck();
    }, STARTUP_CHECK_DELAY_MS);
    if (!periodicTimer) periodicTimer = setInterval(runAutomaticCheck, CHECK_INTERVAL_MS);
  };
  const ready = runtime.loadPreferences().then((loaded) => {
    preferences = loaded;
    schedule();
  });
  const automaticInstallAllowed = () => pendingPreferenceWrites === 0 && preferences.automaticUpdates && !status.deferred;
  const canAutoInstall = () => automaticInstallAllowed() && status.phase === "ready";

  async function check(manual = true): Promise<ManualUpdateCheckResult> {
    await ready;
    if (status.phase === "pending") return { ok: false, error: status.message ?? "更新尚待驗收。" };
    if (runtime.unsupportedReason) return { ok: false, error: runtime.unsupportedReason };
    if (disposed || (!manual && !preferences.automaticUpdates)) {
      return { ok: false, error: "自動更新已暫停。" };
    }
    if (flight) return flight;
    if (staged) {
      if (status.phase === "error") publish({ ...status, phase: "ready", error: undefined });
      return { ok: true, currentVersion: runtime.currentVersion, latestVersion: staged.update.version, available: true };
    }
    if (retryTimer) clearTimeout(retryTimer);
    retryTimer = null;
    abort = new AbortController();
    const signal = abort.signal;
    manualFlight = manual;
    publish({ phase: "checking", currentVersion: runtime.currentVersion });
    flight = Promise.resolve().then(async (): Promise<ManualUpdateCheckResult> => {
      try {
        const manifest = await runtime.fetchManifest(signal);
        signal.throwIfAborted();
        const update = runtime.findUpdate(manifest);
        if (!update) {
          retryDelay = RETRY_DELAY_MS;
          publish({ phase: "idle", currentVersion: runtime.currentVersion });
          return { ok: true, currentVersion: runtime.currentVersion, latestVersion: runtime.currentVersion, available: false };
        }
        publish({ phase: "downloading", currentVersion: runtime.currentVersion, version: update.version, notesUrl: update.notesUrl, percent: 0 });
        const path = await runtime.stage(update, (percent) => {
          if (!signal.aborted) publish({ ...status, percent });
        }, signal);
        signal.throwIfAborted();
        staged = { path, update };
        retryDelay = RETRY_DELAY_MS;
        publish({ phase: "ready", currentVersion: runtime.currentVersion, version: update.version, notesUrl: update.notesUrl, percent: 100, deferred: false });
        return { ok: true, currentVersion: runtime.currentVersion, latestVersion: update.version, available: true };
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        if (signal.aborted) {
          publish({ phase: "idle", currentVersion: runtime.currentVersion });
        } else {
          publish({ ...status, phase: "error", error: message });
          if (preferences.automaticUpdates && !disposed) {
            retryTimer = setTimeout(() => {
              retryTimer = null;
              runAutomaticCheck();
            }, retryDelay);
            retryDelay = Math.min(retryDelay * 2, MAX_RETRY_DELAY_MS);
          }
        }
        return { ok: false, error: message };
      } finally {
        flight = null;
        abort = null;
      }
    });
    return flight;
  }

  return {
    ready,
    check,
    getStatus: (): UpdaterStatus => ({ ...status }),
    getPreferences: async (): Promise<UpdaterPreferences> => { await ready; return { ...preferences }; },
    setAutomaticUpdates: async (enabled: unknown): Promise<UpdaterPreferences> => {
      if (typeof enabled !== "boolean") throw new TypeError("automaticUpdates must be a boolean");
      pendingPreferenceWrites += 1;
      const save = preferenceWrites.then(async () => {
        await ready;
        await runtime.savePreferences({ automaticUpdates: enabled });
        preferences = { automaticUpdates: enabled };
        cancelTimers();
        if (!enabled && !manualFlight) abort?.abort();
        if (enabled) schedule();
      });
      preferenceWrites = save.catch(() => {});
      try { await save; } finally { pendingPreferenceWrites -= 1; }
      return { automaticUpdates: enabled };
    },
    canAutoInstall,
    defer: () => { if (status.phase === "ready") publish({ ...status, deferred: true }); },
    install: async (automatic = false): Promise<void> => {
      if (automatic && !canAutoInstall()) return;
      if (!staged || status.phase === "installing") return;
      const previous = status;
      publish({ ...status, phase: "installing" });
      try {
        await runtime.install(staged.path, staged.update, () => !automatic || automaticInstallAllowed());
      } catch (error) {
        // Keep the verified stage so a failed helper spawn can be retried.
        publish({ ...previous, phase: "error", error: error instanceof Error ? error.message : String(error) });
        throw error;
      }
    },
    reportStartupError: (error: string) => publish({ phase: "error", currentVersion: runtime.currentVersion, error }),
    reportPending: (version: string) => publish({ phase: "pending", currentVersion: runtime.currentVersion, version, message: "目前版本已啟動，待原 runtime 完成並回報任務後才完成驗收。" }),
    reportAccepted: () => publish({ phase: "idle", currentVersion: runtime.currentVersion }),
    reportInstallError: (error: string) => publish({ ...status, phase: "error", error }),
    dispose: () => { disposed = true; cancelTimers(); abort?.abort(); },
  };
}
