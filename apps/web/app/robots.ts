import type { MetadataRoute } from "next";

export default function robots(): MetadataRoute.Robots {
  const baseUrl = "https://10.1.24.90:45671";

  return {
    rules: [
      {
        userAgent: "*",
        allow: ["/", "/about", "/changelog"],
        disallow: [
          "/api/",
          "/ws",
          "/auth/",
          "/issues",
          "/board",
          "/inbox",
          "/agents",
          "/settings",
          "/my-issues",
          "/runtimes",
          "/skills",
        ],
      },
    ],
    sitemap: [`${baseUrl}/sitemap.xml`, `${baseUrl}/docs/sitemap.xml`],
  };
}
