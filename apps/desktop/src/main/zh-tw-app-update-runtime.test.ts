// @vitest-environment node
import { EventEmitter } from "node:events";
import { createHash } from "node:crypto";
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const ctx = vi.hoisted(() => ({
  root: "", current: "", version: "0.8.1-zh-tw.1", packaged: true,
  windows: [] as { isDestroyed: () => boolean; webContents: { isDestroyed: () => boolean; send: ReturnType<typeof vi.fn> } }[],
  events: new Map<string, ((...args: any[]) => unknown)[]>(),
  handlers: new Map<string, (...args: any[]) => unknown>(),
  listeners: new Map<string, (...args: any[]) => unknown>(),
  exec: vi.fn(), spawn: vi.fn(), quit: vi.fn(), preserve: vi.fn(),
  stagedId: "com.multica.zh-tw", stagedVersion: "0.8.1-zh-tw.2", signingError: false,
}));
vi.mock("electron", () => ({
  app: {
    get isPackaged() { return ctx.packaged; },
    getVersion: () => ctx.version,
    getPath: (key: string) => key === "exe" ? join(ctx.current, "Contents/MacOS/Multica") : ctx.root,
    quit: ctx.quit,
    on: (name: string, handler: (...args: any[]) => unknown) => ctx.events.set(name, [...(ctx.events.get(name) ?? []), handler]),
  },
  BrowserWindow: { getAllWindows: () => ctx.windows },
  ipcMain: {
    handle: (name: string, handler: (...args: any[]) => unknown) => ctx.handlers.set(name, handler),
    on: (name: string, handler: (...args: any[]) => unknown) => ctx.listeners.set(name, handler),
  },
}));
vi.mock("node:child_process", () => ({ execFile: ctx.exec, spawn: ctx.spawn }));

import { setupZhTWAppUpdate } from "./zh-tw-app-update";

describe("Taiwan updater Electron integration", () => {
  const originalPlatform = process.platform;
  const originalArch = process.arch;
  const zip = "fixture archive";
  const asset = { url: "https://taiwan.example/downloads/v2/app.zip", size: Buffer.byteLength(zip), sha256: createHash("sha256").update(zip).digest("hex") };
  const invoke = async (channel: string, ...args: unknown[]) => ctx.handlers.get(channel)!({}, ...args);
  const emit = (event: string, ...args: unknown[]) => ctx.events.get(event)?.forEach((handler) => handler(...args));
  const status = () => invoke("updater:get-status");
  const start = async () => {
    setupZhTWAppUpdate({ getMainWindow: () => ctx.windows[0] as never, getApiBaseUrl: () => "https://taiwan.example/api", preserveDaemonOnQuit: ctx.preserve });
    await status();
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    ctx.root = mkdtempSync(join(tmpdir(), "zh-tw-runtime-"));
    ctx.current = join(ctx.root, "Applications/Multica 繁中版.app");
    mkdirSync(ctx.current, { recursive: true });
    ctx.packaged = true; ctx.signingError = false;
    ctx.stagedId = "com.multica.zh-tw"; ctx.stagedVersion = "0.8.1-zh-tw.2";
    ctx.events.clear(); ctx.handlers.clear(); ctx.listeners.clear();
    ctx.windows = [{ isDestroyed: () => false, webContents: { isDestroyed: () => false, send: vi.fn() } }];
    Object.defineProperty(process, "platform", { value: "darwin" });
    Object.defineProperty(process, "arch", { value: "arm64" });
    vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(url.endsWith("latest.json")
      ? JSON.stringify({ version: "0.8.1-zh-tw.2", desktop: { "darwin-arm64": asset }, notes_url: "https://taiwan.example/notes/v2" }) : zip)));
    ctx.exec.mockImplementation((file: string, args: string[], _options: unknown, done: (...args: unknown[]) => void) => {
      if (file.endsWith("ditto")) {
        const bundle = join(args[3]!, "Multica.app/Contents/MacOS");
        mkdirSync(bundle, { recursive: true });
        writeFileSync(join(bundle, "Multica"), "fixture executable", { mode: 0o700 });
      }
      if (file.endsWith("codesign") && ctx.signingError) { done(new Error("bad signature"), "", "bad signature"); return; }
      let value = "";
      if (file.endsWith("PlistBuddy")) {
        if (args[1]?.includes("CFBundleIdentifier")) value = args[2]?.startsWith(ctx.current) ? "com.multica.zh-tw" : ctx.stagedId;
        if (args[1]?.includes("CFBundleShortVersionString")) value = ctx.stagedVersion;
        if (args[1]?.includes("CFBundleExecutable")) value = "Multica";
      }
      done(null, value, "");
    });
    ctx.spawn.mockImplementation(() => {
      const child = new EventEmitter() as EventEmitter & { unref: () => void };
      child.unref = vi.fn();
      Promise.resolve().then(() => child.emit("spawn"));
      return child;
    });
  });
  afterEach(() => {
    emit("will-quit");
    Object.defineProperty(process, "platform", { value: originalPlatform });
    Object.defineProperty(process, "arch", { value: originalArch });
    vi.unstubAllGlobals(); vi.useRealTimers();
    rmSync(ctx.root, { recursive: true, force: true });
  });

  it("uses the Taiwan feed for manual checks, persists preferences, and publishes download state", async () => {
    await start();
    await invoke("updater:set-automatic-updates", false);
    expect(JSON.parse(readFileSync(join(ctx.root, "updater-preferences.json"), "utf8"))).toEqual({ automaticUpdates: false });
    expect(await invoke("updater:check")).toMatchObject({ ok: true, available: true });
    expect(fetch).toHaveBeenNthCalledWith(1, "https://taiwan.example/downloads/latest.json", expect.objectContaining({ redirect: "error" }));
    expect(fetch).toHaveBeenNthCalledWith(2, asset.url, expect.objectContaining({ redirect: "error" }));
    expect(await status()).toMatchObject({ phase: "ready", version: "0.8.1-zh-tw.2", notesUrl: "https://taiwan.example/notes/v2" });
    expect(ctx.windows[0]!.webContents.send).toHaveBeenCalledWith("updater:status", expect.objectContaining({ phase: "downloading" }));
    expect(ctx.spawn).not.toHaveBeenCalled();
  });

  it("only prepares an automatic install on an explicit quit, preserving the daemon before the final quit", async () => {
    await start();
    await invoke("updater:check");
    expect(ctx.quit).not.toHaveBeenCalled();
    const event = { preventDefault: vi.fn() };
    emit("before-quit", event);
    expect(event.preventDefault).toHaveBeenCalledOnce();
    expect(ctx.preserve).not.toHaveBeenCalled();
    // Await the same install's async filesystem work through the publication.
    await vi.waitFor(() => expect(ctx.quit).toHaveBeenCalledOnce());
    expect(ctx.preserve).toHaveBeenCalledOnce();
    expect(ctx.preserve.mock.invocationCallOrder[0]).toBeLessThan(ctx.quit.mock.invocationCallOrder[0]!);
    const pending = JSON.parse(readFileSync(join(ctx.root, "zh-tw-update/pending-launch.json"), "utf8"));
    expect(pending).toMatchObject({ version: "0.8.1-zh-tw.2", bundle: ctx.current });
    expect(ctx.spawn).toHaveBeenCalledWith("/bin/bash", expect.arrayContaining([pending.token]), { detached: true, stdio: "ignore" });
  });

  it("automatically installs after the last window closes, while Later suppresses this safe point", async () => {
    await start();
    await invoke("updater:check");
    await invoke("updater:defer");
    expect(await status()).toMatchObject({ phase: "ready", deferred: true });
    ctx.windows = [];
    emit("window-all-closed");
    expect(ctx.spawn).not.toHaveBeenCalled();
    // Explicit install remains available after deferral.
    await invoke("updater:install");
    expect(ctx.spawn).toHaveBeenCalledOnce();
  });

  it("installs automatically if all windows are already closed when verification finishes", async () => {
    await start();
    ctx.windows = [];
    await invoke("updater:check");
    await vi.waitFor(() => expect(ctx.quit).toHaveBeenCalledOnce());
  });

  it("recovers from a cancelled quit without leaving the UI or daemon preservation stuck", async () => {
    const releasePreservation = vi.fn();
    ctx.preserve.mockReturnValueOnce(releasePreservation);
    await start();
    await invoke("updater:check");
    writeFileSync(join(ctx.root, "zh-tw-update/result"), "quit-timeout");
    await invoke("updater:install");
    expect(() => readFileSync(join(ctx.root, "zh-tw-update/result"))).toThrow();
    const helper = ctx.spawn.mock.results[0]!.value as EventEmitter;
    helper.emit("exit", 1);
    expect(await status()).toMatchObject({ phase: "error", error: expect.stringContaining("App 尚未結束") });
    expect(releasePreservation).toHaveBeenCalledOnce();
    await invoke("updater:check");
    await invoke("updater:install");
    expect(ctx.spawn).toHaveBeenCalledTimes(2);
    expect(ctx.quit).toHaveBeenCalledTimes(2);
  });

  it.each(["bundle", "version", "codesign"])("rejects a mismatched %s before installation", async (failure) => {
    if (failure === "bundle") ctx.stagedId = "com.official.multica";
    if (failure === "version") ctx.stagedVersion = "0.8.1-zh-tw.3";
    if (failure === "codesign") ctx.signingError = true;
    await start();
    expect(await invoke("updater:check")).toMatchObject({ ok: false });
    expect(await status()).toMatchObject({ phase: "error" });
    emit("before-quit", { preventDefault: vi.fn() });
    expect(ctx.spawn).not.toHaveBeenCalled();
    expect(ctx.preserve).not.toHaveBeenCalled();
  });

  it("registers settings handlers on unsupported platforms without checking any feed", async () => {
    Object.defineProperty(process, "platform", { value: "linux" });
    await start();
    expect(await status()).toMatchObject({ phase: "unsupported" });
    expect(await invoke("updater:check")).toMatchObject({ ok: false });
    expect(await invoke("updater:get-preferences")).toEqual({ automaticUpdates: true });
    expect(fetch).not.toHaveBeenCalled();
  });
});
