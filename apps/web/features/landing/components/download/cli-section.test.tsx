import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { CliSection } from "./cli-section";

vi.mock("../../i18n", () => ({
  useLocale: () => ({
    t: {
      download: {
        platforms: { linuxX64: "Linux · x64", macArm64: "macOS · Apple Silicon" },
        allPlatforms: { unavailable: "Unavailable" },
        cli: {
          title: "Prefer the CLI?",
          sub: "For servers and headless setups.",
          installLabel: "Install",
          platformGroup: "Choose your platform",
          platformMacosLinux: "macOS / Linux",
          platformWindows: "Windows",
          startLabel: "Start daemon",
          copyLabel: "Copy",
          copiedLabel: "Copied",
        },
      },
    },
  }),
}));

describe("CliSection", () => {
  it("offers only the published CLI platform even without an installer script", () => {
    render(<CliSection assets={{ linuxX64: "https://downloads.test/linux-amd64.tar.gz" }} />);
    expect(screen.getByRole("link", { name: "Linux · x64 · CLI / daemon" })).toHaveAttribute("href", "https://downloads.test/linux-amd64.tar.gz");
    expect(screen.queryByRole("link", { name: /macOS/ })).toBeNull();
  });

  it("allows a configured repository installer with source metadata in its tag", () => {
    render(<CliSection installerUrl="https://github.com/another-owner/multica/releases/download/zh-tw-v0.9.0-zh-tw.1+up.013be77/install.sh" />);
    expect(screen.getByText(/curl -fsSL/)).toHaveTextContent("another-owner/multica");
  });
  it("shows the published installer without an unsupported Windows command", () => {
    render(<CliSection installerUrl="https://github.com/hyc5566/multica/releases/download/zh-tw-v0.4.43-zh-tw.7/install.sh" />);

    expect(screen.getByText(/bash install.sh --login/)).toBeInTheDocument();
    expect(screen.getByText("macOS / Linux")).toBeInTheDocument();
    expect(screen.queryByText("Windows")).not.toBeInTheDocument();
  });

  it("does not offer an installer when the release lacks one", () => {
    render(<CliSection />);
    expect(screen.getByRole("status")).toHaveTextContent("Unavailable");
  });

  it("rejects installer URLs that could alter the shell command", () => {
    render(<CliSection installerUrl="https://github.com/hyc5566/multica/releases/download/tag/install.sh' ; echo unsafe" />);
    expect(screen.getByRole("status")).toHaveTextContent("Unavailable");
    expect(screen.queryByText(/curl -fsSL/)).not.toBeInTheDocument();
  });

  it("does not copy a relative installer path as a shell command", () => {
    render(<CliSection installerUrl="/downloads/0.4.43-zh-tw.7/install.sh" />);
    expect(screen.getByRole("status")).toHaveTextContent("Unavailable");
  });
});
