"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { useLocale } from "@/features/landing/i18n";
import { SitePage } from "@/features/landing/site/site-page";
import { localizeHref } from "@/lib/landing-locale-path";
import { DownloadHero } from "@/features/landing/components/download/hero";
import { CliSection } from "@/features/landing/components/download/cli-section";
import { DownloadStarted } from "@/features/landing/components/download/started";
import { useDetectedPlatform } from "@/features/landing/components/download/use-detected-platform";
import { takePendingDownload } from "@/features/landing/utils/download-target";
import type { LatestRelease } from "@/features/landing/utils/github-release";
import type { DetectResult } from "@/features/landing/utils/os-detect";
import type { DownloadKey } from "@/features/landing/utils/parse-release-assets";

const MIRROR_VERSION = "zh-tw-v0.4.43-zh-tw.7";
const MIRROR_BASE = "/downloads/0.4.43-zh-tw.7";
const MIRROR_INSTALLER_URL = `https://github.com/hyc5566/multica/releases/download/${MIRROR_VERSION}/install.sh`;

export function DownloadClient({
  release,
  platform,
  started: startedFromUrl,
}: {
  release: LatestRelease;
  platform: DetectResult;
  /** Set when a homepage download button sent the visitor here. */
  started: DownloadKey | null;
}) {
  const effectiveRelease = useMemo(() => {
    const useMirror = release.version === null || release.version === MIRROR_VERSION;
    if (!useMirror) return release;
    return {
      ...release,
      version: release.version ?? MIRROR_VERSION,
      htmlUrl: release.htmlUrl ?? `https://github.com/hyc5566/multica/releases/tag/${MIRROR_VERSION}`,
      assets: {
        ...release.assets,
        macArm64Zip: release.assets.macArm64Zip ?? `${MIRROR_BASE}/multica-desktop-0.4.43-zh-tw.7-mac-arm64.zip`,
      },
      installerUrl: release.installerUrl ?? MIRROR_INSTALLER_URL,
    };
  }, [release]);
  const router = useRouter();
  const { locale } = useLocale();
  const detected = useDetectedPlatform(platform);
  const [started, setStarted] = useState(startedFromUrl);
  const startedHref = started ? effectiveRelease.assets[started] : undefined;

  // The homepage click left a pending flag; start that file once. A reload
  // or a shared link shows the same page without downloading again.
  useEffect(() => {
    const href = startedFromUrl ? effectiveRelease.assets[startedFromUrl] : undefined;
    if (startedFromUrl && href && takePendingDownload(startedFromUrl)) window.location.assign(href);
  }, [startedFromUrl, effectiveRelease.assets]);

  return (
    <SitePage>
      {started && startedHref ? (
        <DownloadStarted
          downloadKey={started}
          href={startedHref}
          size={effectiveRelease.sizes[started]}
          onBack={() => {
            setStarted(null);
            if (startedFromUrl) router.replace(localizeHref(locale, "/download"), { scroll: false });
          }}
        />
      ) : (
        <>
          <DownloadHero detected={detected} release={effectiveRelease} onStart={setStarted} />
          <CliSection installerUrl={effectiveRelease.installerUrl} />
        </>
      )}
    </SitePage>
  );
}
