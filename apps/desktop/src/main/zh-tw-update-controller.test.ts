// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { CHECK_INTERVAL_MS, createZhTWUpdateController, RETRY_DELAY_MS, STARTUP_CHECK_DELAY_MS, type ZhTWUpdateRuntime } from "./zh-tw-update-controller";

const update = { version: "0.8.1-zh-tw.2", url: "https://s90.example/0.8.1-zh-tw.2/app.zip", size: 3, sha256: "a".repeat(64) };
const manifest = { version: update.version, desktop: { "darwin-arm64": update } };

describe("Taiwan updater controller", () => {
  let runtime: ZhTWUpdateRuntime;
  let controller: ReturnType<typeof createZhTWUpdateController>;
  beforeEach(() => {
    vi.useFakeTimers();
    runtime = {
      currentVersion: "0.8.1-zh-tw.1", unsupportedReason: null,
      loadPreferences: vi.fn().mockResolvedValue({ automaticUpdates: true }),
      savePreferences: vi.fn().mockResolvedValue(undefined),
      fetchManifest: vi.fn().mockResolvedValue(manifest),
      findUpdate: vi.fn().mockReturnValue(update),
      stage: vi.fn().mockImplementation(async (_update, progress) => { progress(50); return "/staged/Multica.app"; }),
      install: vi.fn().mockResolvedValue(undefined), publish: vi.fn(),
    };
  });
  afterEach(() => { controller?.dispose(); vi.useRealTimers(); });
  const start = async () => { controller = createZhTWUpdateController(runtime); await controller.ready; };

  it("automatically checks and downloads after 30 seconds without restarting a live App", async () => {
    await start();
    await vi.advanceTimersByTimeAsync(STARTUP_CHECK_DELAY_MS - 1);
    expect(runtime.fetchManifest).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(runtime.stage).toHaveBeenCalledOnce();
    expect(runtime.install).not.toHaveBeenCalled();
    expect(vi.mocked(runtime.publish).mock.calls.map(([status]) => [status.phase, status.percent])).toEqual([
      ["checking", undefined], ["downloading", 0], ["downloading", 50], ["ready", 100],
    ]);
    expect(controller.canAutoInstall()).toBe(true);
  });

  it("keeps the 24-hour schedule when there is no newer release", async () => {
    vi.mocked(runtime.findUpdate).mockReturnValue(null);
    await start();
    await vi.advanceTimersByTimeAsync(STARTUP_CHECK_DELAY_MS);
    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL_MS - STARTUP_CHECK_DELAY_MS - 1);
    expect(runtime.fetchManifest).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(runtime.fetchManifest).toHaveBeenCalledTimes(2);
    expect(runtime.stage).not.toHaveBeenCalled();
  });

  it("honors a persisted disabled preference but permits a manual download", async () => {
    vi.mocked(runtime.loadPreferences).mockResolvedValue({ automaticUpdates: false });
    await start();
    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL_MS * 2);
    expect(runtime.fetchManifest).not.toHaveBeenCalled();
    expect(await controller.check()).toMatchObject({ ok: true, available: true, latestVersion: update.version });
    expect(runtime.stage).toHaveBeenCalledOnce();
    expect(controller.canAutoInstall()).toBe(false);
    await controller.install(true);
    expect(runtime.install).not.toHaveBeenCalled();
    await controller.install();
    expect(runtime.install).toHaveBeenCalledOnce();
  });

  it("coalesces background and manual downloads", async () => {
    let release!: (value: string) => void;
    vi.mocked(runtime.stage).mockImplementation(() => new Promise((resolve) => { release = resolve; }));
    await start();
    const first = controller.check();
    await vi.advanceTimersByTimeAsync(STARTUP_CHECK_DELAY_MS);
    const second = controller.check();
    release("/stage");
    expect(await second).toEqual(await first);
    expect(runtime.fetchManifest).toHaveBeenCalledOnce();
    expect(runtime.stage).toHaveBeenCalledOnce();
  });

  it("surfaces offline failures and retries without waiting 24 hours", async () => {
    vi.mocked(runtime.fetchManifest).mockRejectedValueOnce(new Error("offline"));
    await start();
    await vi.advanceTimersByTimeAsync(STARTUP_CHECK_DELAY_MS);
    expect(controller.getStatus()).toMatchObject({ phase: "error", error: "offline" });
    await vi.advanceTimersByTimeAsync(RETRY_DELAY_MS - 1);
    expect(runtime.fetchManifest).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1);
    expect(controller.getStatus().phase).toBe("ready");
    expect(runtime.fetchManifest).toHaveBeenCalledTimes(2);
  });

  it("backs off failed retries and cancels them when disabled", async () => {
    vi.mocked(runtime.fetchManifest).mockRejectedValue(new Error("offline"));
    await start();
    await controller.check();
    await vi.advanceTimersByTimeAsync(RETRY_DELAY_MS);
    expect(runtime.fetchManifest).toHaveBeenCalledTimes(2);
    await vi.advanceTimersByTimeAsync(RETRY_DELAY_MS);
    expect(runtime.fetchManifest).toHaveBeenCalledTimes(2);
    await controller.setAutomaticUpdates(false);
    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL_MS * 2);
    expect(runtime.fetchManifest).toHaveBeenCalledTimes(2);
  });

  it("aborts an automatic download when disabled and never installs it", async () => {
    vi.mocked(runtime.stage).mockImplementation((_update, _progress, signal) => new Promise((_resolve, reject) => {
      signal.addEventListener("abort", () => reject(signal.reason), { once: true });
    }));
    await start();
    await vi.advanceTimersByTimeAsync(STARTUP_CHECK_DELAY_MS);
    expect(controller.getStatus().phase).toBe("downloading");
    await controller.setAutomaticUpdates(false);
    await vi.advanceTimersByTimeAsync(0);
    expect(controller.getStatus().phase).toBe("idle");
    await controller.install(true);
    expect(runtime.install).not.toHaveBeenCalled();
  });

  it("leaves preferences unchanged on save failure and serializes writes", async () => {
    await start();
    vi.mocked(runtime.savePreferences).mockRejectedValueOnce(new Error("disk full"));
    await expect(controller.setAutomaticUpdates(false)).rejects.toThrow("disk full");
    expect(await controller.getPreferences()).toEqual({ automaticUpdates: true });
    await Promise.all([controller.setAutomaticUpdates(false), controller.setAutomaticUpdates(true)]);
    expect(await controller.getPreferences()).toEqual({ automaticUpdates: true });
    await expect(controller.setAutomaticUpdates("false")).rejects.toThrow(TypeError);
  });

  it("blocks automatic installation while a preference change is being saved", async () => {
    await start();
    await controller.check();
    let saved!: () => void;
    vi.mocked(runtime.savePreferences).mockImplementationOnce(() => new Promise<void>((resolve) => { saved = resolve; }));
    const saving = controller.setAutomaticUpdates(false);
    expect(controller.canAutoInstall()).toBe(false);
    await controller.install(true);
    await vi.advanceTimersByTimeAsync(0);
    saved();
    await saving;
    expect(runtime.install).not.toHaveBeenCalled();
  });

  it("defers automatic installation for the session while allowing an explicit restart", async () => {
    await start();
    await controller.check();
    controller.defer();
    expect(controller.getStatus()).toMatchObject({ phase: "ready", deferred: true });
    await controller.install(true);
    expect(runtime.install).not.toHaveBeenCalled();
    await controller.install();
    expect(runtime.install).toHaveBeenCalledOnce();
  });

  it("revokes an automatic install's permission if preferences change during helper preparation", async () => {
    await start();
    await controller.check();
    let finish!: () => void;
    vi.mocked(runtime.install).mockImplementationOnce(async (_stage, _update, shouldContinue) => {
      await new Promise<void>((resolve) => { finish = resolve; });
      if (!shouldContinue()) throw new Error("installation paused");
    });
    const installing = controller.install(true);
    await controller.setAutomaticUpdates(false);
    finish();
    await expect(installing).rejects.toThrow("installation paused");
    expect(controller.getStatus().phase).toBe("error");
  });

  it("retains a verified stage after helper failure and allows retry", async () => {
    await start();
    await controller.check();
    vi.mocked(runtime.install).mockRejectedValueOnce(new Error("spawn failed"));
    await expect(controller.install()).rejects.toThrow("spawn failed");
    expect(controller.getStatus()).toMatchObject({ phase: "error", error: "spawn failed" });
    await controller.check();
    expect(controller.getStatus().phase).toBe("ready");
    await controller.install();
    expect(runtime.stage).toHaveBeenCalledOnce();
    expect(runtime.install).toHaveBeenCalledTimes(2);
  });

  it("never downloads or schedules on unsupported builds", async () => {
    runtime.unsupportedReason = "Apple Silicon only";
    await start();
    await vi.advanceTimersByTimeAsync(CHECK_INTERVAL_MS * 2);
    expect(await controller.check()).toEqual({ ok: false, error: "Apple Silicon only" });
    expect(controller.getStatus().phase).toBe("unsupported");
    expect(runtime.fetchManifest).not.toHaveBeenCalled();
  });
});
