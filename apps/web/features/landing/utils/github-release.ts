import {
  parseReleaseAssets,
  parseReleaseAssetSizes,
  parseCliAssets,
  type CliAssets,
  type DownloadAssets,
  type DownloadSizes,
  type GitHubAsset,
} from "./parse-release-assets";
import { ALL_RELEASES_URL, releaseSource } from "./release-source";

/**
 * Server-side fetcher for the latest downloadable Multica release,
 * designed to run inside a Next.js server component. Response is cached
 * by the Next.js fetch cache for 5 minutes (Vercel ISR) so hitting
 * /download costs at most one GitHub API call per region per 5 minutes.
 *
 * Choose the newest release with any supported App or CLI asset. Missing
 * platforms stay unavailable; files from different releases are never mixed.
 *
 * On any failure (network, timeout, rate limit, malformed payload) returns a
 * `null`-shaped result and logs — the page degrades to a "version
 * unavailable" view rather than 500ing.
 */

export interface LatestRelease {
  version: string | null;
  publishedAt: string | null;
  htmlUrl: string | null;
  assets: DownloadAssets;
  sizes: DownloadSizes;
  installerUrl?: string;
  releasesUrl?: string;
  cliAssets?: CliAssets;
}

const REVALIDATE_SECONDS = 300;

// The homepage hero awaits this on a cold cache, so a GitHub that accepts
// the connection but never answers would hold the whole page. Past this,
// the page renders with the empty release instead.
export const RELEASE_TIMEOUT_MS = 3000;

interface GitHubReleasePayload {
  tag_name?: string;
  published_at?: string;
  html_url?: string;
  prerelease?: boolean;
  draft?: boolean;
  assets?: GitHubAsset[];
}

export async function fetchLatestRelease(): Promise<LatestRelease> {
  let releasesUrl = ALL_RELEASES_URL;
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

  const controller = new AbortController();
  const timer = setTimeout(
    () => controller.abort(new Error(`GitHub API timed out after ${RELEASE_TIMEOUT_MS}ms`)),
    RELEASE_TIMEOUT_MS,
  );
  try {
    const source = releaseSource(process.env.MULTICA_DOWNLOAD_REPO);
    releasesUrl = source.releasesUrl;
    const res = await fetch(source.apiUrl, {
      next: { revalidate: REVALIDATE_SECONDS },
      headers,
      signal: controller.signal,
    });
    if (!res.ok) {
      throw new Error(`GitHub API responded ${res.status}`);
    }
    const data = (await res.json()) as GitHubReleasePayload[];

    // Defensive filter — Multica doesn't publish prereleases or drafts
    // today, but the endpoint returns them if that ever changes. A
    // prerelease shadowing a stable version on /download would be a
    // regression.
    const stable = data.filter((r) => !r.prerelease && !r.draft && r.tag_name &&
      (!source.taiwanOnly || r.tag_name.startsWith("zh-tw-v")));
    const chosen = pickRelease(stable);
    if (!chosen) {
      return emptyRelease(releasesUrl);
    }

    return {
      version: chosen.release.tag_name ?? null,
      publishedAt: chosen.release.published_at ?? null,
      htmlUrl: chosen.release.html_url ?? null,
      assets: chosen.assets,
      sizes: parseReleaseAssetSizes(chosen.release.assets ?? []),
      cliAssets: chosen.cliAssets,
      releasesUrl,
      installerUrl: chosen.release.assets?.find((asset) =>
        asset.name === "install.sh" &&
        asset.browser_download_url ===
          `${releasesUrl}/download/${chosen.release.tag_name}/install.sh`,
      )?.browser_download_url,
    };
  } catch (err) {
    console.warn("[download] fetchLatestRelease failed:", err);
    return emptyRelease(releasesUrl);
  } finally {
    clearTimeout(timer);
  }
}

interface PickedRelease {
  release: GitHubReleasePayload;
  assets: DownloadAssets;
  cliAssets: CliAssets;
}

/** A Linux-only release is as downloadable as a Mac-only one. */
function pickRelease(
  candidates: GitHubReleasePayload[],
): PickedRelease | undefined {
  const parsed = candidates.map((release) => ({
    release,
    assets: parseReleaseAssets(release.assets ?? []),
    cliAssets: parseCliAssets(release.assets ?? []),
  }));

  return parsed.find((c) => Object.keys(c.assets).length > 0 || Object.keys(c.cliAssets).length > 0) ?? parsed[0];
}

function emptyRelease(releasesUrl: string): LatestRelease {
  return {
    version: null,
    publishedAt: null,
    htmlUrl: null,
    assets: {},
    sizes: {},
    cliAssets: {},
    releasesUrl,
  };
}
