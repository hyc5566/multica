// Pure decision logic for the daemon version-check flow. Kept in its own
// module so it can be unit-tested without mocking Electron, execFile, or
// the HTTP health probe.

export interface VersionCheckHealth {
  status?: string;
  cli_version?: string;
  active_task_count?: number;
  /** zh-tw daemons that reload into the bundled CLI themselves report this. */
  zh_tw_self_reload?: boolean;
}

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

  // A daemon that reports zh_tw_self_reload restarts itself into the bundled
  // CLI once idle, atomically under its claim barrier; stopping it from here
  // could race a task claimed after the health snapshot. Decided by the
  // reported capability, never by version format: older zh-tw releases match
  // the format but cannot reload, so they take the idle restart below.
  if (running.zh_tw_self_reload === true) return "defer";
  // An unknown task count is not "idle".
  const activeTasks = running.active_task_count;
  if (activeTasks === undefined || activeTasks > 0) return "defer";
  return "restart";
}
