import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createEnDict } from "../../i18n/en";
import { AllPlatforms } from "./all-platforms";

vi.mock("next/navigation", () => ({ useRouter: () => ({ push: vi.fn() }) }));
vi.mock("../../i18n", () => ({ useLocale: () => ({ t: { download: createEnDict().download } }) }));

const ASSETS = {
  macArm64Dmg: "https://downloads.test/mac-arm64.dmg",
  macArm64Zip: "https://downloads.test/mac-arm64.zip",
  macX64Dmg: "https://downloads.test/mac-x64.dmg",
  macX64Zip: "https://downloads.test/mac-x64.zip",
};

function row(arch: string) {
  return screen.getByText(arch).closest("li")!;
}

describe("AllPlatforms", () => {
  it("only shows published Apple Silicon formats even when other platforms are present", () => {
    render(<AllPlatforms assets={{ ...ASSETS, winX64Exe: "https://downloads.test/windows.exe", linuxAmd64Deb: "https://downloads.test/linux.deb" }} />);
    expect(within(row("Apple Silicon")).getByRole("link", { name: ".dmg" })).toHaveAttribute("href", ASSETS.macArm64Dmg);
    expect(within(row("Apple Silicon")).getByRole("link", { name: ".zip" })).toHaveAttribute("href", ASSETS.macArm64Zip);
    expect(screen.queryByText("Intel")).toBeNull();
    expect(screen.queryByText("Windows")).toBeNull();
    expect(screen.queryByText("Linux")).toBeNull();
  });
  it("omits formats that are not published", () => {
    render(<AllPlatforms assets={{ macArm64Zip: ASSETS.macArm64Zip }} />);
    expect(within(row("Apple Silicon")).queryByText(".dmg")).toBeNull();
  });
});
