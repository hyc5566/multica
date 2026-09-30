import {
  parseReleaseAssets,
  type DownloadAssets,
} from "./parse-release-assets";

/**
 * Server-side fetcher for the latest downloadable Multica release,
 * designed to run inside a Next.js server component. Response is cached
 * by the Next.js fetch cache for 5 minutes (Vercel ISR) so hitting
 * /download costs at most one GitHub API call per region per 5 minutes.
 *
 * Taiwan releases currently publish an Apple Silicon App and macOS/Linux
 * CLI assets. Choose the newest release with a Mac App instead of requiring
 * the upstream project's Windows and Linux Desktop artifacts.
 *
 * On any failure (network, rate limit, malformed payload) returns a
 * `null`-shaped result and logs — the page degrades to a "version
 * unavailable" view rather than 500ing.
 */

export interface LatestRelease {
  version: string | null;
  publishedAt: string | null;
  htmlUrl: string | null;
  assets: DownloadAssets;
  installerUrl?: string;
}

// Five candidates tolerate several drafts or releases without a Mac App.
const GITHUB_RELEASES_URL =
  "https://api.github.com/repos/hyc5566/multica/releases?per_page=5";

const REVALIDATE_SECONDS = 300;

interface GitHubReleasePayload {
  tag_name?: string;
  published_at?: string;
  html_url?: string;
  prerelease?: boolean;
  draft?: boolean;
  assets?: Array<{ name: string; browser_download_url: string }>;
}

export async function fetchLatestRelease(): Promise<LatestRelease> {
  const headers: Record<string, string> = {
    Accept: "application/vnd.github+json",
    "X-GitHub-Api-Version": "2022-11-28",
  };
  // Optional PAT for local development and self-hosted deploys where
  // the shared outbound IP keeps hitting the 60-requests/hour
  // unauthenticated limit. Vercel's fetch cache is shared across all
  // regions so production rarely needs this — but the env var lets
  // anyone running the site locally avoid the rate-limit dance. Never
  // prefix this with `NEXT_PUBLIC_`; the token must stay server-side.
  const token = process.env.GITHUB_TOKEN;
  if (token) {
    headers.Authorization = `Bearer ${token}`;
  }

  try {
    const res = await fetch(GITHUB_RELEASES_URL, {
      next: { revalidate: REVALIDATE_SECONDS },
      headers,
    });
    if (!res.ok) {
      throw new Error(`GitHub API responded ${res.status}`);
    }
    const data = (await res.json()) as GitHubReleasePayload[];

    // Defensive filter — Multica doesn't publish prereleases or drafts
    // today, but the endpoint returns them if that ever changes. A
    // prerelease shadowing a stable version on /download would be a
    // regression.
    const stable = data.filter((r) => !r.prerelease && !r.draft && r.tag_name?.startsWith("zh-tw-v"));
    const chosen = pickRelease(stable);
    if (!chosen) {
      return emptyRelease();
    }

    return {
      version: chosen.release.tag_name ?? null,
      publishedAt: chosen.release.published_at ?? null,
      htmlUrl: chosen.release.html_url ?? null,
      assets: chosen.assets,
      installerUrl: chosen.release.assets?.find((asset) =>
        asset.name === "install.sh" &&
        asset.browser_download_url ===
          `https://github.com/hyc5566/multica/releases/download/${chosen.release.tag_name}/install.sh`,
      )?.browser_download_url,
    };
  } catch (err) {
    console.warn("[download] fetchLatestRelease failed:", err);
    return emptyRelease();
  }
}

interface PickedRelease {
  release: GitHubReleasePayload;
  assets: DownloadAssets;
}

/** Newest release with an Apple Silicon App, or the newest release overall. */
function pickRelease(
  candidates: GitHubReleasePayload[],
): PickedRelease | undefined {
  const parsed = candidates.map((release) => ({
    release,
    assets: parseReleaseAssets(release.assets ?? []),
  }));

  const downloadable = parsed.find((c) => c.assets.macArm64Dmg || c.assets.macArm64Zip);
  if (downloadable) {
    if (downloadable !== parsed[0]) {
      console.warn(
        `[download] skipping ${parsed[0]?.release.tag_name ?? "latest release"}` +
          ` — no Apple Silicon App; showing ${downloadable.release.tag_name}`,
      );
    }
    return downloadable;
  }
  return parsed[0];
}

function emptyRelease(): LatestRelease {
  return {
    version: null,
    publishedAt: null,
    htmlUrl: null,
    assets: {},
  };
}
