// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchLatestRelease } from "./github-release";

/** The twelve desktop artifacts a finished release carries. */
function completeAssets(version: string) {
  return [
    `multica-desktop-${version}-mac-arm64.dmg`,
    `multica-desktop-${version}-mac-arm64.zip`,
    `multica-desktop-${version}-mac-x64.dmg`,
    `multica-desktop-${version}-mac-x64.zip`,
    `multica-desktop-${version}-windows-x64.exe`,
    `multica-desktop-${version}-windows-arm64.exe`,
    `multica-desktop-${version}-linux-x86_64.AppImage`,
    `multica-desktop-${version}-linux-amd64.deb`,
    `multica-desktop-${version}-linux-x86_64.rpm`,
    `multica-desktop-${version}-linux-arm64.AppImage`,
    `multica-desktop-${version}-linux-arm64.deb`,
    `multica-desktop-${version}-linux-aarch64.rpm`,
  ].map((name) => ({
    name,
    browser_download_url: `https://github.test/download/v${version}/${name}`,
  }));
}

/** What a release looks like before the Windows/Linux jobs upload. */
function macOnlyAssets(version: string) {
  return completeAssets(version).filter((a) => a.name.includes("-mac-"));
}

function releasePayload(overrides: {
  tag: string;
  assets?: { name: string; browser_download_url: string }[];
  prerelease?: boolean;
  draft?: boolean;
}) {
  return {
    tag_name: overrides.tag,
    published_at: "2026-08-17T10:00:00Z",
    html_url: `https://github.com/hyc5566/multica/releases/tag/${overrides.tag}`,
    prerelease: overrides.prerelease ?? false,
    draft: overrides.draft ?? false,
    assets: overrides.assets ?? [],
  };
}

function mockFetchWithReleases(releases: unknown[]) {
  const fetchMock = vi.fn().mockResolvedValue(
    new Response(JSON.stringify(releases), {
      status: 200,
      headers: { "Content-Type": "application/json" },
    }),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

afterEach(() => {
  vi.unstubAllGlobals();
  vi.restoreAllMocks();
});

describe("fetchLatestRelease", () => {
  it("uses the published Taiwan installer and Apple Silicon asset", async () => {
    const installerUrl = "https://github.com/hyc5566/multica/releases/download/zh-tw-v0.4.43-zh-tw.7/install.sh";
    const assets = [
      { name: "install.sh", browser_download_url: installerUrl },
      { name: "multica-desktop-0.4.43-zh-tw.7-mac-arm64.zip", browser_download_url: `${installerUrl}/mac.zip` },
    ];
    const fetchMock = mockFetchWithReleases([
      releasePayload({ tag: "v0.6.0", assets: completeAssets("0.6.0") }),
      releasePayload({ tag: "zh-tw-v0.4.43-zh-tw.7", assets }),
    ]);

    const result = await fetchLatestRelease();
    expect(fetchMock).toHaveBeenCalledWith(expect.stringContaining("hyc5566/multica"), expect.anything());
    expect(result.version).toBe("zh-tw-v0.4.43-zh-tw.7");
    expect(result.installerUrl).toBe(installerUrl);
    expect(result.assets.macArm64Zip).toBe(`${installerUrl}/mac.zip`);
  });

  it("rejects an installer URL from a different release tag", async () => {
    const otherInstallerUrl =
      "https://github.com/hyc5566/multica/releases/download/zh-tw-v0.4.42-zh-tw.6/install.sh";
    mockFetchWithReleases([
      releasePayload({
        tag: "zh-tw-v0.4.43-zh-tw.7",
        assets: [
          { name: "install.sh", browser_download_url: otherInstallerUrl },
          ...macOnlyAssets("0.4.43-zh-tw.7"),
        ],
      }),
    ]);

    const result = await fetchLatestRelease();
    expect(result.installerUrl).toBeUndefined();
  });

  it("uses the latest release when its desktop assets are complete", async () => {
    mockFetchWithReleases([
      releasePayload({ tag: "zh-tw-v0.2.14", assets: completeAssets("0.2.14") }),
      releasePayload({ tag: "zh-tw-v0.2.13", assets: completeAssets("0.2.13") }),
    ]);

    const result = await fetchLatestRelease();
    expect(result.version).toBe("zh-tw-v0.2.14");
    expect(result.assets.winX64Exe).toContain("0.2.14");
  });

  it("keeps the latest Taiwan release when only its Mac App is published", async () => {
    mockFetchWithReleases([
      releasePayload({ tag: "zh-tw-v0.4.28", assets: macOnlyAssets("0.4.28") }),
      releasePayload({ tag: "zh-tw-v0.4.27", assets: completeAssets("0.4.27") }),
    ]);

    const result = await fetchLatestRelease();
    expect(result.version).toBe("zh-tw-v0.4.28");
    expect(result.assets.macArm64Zip).toContain("0.4.28");
    expect(result.assets.winX64Exe).toBeUndefined();
  });

  it("steps back when the latest release has no Mac App", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    mockFetchWithReleases([
      {
        ...releasePayload({
          tag: "zh-tw-v0.4.28",
          assets: [],
        }),
        published_at: "2020-01-01T00:00:00Z",
      },
      releasePayload({ tag: "zh-tw-v0.4.27", assets: completeAssets("0.4.27") }),
    ]);

    const result = await fetchLatestRelease();
    expect(result.version).toBe("zh-tw-v0.4.27");
  });

  it("searches past several incomplete releases", async () => {
    vi.spyOn(console, "warn").mockImplementation(() => {});
    mockFetchWithReleases([
      releasePayload({ tag: "zh-tw-v0.5.2", assets: [] }),
      releasePayload({ tag: "zh-tw-v0.5.1", assets: [] }),
      releasePayload({ tag: "zh-tw-v0.5.0", assets: [] }),
      releasePayload({ tag: "zh-tw-v0.4.9", assets: completeAssets("0.4.9") }),
    ]);

    const result = await fetchLatestRelease();
    expect(result.version).toBe("zh-tw-v0.4.9");
  });

  it("falls back to the latest release when no candidate has a Mac App", async () => {
    mockFetchWithReleases([
      releasePayload({ tag: "zh-tw-v0.4.28", assets: [] }),
      releasePayload({ tag: "zh-tw-v0.4.27", assets: [] }),
    ]);

    const result = await fetchLatestRelease();
    expect(result.version).toBe("zh-tw-v0.4.28");
    expect(result.assets.macArm64Dmg).toBeUndefined();
    expect(result.assets.winX64Exe).toBeUndefined();
  });

  it("skips prereleases and drafts in the candidate list", async () => {
    mockFetchWithReleases([
      releasePayload({ tag: "zh-tw-v0.2.15-rc.1", prerelease: true }),
      releasePayload({ tag: "zh-tw-v0.2.14-draft", draft: true }),
      releasePayload({ tag: "zh-tw-v0.2.14", assets: completeAssets("0.2.14") }),
    ]);

    const result = await fetchLatestRelease();
    expect(result.version).toBe("zh-tw-v0.2.14");
  });

  it("returns an empty release shape when the API errors", async () => {
    const fetchMock = vi.fn().mockResolvedValue(
      new Response("rate limited", { status: 403 }),
    );
    vi.stubGlobal("fetch", fetchMock);
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

    const result = await fetchLatestRelease();
    expect(result).toEqual({
      version: null,
      publishedAt: null,
      htmlUrl: null,
      assets: {},
    });
    expect(warnSpy).toHaveBeenCalled();
  });

  it("returns an empty release shape when all candidates are filtered out", async () => {
    mockFetchWithReleases([
      releasePayload({ tag: "zh-tw-v0.2.15-rc.1", prerelease: true }),
      releasePayload({ tag: "zh-tw-v0.2.14-draft", draft: true }),
    ]);

    const result = await fetchLatestRelease();
    expect(result.version).toBeNull();
    expect(result.assets).toEqual({});
  });
});
