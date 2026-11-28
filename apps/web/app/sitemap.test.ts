// @vitest-environment node
import { describe, expect, it } from "vitest";
import { isLiveRoute, siteRoutes } from "@/features/landing/site/sitemap";
import sitemap from "./sitemap";

describe("sitemap.xml", () => {
  const entries = sitemap();
  const urls = entries.map((entry) => entry.url);

  it("lists every released marketing route, including /download", () => {
    const live = siteRoutes().filter(isLiveRoute);
    expect(urls).toEqual(live.map((route) => (route === "/" ? "https://10.1.24.90:45671" : `https://10.1.24.90:45671${route}`)));
    expect(urls).toContain("https://10.1.24.90:45671/download");
    expect(urls).not.toContain("https://10.1.24.90:45671/usecases");
  });

  it("dates only the pages that change with each release", () => {
    const dated = entries.filter((entry) => entry.lastModified).map((entry) => entry.url);
    expect(dated).toEqual(["https://10.1.24.90:45671", "https://10.1.24.90:45671/changelog"]);
  });

  it("declares every language version", () => {
    const download = entries.find((entry) => entry.url.endsWith("/download"));
    expect(download?.alternates?.languages).toMatchObject({
      en: "https://10.1.24.90:45671/download",
      "zh-TW": "https://10.1.24.90:45671/zh/download",
      "x-default": "https://10.1.24.90:45671/download",
    });
  });
});
