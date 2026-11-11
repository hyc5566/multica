import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { UpdaterStatus } from "../../../shared/updater-types";

const translations = { desktop: { updates: {
  title: "Updates", progress: "Downloading {{percent}}%", ready: "Ready {{version}}",
  installing: "Installing", defer_explanation: "Deferred for this session",
  status_failed: "Status failed", release_notes: "Release notes", later: "Later",
  restart_now: "Restart now", retry: "Retry",
} } };
vi.mock("@multica/views/i18n", () => ({ useT: () => ({
  t: (selector: (value: typeof translations) => string, values?: Record<string, string>) =>
    Object.entries(values ?? {}).reduce((text, [key, value]) => text.replace(`{{${key}}}`, String(value)), selector(translations)),
}) }));
import { UpdateNotification } from "./update-notification";

const initial: UpdaterStatus = { phase: "idle", currentVersion: "1.0" };
let listener: (status: UpdaterStatus) => void;
let getStatus: ReturnType<typeof vi.fn>;
let deferUpdate: ReturnType<typeof vi.fn>;
let installUpdate: ReturnType<typeof vi.fn>;
let checkForUpdates: ReturnType<typeof vi.fn>;
let rendererReady: ReturnType<typeof vi.fn>;
let openExternal: ReturnType<typeof vi.fn>;

beforeEach(() => {
  getStatus = vi.fn().mockResolvedValue(initial);
  deferUpdate = vi.fn().mockResolvedValue(undefined);
  installUpdate = vi.fn().mockResolvedValue(undefined);
  checkForUpdates = vi.fn().mockResolvedValue({ ok: true, available: false });
  rendererReady = vi.fn();
  openExternal = vi.fn();
  Object.defineProperty(window, "desktopAPI", { configurable: true, value: { openExternal } });
  Object.defineProperty(window, "updater", { configurable: true, value: {
    getStatus, deferUpdate, installUpdate, checkForUpdates, rendererReady,
    onStatusChange: (callback: typeof listener) => { listener = callback; return vi.fn(); },
  } });
});

describe("UpdateNotification", () => {
  it("shows pending acceptance without presenting it as complete", async () => {
    render(<UpdateNotification />);
    await act(async () => listener({ ...initial, phase: "pending", message: "待原 runtime 任務驗收" }));
    expect(screen.getByText("待原 runtime 任務驗收")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Restart now" })).not.toBeInTheDocument();
  });
  it("shows progress and ready actions, using the manifest notes URL", async () => {
    render(<UpdateNotification />);
    expect(rendererReady).toHaveBeenCalledOnce();
    await act(async () => listener({ ...initial, phase: "downloading", percent: 42 }));
    expect(screen.getByText("Downloading 42%")).toBeInTheDocument();
    await act(async () => listener({ ...initial, phase: "ready", version: "1.1", notesUrl: "https://example.com/notes" }));
    fireEvent.click(screen.getByRole("button", { name: "Release notes" }));
    expect(openExternal).toHaveBeenCalledWith("https://example.com/notes");
    fireEvent.click(screen.getByRole("button", { name: "Later" }));
    await waitFor(() => expect(deferUpdate).toHaveBeenCalledOnce());
    await act(async () => listener({ ...initial, phase: "ready", deferred: true }));
    expect(screen.queryByRole("button", { name: "Later" })).not.toBeInTheDocument();
  });

  it("does not overwrite a newer event with a stale snapshot and catches install failure", async () => {
    let resolve!: (status: UpdaterStatus) => void;
    getStatus.mockReturnValue(new Promise((done) => { resolve = done; }));
    installUpdate.mockRejectedValue(new Error("install failed"));
    render(<UpdateNotification />);
    await act(async () => listener({ ...initial, phase: "ready", version: "1.1" }));
    await act(async () => resolve(initial));
    fireEvent.click(screen.getByRole("button", { name: "Restart now" }));
    expect(await screen.findByRole("alert")).toHaveTextContent("install failed");
  });

  it("shows a failed check and retries", async () => {
    getStatus.mockRejectedValueOnce(new Error("offline")).mockResolvedValue(initial);
    render(<UpdateNotification />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Status failed");
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(checkForUpdates).toHaveBeenCalledOnce());
    await waitFor(() => expect(screen.queryByRole("alert")).not.toBeInTheDocument());
    expect(getStatus).toHaveBeenCalledTimes(2);
  });

  it("clears an action error after a background retry starts", async () => {
    checkForUpdates.mockResolvedValue({ ok: false, error: "offline" });
    render(<UpdateNotification />);
    await act(async () => listener({ ...initial, phase: "error", error: "offline" }));
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() => expect(checkForUpdates).toHaveBeenCalledOnce());
    await act(async () => listener({ ...initial, phase: "downloading", percent: 10 }));
    expect(screen.queryByText("offline")).not.toBeInTheDocument();
    expect(screen.getByText("Downloading 10%")).toBeInTheDocument();
  });

});
