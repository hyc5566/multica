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
  it("always uses the fixed installer and exposes the guide alongside release assets", () => {
    render(<CliSection assets={{ linuxX64: "https://downloads.test/linux-amd64.tar.gz" }} />);
    expect(screen.getByText("curl -fsSL https://10.1.24.90:45671/downloads/selfhost/install.sh -o install.sh && bash install.sh" )).toBeInTheDocument();
    expect(screen.getByText("multica login")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "guideline.md" })).toHaveAttribute("href", "https://10.1.24.90:45671/downloads/selfhost/guideline.md");
    expect(screen.getByRole("link", { name: "Linux · x64 · CLI / daemon" })).toHaveAttribute("href", "https://downloads.test/linux-amd64.tar.gz");
  });
});
