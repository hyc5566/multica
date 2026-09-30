import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { CliSection } from "./cli-section";

vi.mock("../../i18n", () => ({
  useLocale: () => ({
    t: {
      download: {
        allPlatforms: { unavailable: "Unavailable" },
        cli: {
          title: "Prefer the CLI?",
          sub: "For servers and headless setups.",
          installLabel: "Install",
          platformGroup: "Choose your platform",
          platformMacosLinux: "macOS / Linux",
          platformWindows: "Windows",
          startLabel: "Start daemon",
          sshNote: "Already on a server?",
          copyLabel: "Copy",
          copiedLabel: "Copied",
        },
      },
    },
  }),
}));

describe("CliSection", () => {
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
});
