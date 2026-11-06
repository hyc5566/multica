import type { MainRendererMessageChannel } from "../shared/main-renderer-messages";

export const DESKTOP_PROTOCOL = "multica";

export function handleDesktopDeepLink(
  url: string,
  dispatch: (channel: MainRendererMessageChannel, payload: unknown) => void,
): void {
  try {
    const parsed = new URL(url);
    if (parsed.protocol !== `${DESKTOP_PROTOCOL}:`) return;

    if (parsed.hostname === "auth" && parsed.pathname === "/callback") {
      const token = parsed.searchParams.get("token");
      if (token) dispatch("auth:token", token);
      return;
    }

    if (parsed.hostname === "invite") {
      const id = parsed.pathname.replace(/^\//, "");
      if (id) dispatch("invite:open", decodeURIComponent(id));
    }
  } catch {
    // Ignore malformed URLs.
  }
}
