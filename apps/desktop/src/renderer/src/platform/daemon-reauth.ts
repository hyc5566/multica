import { useAuthStore } from "@multica/core/auth";
import { toast } from "sonner";
import type { DaemonTranslator } from "../components/daemon-i18n";

/** Manual starts refresh credentials using the signed-in app, not PATH tools. */
export async function startDaemonWithSession(t: DaemonTranslator): Promise<void> {
  const user = useAuthStore.getState().user;
  const token = localStorage.getItem("multica_token");
  if (!user || !token) {
    useAuthStore.getState().logout();
    return;
  }
  try {
    const runtime = window.desktopAPI.runtimeConfig;
    if (!runtime.ok) throw new Error("Desktop server configuration is unavailable");
    await window.daemonAPI.setTargetApiUrl(runtime.config.apiUrl);
    await window.daemonAPI.syncToken(token, user.id);
    const result = await window.daemonAPI.start();
    if (!result.success) throw new Error(result.error || "Daemon start failed");
  } catch (error) {
    toast.error(t(($) => $.desktop.daemon.start_failed), {
      description: error instanceof Error ? error.message : String(error),
    });
  }
}

/**
 * Re-establish the local daemon's credentials after it failed to authenticate
 * (daemon state "auth_expired", surfaced by daemon-manager's token probe — see
 * #3512).
 *
 * The desktop owns the daemon's PAT: it mints one from the user's session token
 * and caches it per profile. A stale/revoked cached PAT is the common cause (and
 * merely restarting the app reuses the same bad PAT), so the main process drops
 * the cached token, mints a fresh one, and restarts the daemon.
 *
 * Failure handling is deliberately conservative — we only force a full re-login
 * when the session token itself is rejected (a real 401). A transient failure
 * (mint 5xx, network blip, config write error, restart hiccup) keeps the user
 * signed in and shows a retryable toast, so a momentary glitch never logs them
 * out. The 401-vs-transient classification happens in the main process where the
 * real HTTP status is available; here we just act on the verdict.
 */
export async function reauthenticateDaemon(
  t: DaemonTranslator,
): Promise<void> {
  const user = useAuthStore.getState().user;
  const token = localStorage.getItem("multica_token");
  if (!user || !token) {
    // No usable session at all — the standard recovery is the login page.
    useAuthStore.getState().logout();
    return;
  }

  try {
    const result = await window.daemonAPI.reauthenticate(token, user.id);
    if (result.ok) return; // daemon restarting; status flips via onStatusChange
    if (result.reason === "session_invalid") {
      // The session token itself is rejected (401) — full re-login.
      useAuthStore.getState().logout();
      return;
    }
    // Transient failure — keep the user signed in and let them retry.
    toast.error(t(($) => $.desktop.daemon.reconnect_failed), {
      description:
        result.message || t(($) => $.desktop.daemon.try_again_moment),
    });
  } catch (err) {
    // An unexpected IPC error is not an auth failure — never log out on it.
    toast.error(t(($) => $.desktop.daemon.reconnect_failed), {
      description:
        err instanceof Error
          ? err.message
          : t(($) => $.desktop.daemon.try_again),
    });
  }
}
