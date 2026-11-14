"use client";

import { cn } from "@multica/ui/lib/utils";
import { useLocale } from "../../i18n";
import { SiteTextLink, focusRing } from "../../site/primitives";
import { osOf, platformOf, type PlatformKey } from "../../utils/download-target";
import type { DownloadAssets, DownloadKey } from "../../utils/parse-release-assets";
import { DownloadLink } from "./download-link";
import { OSIcon } from "./os-icons";
import { ALL_RELEASES_URL } from "../../utils/release-source";

type FormatLabel = "formatDmg" | "formatZip" | "formatExe" | "formatAppImage" | "formatDeb" | "formatRpm";

const ROWS: { platform: PlatformKey; formats: [DownloadKey, FormatLabel][] }[] = [
  { platform: "macArm64", formats: [["macArm64Dmg", "formatDmg"], ["macArm64Zip", "formatZip"]] },
];

const FORMAT_CHIP =
  "inline-flex h-7 items-center rounded-site-inner border px-2.5 font-site-mono text-[0.75rem] transition-colors";

/**
 * Available Apple Silicon formats download directly.
 */
export function AllPlatforms({
  assets,
  current,
  onStart,
  releasesUrl = ALL_RELEASES_URL,
}: {
  assets: DownloadAssets;
  /** The installer offered as this visitor's main download. */
  current?: DownloadKey;
  onStart?: (key: DownloadKey) => void;
  releasesUrl?: string;
}) {
  const { t } = useLocale();
  const d = t.download.allPlatforms;
  const currentPlatform = current ? platformOf(current) : undefined;

  return (
    <section
      id="all-platforms"
      aria-labelledby="all-platforms-title"
      className="overflow-hidden rounded-site-panel border border-site-line bg-site-surface"
    >
      <div className="flex items-center justify-between gap-4 border-b border-site-line px-4 py-3">
        <h2 id="all-platforms-title" className="text-site-small text-site-ink">
          {d.title}
        </h2>
        <SiteTextLink href={releasesUrl} external className="text-site-small text-site-muted hover:text-site-ink">
          {d.allReleases}
        </SiteTextLink>
      </div>
      <ul>
        {ROWS.map(({ platform, formats }) => {
          const [osName, arch] = t.download.platforms[platform].split(" · ");
          const isCurrent = platform === currentPlatform;
          return (
            <li
              key={platform}
              className={cn(
                "flex flex-wrap items-center gap-x-3 gap-y-2 border-t border-site-rule px-4 py-3 first:border-t-0",
                isCurrent && "bg-site-sunken",
              )}
            >
              <OSIcon os={osOf(formats[0]![0])} size={16} className="shrink-0 text-site-ink-secondary" aria-hidden />
              <span className="flex min-w-fit flex-1 items-center gap-1.5 text-site-small">
                <span className="text-site-ink">{osName}</span>
                <span className="text-site-muted">{arch}</span>
                {isCurrent ? (
                  <span className="ml-1 rounded-site-inner border border-site-line bg-site-surface px-1.5 py-0.5 text-[0.75rem] text-site-ink-secondary">
                    {d.thisDevice}
                  </span>
                ) : null}
              </span>
              <span className="flex gap-1.5">
                {formats.map(([key, label]) => {
                  const href = assets[key];
                  return href ? (
                    <DownloadLink
                      key={key}
                      downloadKey={key}
                      href={href}
                      onStart={onStart}
                      className={cn(
                        FORMAT_CHIP,
                        "border-site-line bg-site-surface text-site-ink-secondary hover:border-site-line-strong hover:text-site-ink",
                        focusRing,
                      )}
                    >
                      {d[label]}
                    </DownloadLink>
                  ) : null;
                })}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
