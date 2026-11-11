import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";

const mocks = vi.hoisted(() => ({
  getPreferences: vi.fn(),
  setAutomaticUpdates: vi.fn(),
  checkForUpdates: vi.fn(),
  toastSuccess: vi.fn(),
  toastError: vi.fn(),
}));

const translations = {
  auto_save: { toast_saved: "Settings saved" },
  desktop: {
    updates: {
      title: "Updates",
      description: "Update preferences",
      current_version: "Current version",
      automatic_updates_title: "Automatic background updates",
      automatic_updates_description: "Download updates in the background",
      automatic_updates_save_failed: "Failed to save update settings",
      check_section_title: "Check for updates",
      check_section_description: "Check manually",
      up_to_date: "Up to date",
      downloading: "Downloading v{{version}}",
      check_now: "Check now",
      checking: "Checking",
      available: "Available {{version}}", progress: "Downloading {{percent}}%", ready: "Ready {{version}}", installing: "Installing", unsupported: "Unsupported", status_failed: "Status failed", restart_now: "Restart now",
    },
  },
};

vi.mock("@multica/views/i18n", () => ({
  useT: () => ({
    t: (
      selector: (resources: typeof translations) => string,
      values?: Record<string, string>,
    ) => {
      const template = selector(translations);
      return Object.entries(values ?? {}).reduce(
        (result, [key, value]) => result.replace(`{{${key}}}`, value),
        template,
      );
    },
  }),
}));

vi.mock("sonner", () => ({
  toast: {
    success: mocks.toastSuccess,
    error: mocks.toastError,
  },
}));

import { UpdatesSettingsTab } from "./updates-settings-tab";
import type { UpdaterStatus } from "../../../shared/updater-types";
let listener: (status: UpdaterStatus) => void;
const initial: UpdaterStatus = { phase: "idle", currentVersion: "1.2.3" };

describe("UpdatesSettingsTab", () => {
  beforeEach(() => {
    mocks.getPreferences.mockReset().mockResolvedValue({
      automaticUpdates: true,
    });
    mocks.setAutomaticUpdates.mockReset();
    mocks.checkForUpdates.mockReset();
    mocks.toastSuccess.mockReset();
    mocks.toastError.mockReset();

    Object.defineProperty(window, "desktopAPI", {
      configurable: true,
      value: { appInfo: { version: "1.2.3" } },
    });
    Object.defineProperty(window, "updater", {
      configurable: true,
      value: {
        getPreferences: mocks.getPreferences,
        setAutomaticUpdates: mocks.setAutomaticUpdates,
        checkForUpdates: mocks.checkForUpdates,
        getStatus: vi.fn().mockResolvedValue({ phase: "idle", currentVersion: "1.2.3" }),
        onStatusChange: (callback: typeof listener) => { listener = callback; return vi.fn(); },
        installUpdate: vi.fn(),
      },
    });
  });

  it("loads the persisted preference and saves changes from the switch", async () => {
    mocks.getPreferences.mockResolvedValue({ automaticUpdates: false });
    mocks.setAutomaticUpdates.mockResolvedValue({ automaticUpdates: true });
    render(<UpdatesSettingsTab />);

    const toggle = screen.getByRole("switch", {
      name: "Automatic background updates",
    });
    // The switch renders as <span role="switch">, so jest-dom's toBeEnabled()
    // treats it as always enabled and does not actually wait for getPreferences
    // to resolve. Wait on the persisted value being reflected instead, which
    // deterministically holds until the loaded preference (false) is applied.
    await waitFor(() => expect(toggle).not.toBeChecked());

    fireEvent.click(toggle);

    await waitFor(() => {
      expect(mocks.setAutomaticUpdates).toHaveBeenCalledWith(true);
      expect(toggle).toBeChecked();
    });
    expect(mocks.toastSuccess).toHaveBeenCalledWith("Settings saved", {
      id: "settings-auto-save",
    });
  });

  it("shows a rejected manual check", async () => {
    mocks.checkForUpdates.mockRejectedValue(new Error("offline"));
    render(<UpdatesSettingsTab />);
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    expect(await screen.findByText("offline")).toBeInTheDocument();
  });

  it("labels a pending manual check as downloading when the status changes", async () => {
    let finish!: (result: { ok: true; currentVersion: string; latestVersion: string; available: boolean }) => void;
    mocks.checkForUpdates.mockReturnValue(new Promise((resolve) => { finish = resolve; }));
    render(<UpdatesSettingsTab />);
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    await act(async () => listener({ ...initial, phase: "checking" }));
    expect(screen.getByRole("button", { name: "Checking" })).toBeDisabled();
    await act(async () => listener({ ...initial, phase: "downloading", percent: 42 }));
    expect(screen.getByRole("button", { name: "Downloading 42%" })).toBeDisabled();
    await act(async () => finish({ ok: true, currentVersion: "1.2.3", latestVersion: "1.2.4", available: true }));
  });

  it("does not duplicate a shared error and clears an old manual error on background recovery", async () => {
    mocks.checkForUpdates.mockRejectedValue(new Error("offline"));
    render(<UpdatesSettingsTab />);
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    expect(await screen.findByText("offline")).toBeInTheDocument();
    await act(async () => listener({ ...initial, phase: "error", error: "offline" }));
    expect(screen.getAllByText("offline")).toHaveLength(1);
    await act(async () => listener({ ...initial, phase: "downloading", percent: 1 }));
    expect(screen.queryByText("offline")).not.toBeInTheDocument();
  });

});
