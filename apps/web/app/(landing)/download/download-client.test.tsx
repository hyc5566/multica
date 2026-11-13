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
    expect(screen.queryByText(/curl -fsSL/)).toBeNull();
  });

  it("keeps failed release fetches unavailable without inventing mirror downloads", () => {
    const { container } = render(<DownloadClient platform={{ os: "mac", arch: "arm64", archConfident: true }} started="macArm64Zip" release={{
      version: null, publishedAt: null, htmlUrl: null, assets: {}, sizes: {},
    }} />);
    expect(container.querySelector('a[href*="/downloads/"]')).toBeNull();
    expect(container.querySelector('a[href$=".zip"]')).toBeNull();
    expect(screen.queryByText(/curl -fsSL/)).toBeNull();
  });
});
