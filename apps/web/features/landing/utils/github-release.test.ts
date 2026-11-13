// @vitest-environment node
import { afterEach, describe, expect, it, vi } from "vitest";
import { fetchLatestRelease, RELEASE_TIMEOUT_MS } from "./github-release";

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
  vi.unstubAllEnvs();
  vi.restoreAllMocks();
});

describe("fetchLatestRelease", () => {
  it("uses an overridden repository for stable releases, installer and navigation", async () => {
    vi.stubEnv("MULTICA_DOWNLOAD_REPO", "another-owner/multica");
    const installerUrl = "https://github.com/another-owner/multica/releases/download/v0.9.0/install.sh";
    const fetchMock = mockFetchWithReleases([
      releasePayload({ tag: "v0.9.0", assets: [
        ...macOnlyAssets("0.9.0"),
        { name: "install.sh", browser_download_url: installerUrl },
      ] }),
    ]);
    const result = await fetchLatestRelease();
    expect(fetchMock).toHaveBeenCalledWith(
      "https://api.github.com/repos/another-owner/multica/releases?per_page=5", expect.anything(),
    );
    expect(result.version).toBe("v0.9.0");
    expect(result.releasesUrl).toBe("https://github.com/another-owner/multica/releases");
    expect(result.installerUrl).toBe(installerUrl);
  });

  it("keeps a newer Linux-only release instead of substituting an older Mac App", async () => {
    const name = "multica-cli-daemon-0.9.0-zh-tw.1+up.013be77-linux-amd64.tar.gz";
    mockFetchWithReleases([
      releasePayload({ tag: "zh-tw-v0.9.0-zh-tw.1", assets: [
        { name, browser_download_url: `https://downloads.test/${name}` },
      ] }),
      releasePayload({ tag: "zh-tw-v0.8.2-zh-tw.1", assets: macOnlyAssets("0.8.2-zh-tw.1") }),
    ]);
    const result = await fetchLatestRelease();
    expect(result.version).toBe("zh-tw-v0.9.0-zh-tw.1");
    expect(result.assets).toEqual({});
    expect(result.cliAssets).toEqual({ linuxX64: `https://downloads.test/${name}` });
  });

  it("rejects an invalid repository without requesting another origin", async () => {
    vi.stubEnv("MULTICA_DOWNLOAD_REPO", "https://other.test/repo");
    vi.spyOn(console, "warn").mockImplementation(() => {});
    const fetchMock = mockFetchWithReleases([]);
    expect((await fetchLatestRelease()).version).toBeNull();
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("uses the published Taiwan installer and Apple Silicon asset", async () => {
    const installerUrl = "https://github.com/hyc5566/multica/releases/download/zh-tw-v0.4.43-zh-tw.7/install.sh";
    const assets = [
      { name: "install.sh", browser_download_url: installerUrl },
      { name: "multica-desktop-0.4.43-zh-tw.7-mac-arm64.zip", browser_download_url: `${installerUrl}/mac.zip`, size: 123456 },
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
    expect(result.sizes.macArm64Zip).toBe(123456);
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
      sizes: {},
      cliAssets: {},
      releasesUrl: "https://github.com/hyc5566/multica/releases",
    });
    expect(warnSpy).toHaveBeenCalled();
  });

  // The homepage awaits this on a cold cache; a GitHub that accepts the
  // connection and then stalls must not hold the page.
  it("gives up on a stalled request and returns the empty release", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(
        (_url: string, init: RequestInit) =>
          new Promise<Response>((_resolve, reject) => {
            init.signal?.addEventListener("abort", () => reject(init.signal?.reason));
          }),
      );
      vi.stubGlobal("fetch", fetchMock);
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      const pending = fetchLatestRelease();
      await vi.advanceTimersByTimeAsync(RELEASE_TIMEOUT_MS);

      await expect(pending).resolves.toMatchObject({ version: null, assets: {} });
      expect(warnSpy).toHaveBeenCalled();
    } finally {
      vi.useRealTimers();
    }
  });

  it("gives up when the response body stalls after the headers arrive", async () => {
    vi.useFakeTimers();
    try {
      const fetchMock = vi.fn(async (_url: string, init: RequestInit) => {
        const body = new ReadableStream({
          start(controller) {
            init.signal?.addEventListener("abort", () => controller.error(init.signal?.reason));
          },
        });
        return new Response(body, { status: 200 });
      });
      vi.stubGlobal("fetch", fetchMock);
      vi.spyOn(console, "warn").mockImplementation(() => {});

      const pending = fetchLatestRelease();
      await vi.advanceTimersByTimeAsync(RELEASE_TIMEOUT_MS);

      await expect(pending).resolves.toMatchObject({ version: null, assets: {} });
    } finally {
      vi.useRealTimers();
    }
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
