export const DEFAULT_DOWNLOAD_REPO = "hyc5566/multica";
export const ALL_RELEASES_URL = `https://github.com/${DEFAULT_DOWNLOAD_REPO}/releases`;

/** Public repository coordinates; authentication stays in the server fetcher. */
export function releaseSource(configuredRepo?: string) {
  const repo = configuredRepo?.trim() || DEFAULT_DOWNLOAD_REPO;
  if (!/^[a-zA-Z0-9][a-zA-Z0-9-]*\/[a-zA-Z0-9][a-zA-Z0-9._-]*$/.test(repo)) {
    throw new Error("MULTICA_DOWNLOAD_REPO must be a GitHub owner/repo");
  }
  return {
    repo,
    apiUrl: `https://api.github.com/repos/${repo}/releases?per_page=5`,
    releasesUrl: `https://github.com/${repo}/releases`,
    taiwanOnly: repo.toLowerCase() === DEFAULT_DOWNLOAD_REPO.toLowerCase(),
  };
}

export function isGitHubInstallerUrl(url: string) {
  return /^https:\/\/github\.com\/[a-zA-Z0-9-]+\/[a-zA-Z0-9._-]+\/releases\/download\/[a-zA-Z0-9._%+-]+\/install\.sh$/.test(url);
}
