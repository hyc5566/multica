// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { files, handlers, cliState } = vi.hoisted(() => ({
  cliState: { running: true, pid: 100, failStart: false },
  files: new Map<string, string>(),
  handlers: new Map<string, (...args: unknown[]) => Promise<unknown>>(),
}));

vi.mock("electron", () => ({
  app: { on: vi.fn(), getAppPath: () => "/test-app" },
  ipcMain: {
    handle: (name: string, handler: (...args: unknown[]) => Promise<unknown>) =>
      handlers.set(name, handler),
    on: vi.fn(),
  },
  BrowserWindow: {},
  shell: {},
}));
vi.mock("fs", () => ({ existsSync: (path: string) => path === "/test-app/resources/bin/multica", watchFile: vi.fn(), unwatchFile: vi.fn() }));
vi.mock("child_process", () => ({ execFile: (_bin: string, args: string[], _opts: unknown, callback: (error: Error | null, out: string) => void) => {
  if (args[0] === "version") return callback(null, JSON.stringify({version:"test"}));
  if (args[1] === "stop") cliState.running = false;
  if (args[1] === "start") {
    if (cliState.failStart) return callback(new Error("start failed"), "");
    cliState.running = true; cliState.pid++;
  }
  callback(null, "");
} }));
vi.mock("os", () => ({ homedir: () => "/test-home", hostname: () => "test-host" }));
vi.mock("fs/promises", () => ({
  readFile: vi.fn(async (path: string) => {
    if (!files.has(path)) throw new Error("ENOENT");
    return files.get(path);
  }),
  writeFile: vi.fn(async (path: string, body: string) => { files.set(path, body); }),
  mkdir: vi.fn(),
  rm: vi.fn(),
  open: vi.fn(),
  stat: vi.fn(),
}));
vi.mock("./cli-bootstrap", () => ({
  ensureManagedCli: vi.fn(),
  managedCliPath: vi.fn(),
}));

import { setupDaemonManager } from "./daemon-manager";
import { DaemonOperationGate } from "./daemon-recovery";
import { profileConfigPath, profileUserIdPath } from "./daemon-profile";

const server = "https://multica.example.test";
const profile = "desktop-multica.example.test";
const configPath = profileConfigPath(profile);
const fetchMock = vi.fn();

function invoke(name: string, ...args: unknown[]) {
  return handlers.get(name)!(null, ...args);
}
function cachedToken() {
  return JSON.parse(files.get(configPath)!).token;
}

describe("Desktop daemon token synchronization", () => {
  beforeEach(async () => {
    files.clear();
    handlers.clear();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    // Register IPC without bootstrapping any real CLI or starting polling.
    vi.spyOn(DaemonOperationGate.prototype, "runBackground").mockResolvedValue({
      success: false, error: "operation_busy",
    });
    setupDaemonManager(() => null);
    files.set(configPath, JSON.stringify({ token: "mul_revoked", server_url: server }));
    files.set(profileUserIdPath(profile), "user-1");
    await invoke("daemon:set-target-api-url", server);
  });
  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it.each(["success", "mint failure", "restart failure", "revoke failure"])("rotation ordering: %s", async (scenario) => {
    vi.useFakeTimers();
    cliState.running = true; cliState.pid = 100; cliState.failStart = scenario === "restart failure";
    const events: string[] = [];
    fetchMock.mockImplementation(async (url: string, options?: {method?: string}) => {
      if (url.endsWith("/api/tokens") && !options?.method) return {ok:true, json:async()=>[{id:"current",token_prefix:"mul_revo"}]};
      if (options?.method === "POST") { events.push("mint"); return {ok:scenario !== "mint failure", status:503, statusText:"unavailable", text:async()=>"", json:async()=>({token:"mul_replacement"})}; }
      if (options?.method === "DELETE") {
        events.push("revoke");
        expect(cliState.running).toBe(true);
        expect(cliState.pid).toBe(101);
        expect(cachedToken()).toBe("mul_replacement");
        return {ok:scenario !== "revoke failure", status:503};
      }
      return {ok:cliState.running, json:async()=>({status:"running", pid:cliState.pid, active_task_count:0, os:process.platform, server_url:server})};
    });
    await invoke("daemon:stop");
    cliState.running = true;
    const result = await invoke("daemon:rotate-desktop-token", "session-jwt", "user-1", "current");
    expect(result, JSON.stringify(result)).toMatchObject({ok:scenario === "success"});
    expect(events).toEqual(scenario === "mint failure" || scenario === "restart failure" ? ["mint"] : ["mint", "revoke"]);
    if (scenario === "mint failure") expect(cachedToken()).toBe("mul_revoked");
  });

  it("identifies the current token by prefix, not duplicate Desktop names", async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => [
      { id: "other", name: "Multica Desktop", token_prefix: "mul_other" },
      { id: "current", name: "renamed", token_prefix: "mul_revo" },
    ] });
    expect(await invoke("daemon:desktop-token-id", "session-jwt", "user-1")).toBe("current");
    expect(await invoke("daemon:desktop-token-id", "session-jwt", "another-user")).toBeNull();
  });

  it("rejects stale selection without minting or revoking", async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => [{ id: "current", token_prefix: "mul_revo" }] });
    const result = await invoke("daemon:rotate-desktop-token", "session-jwt", "user-1", "other");
    expect(result).toMatchObject({ ok: false });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(cachedToken()).toBe("mul_revoked");
  });

  it("fails closed when prefixes are ambiguous", async () => {
    fetchMock.mockResolvedValue({ ok: true, json: async () => [
      { id: "a", token_prefix: "mul_revo" }, { id: "b", token_prefix: "mul_revo" },
    ] });
    await expect(invoke("daemon:desktop-token-id", "session-jwt", "user-1")).rejects.toThrow("ambiguous");
  });

  it("refuses rotation while tasks are active", async () => {
    fetchMock.mockImplementation(async (url: string) => url.endsWith("/api/tokens")
      ? { ok: true, json: async () => [{ id: "current", token_prefix: "mul_revo" }] }
      : { ok: true, json: async () => ({ status: "running", pid: 100, active_task_count: 1, os: process.platform, server_url: server }) });
    await invoke("daemon:stop");
    expect(await invoke("daemon:rotate-desktop-token", "session-jwt", "user-1", "current")).toMatchObject({ ok: false, message: expect.stringContaining("running tasks") });
    expect(fetchMock.mock.calls.every(call => !call[1]?.method)).toBe(true);
    expect(cachedToken()).toBe("mul_revoked");
  });

  it("replaces a revoked PAT using the App session, then reuses the replacement", async () => {
    fetchMock
      .mockResolvedValueOnce({ status: 401 })
      .mockResolvedValueOnce({ ok: true, json: async () => ({ token: "mul_fresh" }) })
      .mockResolvedValueOnce({ status: 200 });

    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(cachedToken()).toBe("mul_fresh");
    expect(fetchMock).toHaveBeenNthCalledWith(2, `${server}/api/tokens`, expect.objectContaining({
      method: "POST", headers: expect.objectContaining({ Authorization: "Bearer session-jwt" }),
    }));
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(cachedToken()).toBe("mul_fresh");
  });

  it.each([200, 403, 503])("keeps the cached PAT for probe status %s", async (status) => {
    fetchMock.mockResolvedValue({ status });
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(cachedToken()).toBe("mul_revoked");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe(`${server}/api/me`);
  });

  it("does not mint or clear credentials on a network failure", async () => {
    fetchMock.mockRejectedValue(new Error("offline"));
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(cachedToken()).toBe("mul_revoked");
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it.each([401, 503])("preserves credentials and surfaces a failed mint (%s) without retries", async (status) => {
    fetchMock.mockResolvedValueOnce({ status: 401 }).mockResolvedValueOnce({
      ok: false, status, statusText: "error", text: async () => "",
    });
    await expect(invoke("daemon:sync-token", "session-jwt", "user-1")).rejects.toMatchObject({ status });
    expect(cachedToken()).toBe("mul_revoked");
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it("defaults to manual startup while preserving an existing auto-start preference", async () => {
    expect(await invoke("daemon:get-prefs")).toEqual({ autoStart: false, autoStop: false });
    files.set("/test-home/.multica/desktop_prefs.json", JSON.stringify({ autoStart: true }));
    expect(await invoke("daemon:get-prefs")).toEqual({ autoStart: true, autoStop: false });
  });
});
