// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { files, handlers, cliState, machine } = vi.hoisted(() => ({
  machine: { home: "/test-home", host: "test-host" },
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
vi.mock("os", () => ({ homedir: () => machine.home, hostname: () => machine.host }));
vi.mock("fs/promises", () => ({
  readFile: vi.fn(async (path: string) => {
    if (!files.has(path)) throw Object.assign(new Error("ENOENT"), { code: "ENOENT" });
    return files.get(path);
  }),
  writeFile: vi.fn(async (path: string, body: string, options?: { flag?: string }) => {
    if (options?.flag === "wx" && files.has(path)) throw Object.assign(new Error("EEXIST"), { code: "EEXIST" });
    files.set(path, body);
  }),
  link: vi.fn(async (source: string, target: string) => {
    if (files.has(target)) throw Object.assign(new Error("EEXIST"), { code: "EEXIST" });
    files.set(target, files.get(source)!);
  }),
  mkdir: vi.fn(),
  rm: vi.fn(async (path: string) => { files.delete(path); }),
  open: vi.fn(),
  stat: vi.fn(),
}));
vi.mock("./cli-bootstrap", () => ({
  ensureManagedCli: vi.fn(),
  managedCliPath: vi.fn(),
}));

import { desktopTokenRecord } from "./desktop-token";
import { setupDaemonManager } from "./daemon-manager";
import { DaemonOperationGate } from "./daemon-recovery";
import { profileConfigPath, profileUserIdPath } from "./daemon-profile";

const server = "https://multica.example.test";
const profile = "desktop-multica.example.test";
const configPath = () => profileConfigPath(profile);
const fetchMock = vi.fn();

function invoke(name: string, ...args: unknown[]) {
  return handlers.get(name)!(null, ...args);
}
const installation = "12345678-1234-1234-1234-123456789abc";
function seedOwnership(id = "current", token = "mul_revoked") {
  files.set(machine.home + "/.multica/desktop-installation.id", installation);
  files.set(machine.home + "/.multica/profiles/" + profile + "/.desktop-token.json", JSON.stringify(
    desktopTokenRecord({ id, token }, { machine: machine.host + ":" + installation, userId: "user-1", serverUrl: server }),
  ));
}
function cachedToken() {
  return JSON.parse(files.get(configPath())!).token;
}

describe("Desktop daemon token synchronization", () => {
  beforeEach(async () => {
    machine.home = "/test-home"; machine.host = "test-host";
    cliState.running = true; cliState.pid = 100; cliState.failStart = false;
    files.clear();
    handlers.clear();
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    // Register IPC without bootstrapping any real CLI or starting polling.
    vi.spyOn(DaemonOperationGate.prototype, "runBackground").mockResolvedValue({
      success: false, error: "operation_busy",
    });
    setupDaemonManager(() => null);
    files.set(configPath(), JSON.stringify({ token: "mul_revoked", server_url: server }));
    files.set(profileUserIdPath(profile), "user-1");
    seedOwnership();
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
      if (options?.method === "POST") { events.push("mint"); return {ok:scenario !== "mint failure", status:503, statusText:"unavailable", text:async()=>"", json:async()=>({id:"replacement",token:"mul_replacement"})}; }
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

  it("identifies the exact local token without resolving duplicate names or prefixes", async () => {
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
    expect(fetchMock).not.toHaveBeenCalled();
    expect(cachedToken()).toBe("mul_revoked");
  });

  it("does not claim a substituted credential with the same prefix", async () => {
    files.set(configPath(), JSON.stringify({ token: "mul_revoked_other", server_url: server }));
    expect(await invoke("daemon:desktop-token-id", "session-jwt", "user-1")).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
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
    let revoked = true;
    fetchMock.mockImplementation(async (url: string, options?: { method?: string }) => {
      if (options?.method === "POST") { revoked = false; return { ok: true, json: async () => ({ id: "fresh", token: "mul_fresh" }) }; }
      if (url.endsWith("/api/me")) return { status: revoked ? 401 : 200 };
      return { ok: false };
    });
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(cachedToken()).toBe("mul_fresh");
    expect(fetchMock).toHaveBeenCalledWith(
      server + "/api/tokens", expect.objectContaining({ method: "POST", headers: expect.objectContaining({ Authorization: "Bearer session-jwt" }) }),
    );
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(fetchMock.mock.calls.filter(call => call[1]?.method === "POST")).toHaveLength(1);
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

  it.each(["first login", "legacy profile"])("isolates two machine/HOME IPC sessions: %s", async (scenario) => {
    vi.useFakeTimers();
    const issued: string[] = [];
    const revoked: string[] = [];
    let exposeHealth = false;
    fetchMock.mockImplementation(async (url: string, options?: { method?: string; headers?: { Authorization?: string } }) => {
      if (options?.method === "POST") {
        expect(options.headers?.Authorization).toBe("Bearer mul_shared_input");
        const id = "mint-" + (issued.length + 1);
        issued.push(id);
        return { ok: true, json: async () => ({ id, token: "mul_" + id }) };
      }
      if (options?.method === "DELETE") { revoked.push(url.split("/").pop()!); return { ok: true }; }
      if (url.endsWith("/api/me")) return { status: 200 };
      return { ok: exposeHealth && cliState.running, json: async () => ({
        status: "running", pid: cliState.pid, active_task_count: 0, os: process.platform, server_url: server,
      }) };
    });
    files.delete(machine.home + "/.multica/profiles/" + profile + "/.desktop-token.json");
    if (scenario === "first login") {
      files.delete(configPath());
      files.delete(machine.home + "/.multica/desktop-installation.id");
    }
    else files.set(configPath(), JSON.stringify({ token: "mul_shared_input", server_url: server }));
    expect(await invoke("daemon:desktop-token-id", "mul_shared_input", "user-1")).toBeNull();
    await invoke("daemon:sync-token", "mul_shared_input", "user-1");
    expect(cachedToken()).toBe("mul_mint-1");
    const machineA = { ...machine };
    const machineAFiles = [...files.entries()];
    machine.home = "/other-home"; machine.host = "other-host";
    // Simulate the worst ordinary migration: copied profile AND installation
    // UUID. A different host still must mint its own credential.
    for (const [path, content] of machineAFiles) files.set(path.replace(machineA.home, machine.home), content);
    expect(await invoke("daemon:desktop-token-id", "mul_shared_input", "user-1")).toBeNull();
    await invoke("daemon:sync-token", "mul_shared_input", "user-1");
    expect(cachedToken()).toBe("mul_mint-2");
    await invoke("daemon:sync-token", "mul_shared_input", "user-1");
    expect(issued).toEqual(["mint-1", "mint-2"]);
    const machineBToken = files.get(configPath());
    Object.assign(machine, machineA);
    await invoke("daemon:sync-token", "mul_shared_input", "user-1");
    expect(cachedToken()).toBe("mul_mint-1");
    expect(issued).toHaveLength(2);
    expect(await invoke("daemon:rotate-desktop-token", "mul_shared_input", "user-1", "mint-2")).toMatchObject({ ok: false });
    expect(revoked).toEqual([]);
    exposeHealth = true;
    cliState.running = true;
    await invoke("daemon:stop");
    cliState.running = true;
    expect(await invoke("daemon:rotate-desktop-token", "mul_shared_input", "user-1", "mint-1")).toEqual({ ok: true });
    expect(issued).toEqual(["mint-1", "mint-2", "mint-3"]);
    expect(revoked).toEqual(["mint-1"]);
    expect(files.get("/other-home/.multica/profiles/" + profile + "/config.json")).toBe(machineBToken);
    expect(cachedToken()).toBe("mul_mint-3");
  });

  it("reloads a running daemon after signout cleared its cached credential", async () => {
    fetchMock.mockImplementation(async (_url: string, options?: { method?: string }) => {
      if (options?.method === "POST") return { ok: true, json: async () => ({ id: "fresh", token: "mul_fresh" }) };
      return { ok: cliState.running, json: async () => ({
        status: "running", pid: cliState.pid, active_task_count: 0, os: process.platform, server_url: server,
      }) };
    });
    await invoke("daemon:clear-token");
    expect(cachedToken()).toBeUndefined();
    await invoke("daemon:sync-token", "session-jwt", "user-1");
    expect(cachedToken()).toBe("mul_fresh");
    expect(cliState.running).toBe(true);
    expect(cliState.pid).toBe(101);
  });

  it("serializes concurrent login syncs so they share one local mint", async () => {
    files.delete(machine.home + "/.multica/profiles/" + profile + "/.desktop-token.json");
    let mintCount = 0;
    fetchMock.mockImplementation(async (url: string, options?: { method?: string }) => {
      if (options?.method === "POST") { mintCount++; return { ok: true, json: async () => ({ id: "fresh", token: "mul_fresh" }) }; }
      if (url.endsWith("/api/me")) return { status: 200 };
      return { ok: false };
    });
    await Promise.all([
      invoke("daemon:sync-token", "session-jwt", "user-1"),
      invoke("daemon:sync-token", "session-jwt", "user-1"),
    ]);
    expect(mintCount).toBe(1);
    expect(cachedToken()).toBe("mul_fresh");
    expect(await invoke("daemon:desktop-token-id", "session-jwt", "user-1")).toBe("fresh");
  });

  it.each([401, 503])("keeps the prior credential when reauthentication mint fails (%s)", async (status) => {
    fetchMock.mockResolvedValue({ ok: false, status });
    expect(await invoke("daemon:reauthenticate", "session-jwt", "user-1")).toMatchObject({
      ok: false, reason: status === 401 ? "session_invalid" : "transient",
    });
    expect(cachedToken()).toBe("mul_revoked");
    expect(await invoke("daemon:desktop-token-id", "session-jwt", "user-1")).toBe("current");
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[1]).toMatchObject({ method: "POST", signal: expect.any(AbortSignal) });
  });

  it.each([null, {}, { id: "x" }, { token: "mul_secret" }, { id: "", token: "mul_secret" },
    { id: 1, token: "mul_secret" }, { id: "x", token: 2 }, { id: "x", token: "jwt" }])(
    "rejects malformed mint responses without saving or revoking: %j", async (body) => {
      fetchMock.mockResolvedValueOnce({ status: 401 }).mockResolvedValueOnce({ ok: true, json: async () => body });
      await expect(invoke("daemon:sync-token", "session-jwt", "user-1")).rejects.toThrow("token identity");
      expect(cachedToken()).toBe("mul_revoked");
      expect(fetchMock.mock.calls.some(call => call[1]?.method === "DELETE")).toBe(false);
    },
  );

  it("defaults to manual startup while preserving an existing auto-start preference", async () => {
    expect(await invoke("daemon:get-prefs")).toEqual({ autoStart: false, autoStop: false });
    files.set("/test-home/.multica/desktop_prefs.json", JSON.stringify({ autoStart: true }));
    expect(await invoke("daemon:get-prefs")).toEqual({ autoStart: true, autoStop: false });
  });
});
