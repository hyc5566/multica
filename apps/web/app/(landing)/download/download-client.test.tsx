import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { createEnDict } from "@/features/landing/i18n/en";
import { DownloadClient } from "./download-client";

vi.mock("next/navigation", () => ({ useRouter: () => ({ replace: vi.fn(), push: vi.fn() }) }));
vi.mock("@/features/landing/i18n", () => ({ useLocale: () => ({ t: createEnDict(), locale: "en" }) }));
vi.mock("@/features/landing/site/site-page", () => ({ SitePage: ({ children }: { children: React.ReactNode }) => <main>{children}</main> }));
vi.mock("@/features/landing/components/download/use-detected-platform", () => ({ useDetectedPlatform: (platform: unknown) => platform }));

describe("DownloadClient", () => {
  it("renders only this release's Mac ZIP and Linux CLI links", () => {
    render(<DownloadClient platform={{ os: "mac", arch: "arm64", archConfident: true }} started={null} release={{
      version: "zh-tw-v0.9.0-zh-tw.1", publishedAt: null, htmlUrl: null,
      releasesUrl: "https://github.com/another-owner/multica/releases",
      assets: { macArm64Zip: "https://downloads.test/mac-arm64.zip" }, sizes: {},
      cliAssets: { linuxX64: "https://downloads.test/linux-amd64.tar.gz" },
    }} />);
    const hrefs = screen.getAllByRole("link").map((link) => link.getAttribute("href"));
    expect(hrefs).toContain("https://downloads.test/mac-arm64.zip");
    expect(hrefs).toContain("https://downloads.test/linux-amd64.tar.gz");
    expect(hrefs).toContain("https://github.com/another-owner/multica/releases");
    expect(hrefs.some((href) => href?.includes("0.4.43") || href?.includes("windows"))).toBe(false);
    expect(screen.getByText(/curl -fsSL/)).toHaveTextContent("https://10.1.24.90:45671/downloads/selfhost/install.sh");
  });

  it("keeps failed release fetches unavailable without inventing mirror downloads", () => {
    const { container } = render(<DownloadClient platform={{ os: "mac", arch: "arm64", archConfident: true }} started="macArm64Zip" release={{
      version: null, publishedAt: null, htmlUrl: null, assets: {}, sizes: {},
    }} />);
    expect(screen.getByRole("link", { name: "guideline.md" })).toHaveAttribute("href", "https://10.1.24.90:45671/downloads/selfhost/guideline.md");
    expect(container.querySelector('a[href$=".zip"]')).toBeNull();
    expect(screen.getByText(/curl -fsSL/)).toHaveTextContent("https://10.1.24.90:45671/downloads/selfhost/install.sh");
  });
  it("rejects an unsupported started key even if its asset exists", () => {
    render(<DownloadClient platform={{ os: "windows", arch: "x64", archConfident: true }} started="winX64Exe" release={{
      version: null, publishedAt: null, htmlUrl: null,
      assets: { winX64Exe: "https://downloads.test/windows.exe", macArm64Dmg: "https://downloads.test/mac.dmg" }, sizes: {},
      installerUrl: "https://bad.test/install.sh'; touch /tmp/injected; '",
    }} />);
    expect(screen.queryByText(/windows.exe/)).toBeNull();
    expect(screen.getByText(/curl -fsSL/)).toHaveTextContent("curl -fsSL https://10.1.24.90:45671/downloads/selfhost/install.sh -o install.sh && bash install.sh");
    expect(screen.queryByText(/injected/)).toBeNull();
  });

  it("keeps the installation guide visible after an App download starts", () => {
    render(<DownloadClient platform={{ os: "mac", arch: "arm64", archConfident: true }} started="macArm64Dmg" release={{
      version: null, publishedAt: null, htmlUrl: null, assets: { macArm64Dmg: "https://downloads.test/mac.dmg" }, sizes: {},
    }} />);
    expect(screen.getByRole("link", { name: "guideline.md" })).toBeInTheDocument();
    expect(screen.getByText(/curl -fsSL/)).toBeInTheDocument();
  });

});
