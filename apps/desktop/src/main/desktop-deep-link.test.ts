// @vitest-environment node
import { readFileSync } from "node:fs";
import { describe, expect, it, vi } from "vitest";
import { DESKTOP_PROTOCOL, handleDesktopDeepLink } from "./desktop-deep-link";

describe("Desktop deep links", () => {
  it("delivers the official Web login handoff to the auth listener", () => {
    const dispatch = vi.fn();

    handleDesktopDeepLink(
      "multica://auth/callback?token=handoff-jwt",
      dispatch,
    );

    expect(dispatch).toHaveBeenCalledExactlyOnceWith("auth:token", "handoff-jwt");
  });

  it("preserves invitation dispatch and URL decoding", () => {
    const dispatch = vi.fn();

    handleDesktopDeepLink("multica://invite/invitation%2Did", dispatch);

    expect(dispatch).toHaveBeenCalledExactlyOnceWith("invite:open", "invitation-id");
  });

  it.each([
    "multica-zh-tw://auth/callback?token=handoff-jwt",
    "https://auth/callback?token=handoff-jwt",
    "multica://auth/callback",
    "multica://auth/callback?token=",
    "multica://auth/other?token=handoff-jwt",
    "multica://unknown/callback?token=handoff-jwt",
    "multica://invite/",
    "multica://invite/%E0%A4%A",
    "invalid URL",
  ])("ignores unsupported or malformed links: %s", (url) => {
    const dispatch = vi.fn();

    handleDesktopDeepLink(url, dispatch);

    expect(dispatch).not.toHaveBeenCalled();
  });

  it.each(["electron-builder.yml", "electron-builder.zh-tw.yml"])(
    "registers the same login protocol in %s as the main handler",
    (filename) => {
      const config = readFileSync(new URL(`../../${filename}`, import.meta.url), "utf8");
      const scheme = config.match(/schemes:\s*\n\s*-\s+([a-z0-9-]+)\s*(?:\n|$)/)?.[1];
      expect(scheme).toBe(DESKTOP_PROTOCOL);

      const dispatch = vi.fn();
      handleDesktopDeepLink(`${scheme}://auth/callback?token=handoff-jwt`, dispatch);
      expect(dispatch).toHaveBeenCalledExactlyOnceWith("auth:token", "handoff-jwt");
    },
  );
});
