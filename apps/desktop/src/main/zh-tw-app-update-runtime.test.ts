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
  coordinated: vi.fn(),
  stagedId: "ai.multica.desktop.zh-tw.local", stagedVersion: "0.8.1-zh-tw.2", signingError: false,
  binaryContent: "fixture cli", binaryCommit: "b".repeat(40), binaryGate: "v1",
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
vi.mock("./zh-tw-cli-entry", () => ({ verifyCLIEntries: vi.fn() }));

import { setupZhTWAppUpdate, watchCoordinatedStartup } from "./zh-tw-app-update";

describe("Taiwan updater Electron integration", () => {
  const originalPlatform = process.platform;
  const originalArch = process.arch;
  const zip = "fixture archive";
  const binary = "fixture cli";
  const commit = "b".repeat(40);
  const asset = { url: "https://taiwan.example/downloads/v2/app.zip", size: Buffer.byteLength(zip), sha256: createHash("sha256").update(zip).digest("hex"), binary_sha256: createHash("sha256").update(binary).digest("hex"), binary_version: "0.8.1-zh-tw.2", binary_commit: commit, data_compatibility: "multica-profile-v1", bundle_id: "ai.multica.desktop.zh-tw.local" };
  const invoke = async (channel: string, ...args: unknown[]) => ctx.handlers.get(channel)!({}, ...args);
  const emit = (event: string, ...args: unknown[]) => ctx.events.get(event)?.forEach((handler) => handler(...args));
  const status = () => invoke("updater:get-status");
  const start = async () => {
    setupZhTWAppUpdate({ getMainWindow: () => ctx.windows[0] as never, getApiBaseUrl: () => "https://taiwan.example/api", preserveDaemonOnQuit: ctx.preserve, withCoordinatedDaemon: ctx.coordinated as never });
    await status();
  };

  beforeEach(() => {
    vi.useFakeTimers();
    vi.clearAllMocks();
    ctx.coordinated.mockRejectedValue(new Error("fixture has no daemon"));
    ctx.root = mkdtempSync(join(tmpdir(), "zh-tw-runtime-"));
    ctx.current = join(ctx.root, "Applications/Multica 繁中版.app");
    mkdirSync(ctx.current, { recursive: true });
    ctx.packaged = true; ctx.signingError = false;
    ctx.stagedId = "ai.multica.desktop.zh-tw.local"; ctx.stagedVersion = "0.8.1-zh-tw.2";
    ctx.binaryContent = binary; ctx.binaryCommit = commit; ctx.binaryGate = "v1";
    ctx.events.clear(); ctx.handlers.clear(); ctx.listeners.clear();
    ctx.windows = [{ isDestroyed: () => false, webContents: { isDestroyed: () => false, send: vi.fn() } }];
    Object.defineProperty(process, "platform", { value: "darwin" });
    Object.defineProperty(process, "arch", { value: "arm64" });
    vi.stubGlobal("fetch", vi.fn(async (url: string) => new Response(url.endsWith("latest.json")
      ? JSON.stringify({ version: "0.8.1-zh-tw.2", source_id: "hyc5566/multica", data_compatibility: "multica-profile-v1", coordinated_update_protocol: 1, commit, desktop: {}, coordinated_desktop: { "darwin-arm64": asset }, notes_url: "https://taiwan.example/notes/v2" }) : zip)));
    ctx.exec.mockImplementation((file: string, args: string[], _options: unknown, done: (...args: unknown[]) => void) => {
      if (file.endsWith("ditto")) {
        const bundle = join(args[3]!, "Multica.app/Contents/MacOS");
        mkdirSync(bundle, { recursive: true });
        writeFileSync(join(bundle, "Multica"), "fixture executable", { mode: 0o700 });
        const cliDir = join(args[3]!, "Multica.app/Contents/Resources/app.asar.unpacked/resources/bin");
        mkdirSync(cliDir, { recursive: true });
        writeFileSync(join(cliDir, "multica"), ctx.binaryContent, { mode: 0o700 });
      }
      if (file.endsWith("codesign") && ctx.signingError) { done(new Error("bad signature"), "", "bad signature"); return; }
      let value = "";
      if (file.endsWith("/bin/multica")) value = JSON.stringify({ version: asset.binary_version, build_commit: ctx.binaryCommit, daemon_maintenance_gate: "v1", daemon_maintenance_shutdown: ctx.binaryGate, data_compatibility: "multica-profile-v1" });
      if (file.endsWith("PlistBuddy")) {
        if (args[1]?.includes("CFBundleIdentifier")) value = args[2]?.startsWith(ctx.current) ? "ai.multica.desktop.zh-tw.local" : ctx.stagedId;
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

  it("claims a ready update quit before downstream daemon auto-stop runs", async () => {
    ctx.coordinated.mockReturnValue(new Promise(() => {}));
    await start();
    await invoke("updater:check");
    const stopDaemon = vi.fn();
    ctx.events.set("before-quit", [
      ...(ctx.events.get("before-quit") ?? []),
      (event: { defaultPrevented: boolean }) => { if (!event.defaultPrevented) stopDaemon(); },
    ]);
    const event = { defaultPrevented: false, preventDefault: () => { event.defaultPrevented = true; } };
    emit("before-quit", event);
    expect(event.defaultPrevented).toBe(true);
    expect(stopDaemon).not.toHaveBeenCalled();
    expect(ctx.quit).not.toHaveBeenCalled();
  });

  it("refuses an uncoordinated install without stopping the daemon or changing the bundle", async () => {
    await start();
    await invoke("updater:check");
    expect(ctx.quit).not.toHaveBeenCalled();
    await expect(invoke("updater:install")).rejects.toThrow(/fixture has no daemon/);
    expect(await status()).toMatchObject({ phase: "error", error: expect.stringContaining("daemon") });
    expect(ctx.preserve).not.toHaveBeenCalled();
    expect(ctx.spawn).not.toHaveBeenCalled();
    expect(ctx.quit).not.toHaveBeenCalled();
  });

  it("hands a verified journal to the staged coordinator before quitting", async () => {
    vi.useRealTimers();
    const work = join(ctx.root, "zh-tw-update");
    const cliPath = join(ctx.current, "Contents/Resources/app.asar.unpacked/resources/bin/multica");
    mkdirSync(join(ctx.current, "Contents/Resources/app.asar.unpacked/resources/bin"), { recursive: true });
    mkdirSync(work, { recursive: true });
    writeFileSync(cliPath, "old cli", { mode: 0o700 });
    const oldHash = createHash("sha256").update("old cli").digest("hex");
    const oldCommit = "a".repeat(40);
    writeFileSync(join(work, "ledger.json"), JSON.stringify({ data_compatibility: "multica-profile-v1", cli_entry: cliPath, current: { version: ctx.version, commit: oldCommit, binary_sha256: oldHash }, previous: {} }));
    ctx.coordinated.mockImplementation(async (work: (snapshot: unknown) => Promise<unknown>) => work({ profile: "desktop-test", daemonId: "daemon", serverURLSHA256: "f".repeat(64), pid: 123, startedAt: "start", port: 4321, gatePath: join(ctx.root, "gate"), binarySHA256: oldHash, version: ctx.version, commit: oldCommit, workspaces: { w: ["r"] }, runtimeIDs: ["r"] }));
    ctx.spawn.mockImplementation((_file: string, args: string[]) => {
      const journalPath = args[args.indexOf("--journal") + 1]!;
      const journal = JSON.parse(readFileSync(journalPath, "utf8"));
      const child = new EventEmitter() as EventEmitter & { pid: number; unref: () => void };
      child.pid = 4321; child.unref = vi.fn();
      writeFileSync(journal.handoff_file, JSON.stringify({ token: journal.token, helper_pid: child.pid }));
      queueMicrotask(() => child.emit("spawn"));
      return child;
    });
    ctx.quit.mockImplementation(() => emit("will-quit"));
    await start();
    await invoke("updater:check");
    await invoke("updater:install");
    const journal = JSON.parse(readFileSync(join(work, "journal.json"), "utf8"));
    expect(journal).toMatchObject({ phase: "prepared", daemon_id: "daemon", server_url_sha256: "f".repeat(64), old_binary_sha256: oldHash });
    expect(ctx.spawn).toHaveBeenCalledWith(expect.stringContaining("staging/"), expect.arrayContaining(["daemon", "coordinated-update", "--journal", join(work, "journal.json")]), expect.objectContaining({ detached: true }));
    expect(ctx.preserve).toHaveBeenCalledOnce();
    expect(ctx.quit).toHaveBeenCalledOnce();
  });

  it("safely aborts a helper that cannot start before handoff", async () => {
    vi.useRealTimers();
    const work = join(ctx.root, "zh-tw-update");
    const cliPath = join(ctx.current, "Contents/Resources/app.asar.unpacked/resources/bin/multica");
    mkdirSync(join(ctx.current, "Contents/Resources/app.asar.unpacked/resources/bin"), { recursive: true });
    mkdirSync(work, { recursive: true });
    writeFileSync(cliPath, "old cli", { mode: 0o700 });
    const oldHash = createHash("sha256").update("old cli").digest("hex");
    const oldCommit = "a".repeat(40);
    writeFileSync(join(work, "ledger.json"), JSON.stringify({ data_compatibility: "multica-profile-v1", cli_entry: cliPath, current: { version: ctx.version, commit: oldCommit, binary_sha256: oldHash }, previous: {} }));
    ctx.coordinated.mockImplementation(async (work: (snapshot: unknown) => Promise<unknown>) => work({ profile: "desktop-test", daemonId: "daemon", serverURLSHA256: "f".repeat(64), pid: 123, startedAt: "start", port: 4321, gatePath: join(ctx.root, "gate"), binarySHA256: oldHash, version: ctx.version, commit: oldCommit, workspaces: { w: ["r"] }, runtimeIDs: ["r"] }));
    ctx.spawn.mockImplementation((_file: string, args: string[]) => {
      const child = new EventEmitter() as EventEmitter & { unref: () => void };
      child.unref = vi.fn();
      if (!args.includes("--abort-before-handoff-app-pid")) queueMicrotask(() => child.emit("error", new Error("fixture helper failed")));
      else queueMicrotask(() => {
        const path = join(work, "journal.json");
        const journal = JSON.parse(readFileSync(path, "utf8"));
        writeFileSync(path, JSON.stringify({ ...journal, phase: "no_change" }));
        child.emit("exit", 0);
      });
      return child;
    });
    await start();
    await invoke("updater:check");
    await expect(invoke("updater:install")).rejects.toThrow(/fixture helper failed/);
    expect(ctx.spawn).toHaveBeenCalledWith(cliPath, expect.arrayContaining(["--abort-before-handoff-app-pid", String(process.pid)]), expect.anything());
    expect(ctx.quit).not.toHaveBeenCalled();
    expect(JSON.parse(readFileSync(join(work, "journal.json"), "utf8")).phase).toBe("no_change");
  });

  it("automatically installs after the last window closes, while Later suppresses this safe point", async () => {
    await start();
    await invoke("updater:check");
    await invoke("updater:defer");
    expect(await status()).toMatchObject({ phase: "ready", deferred: true });
    ctx.windows = [];
    emit("window-all-closed");
    expect(ctx.spawn).not.toHaveBeenCalled();
    await expect(invoke("updater:install")).rejects.toThrow(/fixture has no daemon/);
    expect(ctx.spawn).not.toHaveBeenCalled();
  });

  it.each(["bundle", "version", "codesign", "binary hash", "binary commit", "shutdown capability"])("rejects a mismatched %s before installation", async (failure) => {
    if (failure === "bundle") ctx.stagedId = "com.official.multica";
    if (failure === "version") ctx.stagedVersion = "0.8.1-zh-tw.3";
    if (failure === "codesign") ctx.signingError = true;
    if (failure === "binary hash") ctx.binaryContent = "different cli";
    if (failure === "binary commit") ctx.binaryCommit = "c".repeat(40);
    if (failure === "shutdown capability") ctx.binaryGate = "";
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

  it.each([true, false])("resumes a legacy journal with the retained coordinator present: %s", async (retained) => {
    vi.useRealTimers();
    const work = join(ctx.root, "zh-tw-update");
    mkdirSync(work, { recursive: true });
    const bundled = join(ctx.current, "Contents/Resources/app.asar.unpacked/resources/bin/multica");
    mkdirSync(join(ctx.current, "Contents/Resources/app.asar.unpacked/resources/bin"), { recursive: true });
    writeFileSync(bundled, "fixture");
    if (retained) writeFileSync(join(work, "legacy-coordinator"), "fixture");
    const token = "11111111-1111-4111-8111-111111111111";
    writeFileSync(join(work, "journal.json"), JSON.stringify({ token, phase: "awaiting_acceptance", old_bundle: ctx.current, old_version: "0.8.1-zh-tw.0", new_version: ctx.version, new_binary_sha256: createHash("sha256").update("fixture").digest("hex"), legacy: {} }));
    const enable = vi.fn();
    watchCoordinatedStartup(enable);
    expect(enable).toHaveBeenCalledOnce();
    const binary = retained ? join(work, "legacy-coordinator") : bundled;
    await vi.waitFor(() => expect(ctx.spawn).toHaveBeenCalledWith(binary, ["daemon", "legacy-apply", "--journal", join(work, "journal.json"), "--token", token], expect.objectContaining({ detached: true })));
  });

  it.each(["missing", "mismatched"])("does not execute a legacy fallback with a %s hash", async (kind) => {
    vi.useRealTimers();
    const work = join(ctx.root, "zh-tw-update");
    const binary = join(ctx.current, "Contents/Resources/app.asar.unpacked/resources/bin/multica");
    mkdirSync(work, { recursive: true });
    mkdirSync(join(ctx.current, "Contents/Resources/app.asar.unpacked/resources/bin"), { recursive: true });
    writeFileSync(binary, "unapproved");
    writeFileSync(join(work, "journal.json"), JSON.stringify({ token: "11111111-1111-4111-8111-111111111111", phase: "finalizing", old_bundle: ctx.current, old_version: "0.8.1-zh-tw.0", new_version: ctx.version, new_binary_sha256: kind === "missing" ? undefined : "a".repeat(64), legacy: {} }));
    const error = vi.spyOn(console, "error").mockImplementation(() => {});
    watchCoordinatedStartup(() => {});
    await vi.waitFor(() => expect(error).toHaveBeenCalled());
    expect(ctx.spawn).not.toHaveBeenCalled();
    error.mockRestore();
  });

  it("keeps the restored daemon enabled while rollback cleanup retries", async () => {
    vi.useRealTimers();
    const work = join(ctx.root, "zh-tw-update");
    mkdirSync(work, { recursive: true });
    const token = "11111111-1111-4111-8111-111111111111";
    writeFileSync(join(work, "journal.json"), JSON.stringify({ token, phase: "rollback_finalizing", old_bundle: ctx.current, old_version: ctx.version, new_version: "0.8.1-zh-tw.2" }));
    const enable = vi.fn();
    watchCoordinatedStartup(enable);
    expect(enable).toHaveBeenCalledOnce();
    await vi.waitFor(() => expect(ctx.spawn).toHaveBeenCalledWith(expect.stringContaining("resources/bin/multica"), expect.arrayContaining(["coordinated-update", "--token", token]), expect.objectContaining({ detached: true })));
  });
});
