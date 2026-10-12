// Pure decision logic for the daemon version-check flow. Kept in its own
// module so it can be unit-tested without mocking Electron, execFile, or
// the HTTP health probe.

export interface VersionCheckHealth {
  status?: string;
  cli_version?: string;
  active_task_count?: number;
}

const ZH_TW_RELEASE_VERSION = /^\d+\.\d+\.\d+-zh-tw\.\d+$/;

export type VersionAction = "restart" | "defer" | "ok" | "not_running";

/**
 * Decides what the daemon-manager should do given the currently-resolved
 * bundled CLI version and the latest /health payload.
 *
 *   not_running: no daemon is up, nothing to do
 *   ok:          versions match, OR either side is unknown (fail safe)
 *   defer:       versions differ but the daemon is busy — wait for drain
 *   restart:     versions differ and the daemon is idle — safe to restart
 *
 * Pure function: no I/O, no side effects, no module state.
 */
export function decideVersionAction(
  bundled: string | null,
  running: VersionCheckHealth | null,
): VersionAction {
  if (!running || running.status !== "running") return "not_running";

  const runningVersion = running.cli_version;
  if (!bundled || !runningVersion) return "ok";
  if (runningVersion === bundled) return "ok";

  // zh-tw release daemons restart themselves into the bundled CLI once idle,
  // atomically under their claim barrier; stopping them from here could race
  // a task claimed after the health snapshot.
  if (ZH_TW_RELEASE_VERSION.test(runningVersion)) return "defer";
  // An unknown task count is not "idle".
  const activeTasks = running.active_task_count;
  if (activeTasks === undefined || activeTasks > 0) return "defer";
  return "restart";
}
