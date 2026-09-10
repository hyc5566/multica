// @vitest-environment node
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const { files, handlers } = vi.hoisted(() => ({
  files: new Map<string, string>(),
  handlers: new Map<string, (...args: unknown[]) => Promise<unknown>>(),
}));

vi.mock("electron", () => ({
  app: { on: vi.fn() },
  ipcMain: {
    handle: (name: string, handler: (...args: unknown[]) => Promise<unknown>) =>
      handlers.set(name, handler),
    on: vi.fn(),
  },
  BrowserWindow: {},
  shell: {},
}));
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
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
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
