import { beforeEach, describe, expect, it, vi } from "vitest";

const { mockGetState, logout } = vi.hoisted(() => ({
  mockGetState: vi.fn(),
  logout: vi.fn(),
}));

const { toastError } = vi.hoisted(() => ({ toastError: vi.fn() }));

vi.mock("@multica/core/auth", () => ({
  useAuthStore: { getState: mockGetState },
}));

vi.mock("sonner", () => ({
  toast: { error: toastError },
}));

import { reauthenticateDaemon, startDaemonWithSession } from "./daemon-reauth";
import type { DaemonTranslator } from "../components/daemon-i18n";

const translations = {
  desktop: {
    daemon: {
      reconnect_failed: "無法重新連線 daemon",
      start_failed: "啟動失敗",
      try_again_moment: "请稍后重试。",
      try_again: "请重试。",
    },
  },
};

const t = ((selector: (resources: typeof translations) => string) =>
  selector(translations)) as DaemonTranslator;

const daemonAPI = {
  reauthenticate: vi.fn(),
  setTargetApiUrl: vi.fn(),
  syncToken: vi.fn(),
  start: vi.fn(),
};

beforeEach(() => {
  vi.resetAllMocks();
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { runtimeConfig: { ok: true, config: { apiUrl: "https://server.example" } } } });
  daemonAPI.start.mockResolvedValue({ success: true });
  localStorage.clear();
  (window as unknown as { daemonAPI: typeof daemonAPI }).daemonAPI = daemonAPI;
  mockGetState.mockReturnValue({ user: { id: "user-1" }, logout });
});

describe("reauthenticateDaemon", () => {
  it("re-mints + restarts the daemon when signed in, without logging out", async () => {
    localStorage.setItem("multica_token", "jwt-abc");
    daemonAPI.reauthenticate.mockResolvedValue({ ok: true });

    await reauthenticateDaemon(t);

    expect(daemonAPI.reauthenticate).toHaveBeenCalledWith("jwt-abc", "user-1");
    expect(logout).not.toHaveBeenCalled();
    expect(toastError).not.toHaveBeenCalled();
  });

  it("logs out only when the session token itself is rejected (401)", async () => {
    localStorage.setItem("multica_token", "jwt-abc");
    daemonAPI.reauthenticate.mockResolvedValue({
      ok: false,
      reason: "session_invalid",
    });

    await reauthenticateDaemon(t);

    expect(logout).toHaveBeenCalledOnce();
    expect(toastError).not.toHaveBeenCalled();
  });

  // The reviewer's must-fix: a non-401 (transient) failure must NOT log the
  // user out — they stay signed in and can retry.
  it("does NOT log out on a transient failure; shows a retryable toast", async () => {
    localStorage.setItem("multica_token", "jwt-abc");
    daemonAPI.reauthenticate.mockResolvedValue({
      ok: false,
      reason: "transient",
      message: "mint PAT failed: 503 Service Unavailable",
    });

    await reauthenticateDaemon(t);

    expect(logout).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledWith("無法重新連線 daemon", {
      description: "mint PAT failed: 503 Service Unavailable",
    });
  });

  it("does NOT log out when the IPC call itself throws unexpectedly", async () => {
    localStorage.setItem("multica_token", "jwt-abc");
    daemonAPI.reauthenticate.mockRejectedValue(new Error("ipc boom"));

    await reauthenticateDaemon(t);

    expect(logout).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledWith("無法重新連線 daemon", {
      description: "ipc boom",
    });
  });

  it("routes to login when there is no session token", async () => {
    await reauthenticateDaemon(t);

    expect(logout).toHaveBeenCalledOnce();
    expect(daemonAPI.reauthenticate).not.toHaveBeenCalled();
  });

  it("routes to login when there is no signed-in user", async () => {
    localStorage.setItem("multica_token", "jwt-abc");
    mockGetState.mockReturnValue({ user: null, logout });

    await reauthenticateDaemon(t);

    expect(logout).toHaveBeenCalledOnce();
    expect(daemonAPI.reauthenticate).not.toHaveBeenCalled();
  });
});

describe("startDaemonWithSession", () => {
  it("synchronizes the App session before starting through the bundled CLI", async () => {
    localStorage.setItem("multica_token", "jwt-abc");
    await startDaemonWithSession(t);
    expect(daemonAPI.setTargetApiUrl).toHaveBeenCalledWith("https://server.example");
    expect(daemonAPI.syncToken).toHaveBeenCalledWith("jwt-abc", "user-1");
    expect(daemonAPI.setTargetApiUrl.mock.invocationCallOrder[0]).toBeLessThan(daemonAPI.syncToken.mock.invocationCallOrder[0]!);
    expect(daemonAPI.syncToken.mock.invocationCallOrder[0]).toBeLessThan(daemonAPI.start.mock.invocationCallOrder[0]!);
    expect(toastError).not.toHaveBeenCalled();
  });

  it("does not start or log out when credential synchronization fails", async () => {
    localStorage.setItem("multica_token", "jwt-abc");
    daemonAPI.syncToken.mockRejectedValue(new Error("Server unavailable"));
    await startDaemonWithSession(t);
    expect(daemonAPI.start).not.toHaveBeenCalled();
    expect(logout).not.toHaveBeenCalled();
    expect(toastError).toHaveBeenCalledWith("啟動失敗", { description: "Server unavailable" });
  });

  it("requests login when the App session is missing", async () => {
    await startDaemonWithSession(t);
    expect(logout).toHaveBeenCalledOnce();
    expect(daemonAPI.start).not.toHaveBeenCalled();
  });

  it("reports a failed daemon start", async () => {
    localStorage.setItem("multica_token", "jwt-abc");
    daemonAPI.start.mockResolvedValue({ success: false, error: "CLI unavailable" });
    await startDaemonWithSession(t);
    expect(toastError).toHaveBeenCalledWith("啟動失敗", { description: "CLI unavailable" });
  });
});
