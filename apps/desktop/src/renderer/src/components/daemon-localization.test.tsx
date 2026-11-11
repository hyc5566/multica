import { act, fireEvent, render, screen } from "@testing-library/react";
import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { I18nProvider } from "@multica/core/i18n/react";
import { RESOURCES } from "@multica/views/locales";
import type { DaemonStatus } from "../../../shared/daemon-types";
import { DaemonPanel } from "./daemon-panel";
import { startDaemonWithSession } from "../platform/daemon-reauth";
import { DaemonSettingsTab } from "./daemon-settings-tab";

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("../platform/daemon-reauth", () => ({
  reauthenticateDaemon: vi.fn(),
  startDaemonWithSession: vi.fn(),
}));

let emitLogLine: (line: string) => void = () => {};

function installDaemonAPI(status: DaemonStatus) {
  Object.defineProperty(window, "daemonAPI", {
    configurable: true,
    value: {
      getPrefs: vi.fn().mockResolvedValue({ autoStart: true, autoStop: false }),
      setPrefs: vi.fn().mockResolvedValue({ autoStart: true, autoStop: false }),
      isCliInstalled: vi.fn().mockResolvedValue(true),
      getStatus: vi.fn().mockResolvedValue(status),
      onStatusChange: vi.fn(() => () => {}),
      startLogStream: vi.fn(),
      stopLogStream: vi.fn(),
      onLogLine: vi.fn((handler: (line: string) => void) => {
        emitLogLine = handler;
        return () => {};
      }),
    },
  });
}

function renderInTaiwanChinese(element: ReactNode) {
  return render(
    <I18nProvider locale="zh-Hans" resources={RESOURCES}>
      {element}
    </I18nProvider>,
  );
}

beforeEach(() => {
  emitLogLine = () => {};
});

describe("Desktop daemon localization with real zh-Hans resources", () => {
  it("lets the locale own the externally managed sentence ending", async () => {
    installDaemonAPI({ state: "running", externallyManaged: true });

    renderInTaiwanChinese(<DaemonSettingsTab />);

    expect(
      await screen.findByText(
        "登入時啟動守護程序。應用開啟期間，它會同時監控自動啟動和手動啟動的守護程序。",
      ),
    ).toBeInTheDocument();
    const command = screen.getByText("multica daemon stop");
    expect(command.closest("p")).toHaveTextContent(/multica daemon stop。$/);
  });

  it("shows the pinned protocol warning in Traditional Chinese", async () => {
    installDaemonAPI({ state: "running", externallyManaged: true, pinnedDaemon: true, incompatibleDaemon: true });
    renderInTaiwanChinese(<DaemonSettingsTab />);
    expect(await screen.findByRole("alert")).toHaveTextContent("獨立守護程序與此桌面應用程式不相容，請更新應用程式或守護程序。");
    expect(screen.getByText("此守護程序由外部獨立管理。")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "啟動" })).toBeDisabled();
  });

  it("offers manual start in settings while stopped", async () => {
    installDaemonAPI({ state: "stopped" });
    renderInTaiwanChinese(<DaemonSettingsTab />);
    const button = await screen.findByRole("button", { name: "啟動" });
    expect(button).toBeEnabled();
    await act(async () => { fireEvent.click(button); });
    expect(startDaemonWithSession).toHaveBeenCalledOnce();
  });

  it("renders repeated log messages with straight double quotes", async () => {
    installDaemonAPI({ state: "running" });
    renderInTaiwanChinese(
      <DaemonPanel
        open
        onOpenChange={vi.fn()}
        status={{ state: "running" }}
        runtimeCount={0}
      />,
    );

    await act(async () => {
      emitLogLine("12:00:00.000 INF poll complete component=daemon");
      emitLogLine("12:00:01.000 INF poll complete component=daemon");
    });

    expect(
      await screen.findByText('另有 1 條"poll complete"——點選展開'),
    ).toBeInTheDocument();
  });
});
