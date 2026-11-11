import { z } from "zod";
import { parseWithFallback } from "@multica/core/api/schema";
import { app, ipcMain, BrowserWindow, shell, dialog, type MessageBoxOptions } from "electron";
import { execFile } from "child_process";
import { createHash } from "crypto";
import {
  readFile,
  writeFile,
  mkdir,
  rm,
  open,
  stat,
  lstat,
} from "fs/promises";
import {
  existsSync,
  watchFile,
  unwatchFile,
  type StatsListener,
} from "fs";
import { join } from "path";
import { homedir, hostname } from "os";
import type {
  DaemonStatus,
  DaemonPrefs,
  LocalRuntimeProbe,
} from "../shared/daemon-types";
import { daemonStatusAlive } from "../shared/daemon-types";
import { ensureManagedCli, managedCliPath } from "./cli-bootstrap";
import { verifyCLIEntries } from "./zh-tw-cli-entry";
import { decideVersionAction } from "./version-decision";
import { createDaemonQuitHandler } from "./daemon-quit";
import { quitConfirmationOptions, quitFailureOptions } from "./daemon-quit-dialogs";
import {
  prepareDaemonForInstall,
  type DaemonInstallPrep,
} from "./update-install-gate";
import {
  deriveProfileName,
  profileArgs,
  profileConfigPath,
  profileDir,
  profileLogPath,
  profilePidPath,
  profileUserIdPath,
} from "./daemon-profile";
import { probeProfileHealth, readDaemonPortRecord } from "./daemon-port";
import {
  DaemonOperationGate,
  DaemonRecoveryPolicy,
  daemonProcessExists,
  parseDaemonPid,
  recoveryStartAllowed,
  runDaemonRecoveryAttempt,
} from "./daemon-recovery";
import {
  daemonLifecycleUnreachable,
  isDaemonExternallyManaged,
  normalizeHostOS,
} from "./daemon-os";
import {
  classifyAuthProbe,
  isAuthStatusError,
  type AuthProbeResult,
} from "./daemon-auth-probe";
import { daemonSpawnEnv } from "./daemon-spawn-env";
import { focusEnvironmentAt, type FocusEnvironmentResult } from "./environment-focus";

import {
  desktopTokenMachine,
  desktopTokenRecord,
  ownedDesktopTokenId,
  selectDesktopToken,
  type DesktopTokenOwner,
  type MintedDesktopToken,
} from "./desktop-token";

const POLL_INTERVAL_MS = 5_000;
const PREFS_PATH = join(homedir(), ".multica", "desktop_prefs.json");
const LOG_TAIL_RETRY_MS = 2_000;
const LOG_TAIL_MAX_RETRIES = 5;
// How long a start may sit in "starting" (with no /health) before we probe the
// token to find out whether login expired. The daemon's own startup can legitimately
// take a while (it renews the PAT and lists workspaces before serving /health), so we
// wait past the common case to avoid probing healthy-but-slow starts.
const AUTH_PROBE_GRACE_MS = 10_000;
// `multica daemon start` blocks until the daemon reports ready, polling /health
// for up to its own startup timeout (45s in server/cmd/multica/cmd_daemon.go) to
// cover cold-start agent-version detection. This execFile timeout MUST stay
// above that — otherwise Electron kills the CLI supervisor mid-startup and a
// healthy-but-slow start is misreported as a failure (the detached daemon child
// keeps running, so the UI flashes "stopped" then "running").
const DAEMON_START_EXEC_TIMEOUT_MS = 60_000;
const HEALTH_PROBE_TIMEOUT_MS = 2_000;
// Five times the UI probe and equal to the auth-probe grace: a daemon that
// misses this second independent window is no longer treated as merely busy.
const RECOVERY_HEALTH_PROBE_TIMEOUT_MS = 10_000;

const DEFAULT_PREFS: DaemonPrefs = { autoStart: false, autoStop: false };

// Always a resolved Desktop-owned profile. "Not resolved yet" is represented by
// `null` at every call site, never by an empty name — see daemon-profile.ts.
interface ActiveProfile {
  name: string;
}

let statusPollTimer: ReturnType<typeof setInterval> | null = null;
let logTailWatcher: { path: string; listener: StatsListener } | null = null;
let logTailOwner: Electron.WebContents | null = null;
let currentState: DaemonStatus["state"] = "installing_cli";
let getMainWindow: () => BrowserWindow | null = () => null;
let getStatusWindows: () => readonly BrowserWindow[] = () => [];
let statusPollInProgress = false;
let cachedCliBinary: string | null | undefined = undefined;
let cliResolvePromise: Promise<string | null> | null = null;
let cachedCliBinaryVersion: string | null | undefined = undefined;
// Set when a CLI version mismatch was detected but the running daemon is
// busy executing tasks. The poll loop retries the check on each tick and
// fires the restart once active_task_count drops to 0.
let pendingVersionRestart = false;
let pendingTrustRefresh = 0;
let trustRefreshGeneration = 0;
let trustRestartPID: number | null = null;
let trustRestartRequestedAt = 0;
let targetApiBaseUrl: string | null = null;
let activeProfile: ActiveProfile | null = null;
// Recovery is intentionally process-local: it keeps a daemon alive while the
// Desktop main process is running, but is not an OS service/watchdog.
let desiredDaemonRunning = false;
// Non-null while the Windows updater owns daemon shutdown and installation.
let desiredRunningBeforeUpdateStop: boolean | null = null;
const UPDATE_IN_PROGRESS = { success: false, error: "An update is being installed. Try again shortly." };
let quitting = false;
// Renderer recreation must not undo a failed-quit cancellation. Keep this
// process-local until an explicit start/restart, without changing preferences.
let autoStartSuppressedAfterQuit = false;
// Once a foreign-OS daemon (for example WSL2) is observed on this profile, do
// not replace it with a native daemon if its forwarded health endpoint drops.
let externalDaemonObserved = false;
let pinnedDaemonObserved = false;
const recoveryPolicy = new DaemonRecoveryPolicy();
const lifecycleOperations = new DaemonOperationGate();

// Auth-probe state for the current start attempt. When a start fails to reach
// "running", we probe the daemon's token once (after AUTH_PROBE_GRACE_MS) to
// decide whether the cause is an expired/invalid login. `authExpired` is sticky
// until the next start attempt or a successful /health, so the UI keeps showing
// the re-login prompt instead of flapping back to "starting". See #3512.
let startingSince: number | null = null;
let authProbeDone = false;
let authExpired = false;

// Serialize all writes to any profile config file. Multiple paths
// (syncToken, resolveActiveProfile, clearToken, watch/unwatch handlers)
// may try to write concurrently; chaining them avoids interleaved writes
// corrupting the JSON.
let configWriteChain: Promise<void> = Promise.resolve();

async function readProfileUserId(profile: string): Promise<string | null> {
  try {
    const raw = await readFile(profileUserIdPath(profile), "utf-8");
    const trimmed = raw.trim();
    return trimmed || null;
  } catch {
    return null;
  }
}

async function writeProfileUserId(
  profile: string,
  userId: string,
): Promise<void> {
  await assertProfileWritable({ name: profile });
  await mkdir(profileDir(profile), { recursive: true });
  await writeFile(profileUserIdPath(profile), userId, "utf-8");
}

async function removeProfileUserId(profile: string): Promise<void> {
  await assertProfileWritable({ name: profile });
  try {
    await rm(profileUserIdPath(profile));
  } catch {
    // Already gone — nothing to do.
  }
}

function normalizeUrl(u: string): string {
  if (!u) return "";
  try {
    const parsed = new URL(u);
    return `${parsed.protocol}//${parsed.host}`.toLowerCase();
  } catch {
    return u.replace(/\/+$/, "").toLowerCase();
  }
}

function urlsMatch(a: string, b: string): boolean {
  const na = normalizeUrl(a);
  const nb = normalizeUrl(b);
  return na.length > 0 && na === nb;
}

function sendStatus(status: DaemonStatus): void {
  // One daemon serves every main window, and each shows its status.
  for (const win of getStatusWindows()) {
    if (!win.isDestroyed()) win.webContents.send("daemon:status", status);
  }
}

interface HealthPayload {
  status?: string;
  pid?: number;
  profile?: string;
  /** Daemon's runtime.GOOS. Absent on daemons older than the #3916 fix. */
  os?: string;
  uptime?: string;
  daemon_id?: string;
  device_name?: string;
  server_url?: string;
  cli_version?: string;
  lifecycle_manager?: string;
  desktop_protocol?: number;
  active_task_count?: number;
  running_task_count?: number;
  resource_wait_task_count?: number;
  /** zh-tw: the daemon reloads into the bundled CLI itself when idle. */
  zh_tw_self_reload?: boolean;
  launched_by?: string;
  agents?: string[];
  workspaces?: unknown[];
  maintenance_shutdown?: boolean;
  cli_commit?: string;
  maintenance_gate?: { supported?: boolean; blocked?: boolean; path?: string };
  update_attestation?: { binary_sha256?: string; process_started_at?: string; runtime_heartbeat_acks?: Record<string, number> };
}

export type CoordinatedDaemonSnapshot = {
  profile: string; daemonId: string; serverURLSHA256: string; pid: number; startedAt: string; port: number;
  gatePath: string; binarySHA256: string; version: string; commit: string;
  workspaces: Record<string, string[]>; runtimeIDs: string[];
};

export async function withCoordinatedDaemon<T>(work: (snapshot: CoordinatedDaemonSnapshot) => Promise<T>): Promise<T> {
  return lifecycleOperations.runForeground(async () => {
    const active = await ensureActiveProfile();
    if (!active) throw new Error("尚未確認 Desktop daemon 的服務與 profile，無法更新。");
    const probed = await fetchProfileHealthWithPort(active);
    const h = probed?.health;
    if (!probed || !h || await pinnedBoundary(active, h)) throw new Error("此 profile 由獨立 daemon 管理或無法確認；請另案處置，不會自動接管。");
    const attestation = h.update_attestation;
    const gatePath = join(profileDir(active.name), "daemon.maintenance");
    if (h.status !== "running" || h.os !== "darwin" || h.launched_by !== "desktop" || !!h.lifecycle_manager || h.profile !== active.name ||
        !h.daemon_id || !h.server_url || !Number.isSafeInteger(h.pid) || !h.pid || h.pid < 1 || h.maintenance_shutdown !== true ||
        h.maintenance_gate?.supported !== true || h.maintenance_gate.blocked !== false || h.maintenance_gate.path !== gatePath ||
        !/^[a-f0-9]{64}$/.test(attestation?.binary_sha256 ?? "") || !attestation?.process_started_at ||
        !h.cli_version || !/^[a-f0-9]{40}$/i.test(h.cli_commit ?? "")) {
      throw new Error("daemon 缺少安全協調能力或身分無法確認，無法更新。");
    }
    const workspaces: Record<string, string[]> = {};
    const runtimeIDs: string[] = [];
    if (!Array.isArray(h.workspaces)) throw new Error("無法確認原工作區與 runtime。");
    for (const row of h.workspaces) {
      if (!row || typeof row !== "object") throw new Error("無法確認原工作區與 runtime。");
      const { id, runtimes } = row as { id?: unknown; runtimes?: unknown };
      if (typeof id !== "string" || !id || Object.hasOwn(workspaces, id) || !Array.isArray(runtimes) || runtimes.some((v) => typeof v !== "string" || !v)) throw new Error("無法確認原工作區與 runtime。");
      workspaces[id] = [...runtimes].sort();
      runtimeIDs.push(...runtimes);
    }
    if (!runtimeIDs.length || new Set(runtimeIDs).size !== runtimeIDs.length || runtimeIDs.some((id) => !Number.isSafeInteger(attestation.runtime_heartbeat_acks?.[id]))) throw new Error("無法確認原 runtime 心跳。");
    return work({ profile: active.name, daemonId: h.daemon_id, serverURLSHA256: createHash("sha256").update(h.server_url).digest("hex"), pid: h.pid, startedAt: attestation.process_started_at,
      port: probed.port, gatePath, binarySHA256: attestation.binary_sha256!, version: h.cli_version, commit: h.cli_commit!, workspaces, runtimeIDs });
  });
}

async function fetchHealthAtPort(
  port: number,
  timeoutMs = HEALTH_PROBE_TIMEOUT_MS,
): Promise<HealthPayload | null> {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const res = await fetch(`http://127.0.0.1:${port}/health`, {
      signal: controller.signal,
    });
    if (!res.ok) return null;
    return (await res.json()) as HealthPayload;
  } catch {
    return null;
  } finally {
    clearTimeout(timeout);
  }
}

async function fetchProfileHealth(
  active: ActiveProfile,
  timeoutMs = HEALTH_PROBE_TIMEOUT_MS,
): Promise<HealthPayload | null> {
  return probeProfileHealth(active.name, (port) =>
    fetchHealthAtPort(port, timeoutMs),
  );
}

/** Brings forward a desktop app this profile's daemon detected. */
async function focusEnvironment(key: unknown): Promise<FocusEnvironmentResult> {
  if (typeof key !== "string" || key === "" || key.length > 128) {
    return { ok: false, reason: "failed", error: "invalid environment key" };
  }
  const active = await ensureActiveProfile();
  if (!active) return { ok: false, reason: "unavailable" };
  const health = await probeProfileHealth(active.name, async (port) => {
    const payload = await fetchHealthAtPort(port);
    return payload ? { ...payload, port } : null;
  });
  if (!health || health.status !== "running") return { ok: false, reason: "unavailable" };
  return focusEnvironmentAt(health.port, key);
}

// zh-tw: trust refresh posts back to the daemon, so it needs the port the
// accepted health response came from (daemon.port record or legacy port).
async function fetchProfileHealthWithPort(
  active: ActiveProfile,
): Promise<{ health: HealthPayload; port: number } | null> {
  let port = 0;
  const health = await probeProfileHealth(active.name, async (candidate) => {
    const data = await fetchHealthAtPort(candidate);
    port = candidate;
    return data;
  });
  return health ? { health, port } : null;
}

/**
 * Validates the daemon profile's token against the backend to find out whether
 * a stuck start is an auth problem. Hits the same endpoint `multica auth status`
 * uses (GET /api/me) with the exact token the daemon loads from config.json, so
 * the verdict matches what the daemon itself would get from the server.
 *
 * Only the HTTP status is inspected (never the body) so a future change to the
 * /api/me response shape can't break this — a 401 means the token is rejected,
 * a 2xx means it's fine, and a thrown request means the network is the problem,
 * not auth. See classifyAuthProbe for the full rule set.
 */
async function probeTokenValidity(profile: string): Promise<AuthProbeResult> {
  if (!targetApiBaseUrl) return "unknown";
  const cfg = await readProfileConfig(profile);
  const token = typeof cfg.token === "string" ? cfg.token : "";
  if (!token) return classifyAuthProbe({ noToken: true });
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), 4_000);
  try {
    const res = await fetch(`${targetApiBaseUrl.replace(/\/+$/, "")}/api/me`, {
      headers: { Authorization: `Bearer ${token}` },
      signal: controller.signal,
    });
    return classifyAuthProbe({ status: res.status });
  } catch {
    return classifyAuthProbe({ networkError: true });
  } finally {
    clearTimeout(timeout);
  }
}

// A marker reserves the profile even if it is malformed, unreadable, or a
// dangling symlink. Uncertain filesystem errors also fail closed.
async function profileReserved(profile: string): Promise<boolean> {
  try {
    await lstat(join(profileDir(profile), "daemon-management.json"));
    return true;
  } catch (error) {
    return (error as NodeJS.ErrnoException).code !== "ENOENT";
  }
}

async function pinnedBoundary(active: ActiveProfile, health?: HealthPayload | null): Promise<boolean> {
  if (await profileReserved(active.name)) return true;
  const current = health === undefined ? await fetchProfileHealth(active) : health;
  if (current?.lifecycle_manager === "launchd-pinned") pinnedDaemonObserved = true;
  return pinnedDaemonObserved;
}

async function assertProfileWritable(active: ActiveProfile): Promise<void> {
  if (await pinnedBoundary(active)) throw new Error("This daemon profile is managed independently; manage its credentials outside Desktop.");
}

async function readProfileConfig(
  profile: string,
): Promise<Record<string, unknown>> {
  try {
    const raw = await readFile(profileConfigPath(profile), "utf-8");
    const parsed = JSON.parse(raw);
    return parsed && typeof parsed === "object" ? parsed : {};
  } catch {
    return {};
  }
}

async function writeProfileConfig(
  profile: string,
  cfg: Record<string, unknown>,
): Promise<void> {
  const op = async () => {
    await assertProfileWritable({ name: profile });
    await mkdir(profileDir(profile), { recursive: true });
    await writeFile(
      profileConfigPath(profile),
      JSON.stringify(cfg, null, 2),
      "utf-8",
    );
  };
  const next = configWriteChain.catch(() => {}).then(op);
  configWriteChain = next.catch(() => {});
  return next;
}

/**
 * Returns the Desktop-owned profile for the current target API URL. Creates
 * the profile's config.json on demand with `server_url` pinned to the target.
 *
 * Returns `null` until the renderer reports its `apiUrl`. There is no profile
 * to act on in that window, and callers must do nothing rather than reach for
 * the user's default CLI profile at `~/.multica/` — neither its files nor its
 * health port.
 */
async function resolveActiveProfile(): Promise<ActiveProfile | null> {
  const target = targetApiBaseUrl;
  if (!target) return null;

  const name = deriveProfileName(target);
  const cfg = await readProfileConfig(name);

  if (cfg.server_url !== target && !(await pinnedBoundary({ name }))) {
    cfg.server_url = target;
    await writeProfileConfig(name, cfg);
    console.log(`[daemon] initialized profile "${name}" → ${target}`);
  }

  return { name };
}

async function ensureActiveProfile(): Promise<ActiveProfile | null> {
  if (activeProfile) return activeProfile;
  // Only a resolved profile is cached; the target URL arrives over IPC shortly
  // after startup, and caching "unresolved" would persist until something
  // happened to invalidate it.
  activeProfile = await resolveActiveProfile();
  return activeProfile;
}

function invalidateActiveProfile(): void {
  activeProfile = null;
  externalDaemonObserved = false;
  pinnedDaemonObserved = false;
  recoveryPolicy.reset();
}

function setDesiredDaemonRunning(desired: boolean, explicit = false): void {
  if (desired && desiredRunningBeforeUpdateStop !== null) return;
  if (desired && explicit) autoStartSuppressedAfterQuit = false;
  if (desiredDaemonRunning === desired && !explicit) return;
  desiredDaemonRunning = desired;
  recoveryPolicy.reset();
}

function observeDaemonBoundary(status: DaemonStatus): void {
  if (status.pinnedDaemon) { externalDaemonObserved = true; recoveryPolicy.reset(); return; }
  if (status.state !== "running") return;
  externalDaemonObserved = status.externallyManaged === true;
  if (externalDaemonObserved) {
    recoveryPolicy.reset();
  }
}

async function fetchHealth(): Promise<DaemonStatus> {
  // While the CLI is being downloaded or has permanently failed, short-circuit
  // polling — there's nothing to probe yet and /health calls would just return
  // "stopped", which would overwrite the correct setup state in the UI.
  if ((currentState === "installing_cli" || currentState === "cli_not_found") &&
      (!targetApiBaseUrl || !(await pinnedBoundary({ name: deriveProfileName(targetApiBaseUrl) })))) {
    return { state: currentState };
  }

  const active = await ensureActiveProfile();
  // No profile yet means no daemon of ours to probe. Reporting "stopped" is the
  // honest answer; probing the default port would surface the user's own CLI
  // daemon as if it were Desktop's.
  if (!active) return { state: "stopped" };
  const data = await fetchProfileHealth(active);
  const pinned = await pinnedBoundary(active, data);

  if (!data || data.status !== "running") {
    if (pinned && data?.status !== "starting") {
      return { state: "stopped", profile: active.name, externallyManaged: true, pinnedDaemon: true };
    }
    // A start that never reaches "running" is the symptom; an expired/invalid
    // login is the most common cause and the one with no other signal (the
    // daemon exits before it can serve /health, so we can't read the reason
    // from it). Probe the token once per attempt, after a grace period, to
    // surface a re-login prompt instead of spinning on "starting" forever.
    if (
      currentState === "starting" &&
      !authExpired &&
      !authProbeDone &&
      startingSince !== null &&
      Date.now() - startingSince >= AUTH_PROBE_GRACE_MS
    ) {
      authProbeDone = true;
      if ((await probeTokenValidity(active.name)) === "auth_expired") {
        authExpired = true;
      }
    }
    // Sticky: once login is known-expired, keep reporting it (even after
    // currentState flips away from "starting") until the next start attempt or
    // a successful /health clears the flag.
    if (authExpired) {
      return { state: "auth_expired", profile: active.name };
    }
    // The daemon binds /health before preflight finishes and self-reports
    // "starting" until it's ready. Trust that over our own currentState, so a
    // daemon booting on its own — or started via the CLI — surfaces as
    // "starting" instead of "stopped".
    if (data?.status === "starting") {
      return {
        state: "starting",
        profile: active.name,
        externallyManaged: pinned,
        pinnedDaemon: pinned,
        incompatibleDaemon: pinned && data.desktop_protocol !== 1,
      };
    }
    return {
      state:
        currentState === "starting"
          ? "starting"
          : recoveryPolicy.isPaused
            ? "recovery_paused"
            : "stopped",
      profile: active.name,
      externallyManaged: pinned,
      pinnedDaemon: pinned,
    };
  }

  // A live, authenticated daemon clears any prior auth-failure verdict so the
  // re-login prompt disappears once the user reconnects.
  authExpired = false;
  startingSince = null;

  // A running daemon whose OS differs from this host's is one we can't drive
  // via the native lifecycle CLI (e.g. Linux-in-WSL2 behind a Windows desktop,
  // reachable only over localhost forwarding). Surface it so the UI disables
  // the auto-start/auto-stop toggles instead of letting them silently no-op,
  // and so before-quit skips a stop that would never land. See #3916.
  const externallyManaged = pinned || isDaemonExternallyManaged(
    data.os,
    normalizeHostOS(process.platform),
  );

  // Safety: if we have a target URL and the daemon on our port reports a
  // different server_url, it's not "our" daemon — drop it and re-resolve.
  if (
    targetApiBaseUrl &&
    data.server_url &&
    !urlsMatch(data.server_url, targetApiBaseUrl)
  ) {
    if (!pinned) invalidateActiveProfile();
    return { state: "stopped", externallyManaged: pinned, pinnedDaemon: pinned };
  }

  return {
    state: "running",
    pid: data.pid,
    uptime: data.uptime,
    daemonId: data.daemon_id,
    deviceName: data.device_name,
    agents: data.agents ?? [],
    workspaceCount: Array.isArray(data.workspaces)
      ? data.workspaces.length
      : 0,
    profile: active.name,
    serverUrl: data.server_url,
    externallyManaged,
    pinnedDaemon: pinned,
    incompatibleDaemon: pinned && data.desktop_protocol !== 1,
  };
}

function findCliOnPath(): string | null {
  const candidates = process.platform === "win32" ? ["multica.exe"] : ["multica"];
  const paths = (process.env["PATH"] ?? "").split(
    process.platform === "win32" ? ";" : ":",
  );
  if (process.platform === "darwin") {
    paths.push("/opt/homebrew/bin", "/usr/local/bin");
  }
  for (const name of candidates) {
    for (const dir of paths) {
      const full = join(dir, name);
      if (existsSync(full)) return full;
    }
  }
  return null;
}

/**
 * Returns the path to the CLI binary bundled inside the Desktop app.
 *
 * - Dev (`electron-vite dev`): `app.getAppPath()` → `apps/desktop`, resolving
 *   to `apps/desktop/resources/bin/multica`. `bundle-cli.mjs` populates this
 *   before dev starts, so iterating on Go changes is "make build → restart".
 * - Packaged: `app.getAppPath()` → `<Multica.app>/Contents/Resources/app.asar`.
 *   electron-builder's `asarUnpack: resources/**` extracts the binary to
 *   `app.asar.unpacked/`, so we swap the path segment to execute it.
 */
function bundledCliPath(): string {
  const binName = process.platform === "win32" ? "multica.exe" : "multica";
  return join(app.getAppPath(), "resources", "bin", binName).replace(
    "app.asar",
    "app.asar.unpacked",
  );
}

async function probeCliBinary(
  bin: string,
  source: "bundled" | "managed" | "path",
): Promise<string | null> {
  try {
    const stdout = await new Promise<string>((resolve, reject) => {
      execFile(
        bin,
        ["version", "--output", "json"],
        { timeout: 5_000 },
        (err, out) => {
          if (err) reject(err);
          else resolve(out);
        },
      );
    });
    const parsed = JSON.parse(stdout) as { version?: string };
    if (typeof parsed.version === "string" && parsed.version.length > 0) {
      return parsed.version;
    }
    console.warn(
      `[daemon] ignoring ${source} CLI at ${bin}: version output was missing or invalid`,
    );
    return null;
  } catch (err) {
    console.warn(`[daemon] ignoring ${source} CLI at ${bin}:`, err);
    return null;
  }
}

/**
 * Returns a usable `multica` binary path. Priority:
 *   1. Cached result from a previous successful resolve.
 *   2. Bundled binary shipped with the Desktop app (`bundle-cli.mjs`).
 *   3. Managed binary already installed in userData (`managedCliPath`).
 *   4. Download + install latest release into userData.
 *   5. `multica` on PATH (dev convenience / user-installed via brew).
 * Returns `null` only when all of the above fail.
 *
 * Bundled is preferred so Desktop iterates in lockstep with Go changes in
 * the same repo — avoids the 404 / stale-API problem when the Desktop's
 * TS side is ahead of the last published CLI release.
 *
 * This function is idempotent and safe to call concurrently — in-flight
 * installs are de-duplicated via `cliResolvePromise`.
 */
async function resolveCliBinary(): Promise<string | null> {
  if (quitting) return cachedCliBinary ?? null;
  if (cachedCliBinary !== undefined) return cachedCliBinary;
  if (cliResolvePromise) return cliResolvePromise;

  cliResolvePromise = (async () => {
    const bundled = bundledCliPath();
    if (existsSync(bundled)) {
      const version = await probeCliBinary(bundled, "bundled");
      if (version) {
        console.log(`[daemon] using bundled CLI at ${bundled}`);
        cachedCliBinary = bundled;
        cachedCliBinaryVersion = version;
        return bundled;
      }
    }

    // A Taiwan release must use the CLI embedded in that same App bundle.
    if (app.isPackaged && /-zh-tw\.\d+$/.test(app.getVersion())) {
      cachedCliBinary = null;
      cachedCliBinaryVersion = null;
      return null;
    }

    const managed = managedCliPath();
    if (existsSync(managed)) {
      const version = await probeCliBinary(managed, "managed");
      if (version) {
        cachedCliBinary = managed;
        cachedCliBinaryVersion = version;
        return managed;
      }
    }

    try {
      const installed = await ensureManagedCli({
        forceInstall: existsSync(managed),
      });
      const version = await probeCliBinary(installed, "managed");
      if (version) {
        cachedCliBinary = installed;
        cachedCliBinaryVersion = version;
        return installed;
      }
      console.warn(
        `[daemon] managed CLI at ${installed} failed validation after install`,
      );
    } catch (err) {
      console.warn("[daemon] CLI auto-install failed, falling back to PATH:", err);
    }

    const onPath = findCliOnPath();
    if (onPath) {
      const version = await probeCliBinary(onPath, "path");
      if (version) {
        cachedCliBinary = onPath;
        cachedCliBinaryVersion = version;
        return onPath;
      }
    }

    cachedCliBinary = null;
    cachedCliBinaryVersion = null;
    return null;
  })();

  try {
    return await cliResolvePromise;
  } finally {
    cliResolvePromise = null;
  }
}

/**
 * Reads the version of the currently resolved CLI binary. Cached for the
 * process lifetime — the bundled binary doesn't change after bundle time.
 * Returns null on any failure (unknown `go` at bundle time, broken binary,
 * wrong-arch bundled binary, etc.) so callers can fail open.
 */
async function getCliBinaryVersion(): Promise<string | null> {
  if (cachedCliBinaryVersion !== undefined) return cachedCliBinaryVersion;
  const bin = await resolveCliBinary();
  if (!bin) {
    cachedCliBinaryVersion = null;
    return null;
  }
  cachedCliBinaryVersion = await probeCliBinary(bin, "path");
  return cachedCliBinaryVersion;
}

/**
 * Compares the running daemon's `cli_version` against the CLI binary we
 * would use to spawn a new one, and restarts only when safe. The decision
 * logic itself is in `version-decision.ts` (pure, unit-tested); this
 * wrapper handles the async plumbing and side effects.
 *
 * Restart is only fired when ALL of:
 *   - a daemon is actually running on the active profile's port
 *   - both sides report a version and the strings differ
 *   - `active_task_count` is 0 (no in-flight agent work would be killed)
 *
 * On a confirmed mismatch while the daemon is busy, `pendingVersionRestart`
 * is set; the poll loop retries this function on each 5s tick and will fire
 * the restart as soon as the daemon drains.
 */
async function ensureRunningDaemonVersionMatches(): Promise<
  "restarted" | "deferred" | "ok" | "not_running"
> {
  if (quitting || autoStartSuppressedAfterQuit) return "deferred";
  const active = await ensureActiveProfile();
  if (!active) return "not_running";
  const running = await fetchProfileHealth(active);

  if (app.isPackaged && /-zh-tw\.\d+$/.test(app.getVersion())) {
    pendingVersionRestart = false;
    return running ? "ok" : "not_running";
  }

  // Don't try to version-match a daemon we can't restart (e.g. WSL2). Treat it
  // as up-to-date — restartDaemon would no-op anyway, and skipping here avoids
  // a misleading "restarting daemon" log on every auto-start. #3916.
  if (await pinnedBoundary(active, running) || isDaemonExternallyManaged(running?.os, normalizeHostOS(process.platform))) {
    pendingVersionRestart = false;
    return "ok";
  }

  const bundled = await getCliBinaryVersion();
  if (
    quitting ||
    autoStartSuppressedAfterQuit ||
    desiredRunningBeforeUpdateStop !== null
  ) {
    pendingVersionRestart = true;
    return "deferred";
  }
  const action = decideVersionAction(bundled, running);

  switch (action) {
    case "not_running":
      pendingVersionRestart = false;
      return "not_running";
    case "ok":
      pendingVersionRestart = false;
      return "ok";
    case "defer": {
      if (!pendingVersionRestart) {
        const activeTasks = running?.active_task_count ?? 0;
        console.log(
          `[daemon] CLI version mismatch (bundled=${bundled} running=${running?.cli_version}); deferring restart until ${activeTasks} active task(s) finish`,
        );
      }
      pendingVersionRestart = true;
      return "deferred";
    }
    case "restart":
      console.log(
        `[daemon] CLI version mismatch (bundled=${bundled} running=${running?.cli_version}) — restarting daemon`,
      );
      pendingVersionRestart = false;
      await restartDaemon();
      return "restarted";
  }
}

/**
 * Exchange the user's session credential for a local PAT via POST /api/tokens. The
 * daemon needs a PAT (or `mul_` / `mdt_` token) because JWTs expire in 30
 * days and signatures are tied to a specific backend instance.
 */
const mintedDesktopTokenSchema = z.object({
  id: z.string().min(1),
  token: z.string().regex(/^mul_.+/),
});

async function mintPat(jwt: string): Promise<MintedDesktopToken> {
  if (!targetApiBaseUrl) {
    throw new Error("mint PAT: target API URL not set");
  }
  const url = `${targetApiBaseUrl.replace(/\/+$/, "")}/api/tokens`;
  const res = await fetch(url, {
    method: "POST",
    signal: AbortSignal.timeout(10_000),
    headers: {
      "Content-Type": "application/json",
      Authorization: `Bearer ${jwt}`,
    },
    // Omit expires_in_days → server treats as null → non-expiring PAT.
    body: JSON.stringify({ name: `Multica Desktop (${hostname()})` }),
  });
  if (!res.ok) {
    // Attach the status so callers can tell a genuine auth rejection (401 — the
    // session token is dead) apart from a transient failure (5xx, etc.) without
    // string-matching the message.
    throw Object.assign(
      new Error(`mint PAT failed: HTTP ${res.status}`),
      { status: res.status },
    );
  }
  // parseWithFallback logs rejected inputs; never pass a secret-bearing
  // malformed response to that logger.
  const parsed = mintedDesktopTokenSchema.safeParse(await res.json());
  const data = parseWithFallback<MintedDesktopToken | null>(
    parsed.success ? parsed.data : null, mintedDesktopTokenSchema, null,
    { endpoint: "POST /api/tokens (Desktop)" },
  );
  if (!data) throw new Error("mint PAT: response missing token identity");
  return data;
}

async function tokenOwner(userId: string): Promise<DesktopTokenOwner> {
  if (!targetApiBaseUrl) throw new Error("Desktop API URL is unresolved");
  return {
    machine: await desktopTokenMachine(),
    userId,
    serverUrl: targetApiBaseUrl.replace(/\/+$/, ""),
  };
}

async function readTokenRecord(profile: string): Promise<unknown> {
  try {
    return JSON.parse(await readFile(join(profileDir(profile), ".desktop-token.json"), "utf8"));
  } catch (error) {
    if (error instanceof SyntaxError || (error as NodeJS.ErrnoException).code === "ENOENT") return null;
    throw error;
  }
}

async function saveDesktopToken(
  active: ActiveProfile,
  minted: MintedDesktopToken,
  owner: DesktopTokenOwner,
): Promise<void> {
  await assertProfileWritable(active);
  const config = await readProfileConfig(active.name);
  config.token = minted.token;
  config.server_url = owner.serverUrl;
  await writeProfileConfig(active.name, config);
  await writeProfileUserId(active.name, owner.userId);
  // A partial write leaves no matching provenance, so the next login mints
  // again without ever claiming or revoking the previous credential.
  await assertProfileWritable(active);
  await writeFile(join(profileDir(active.name), ".desktop-token.json"),
    JSON.stringify(desktopTokenRecord(minted, owner)), { mode: 0o600 });
}

/** Reuse only a verified local mint; legacy and pasted PATs authorize a new mint. */
async function syncToken(
  tokenFromRenderer: string,
  userId: string,
  forceMint = false,
): Promise<{ active: ActiveProfile; userChanged: boolean }> {
  const active = await ensureActiveProfile();
  if (!active) throw new Error("daemon profile is not resolved yet; token sync skipped");
  // Login remains usable without claiming the independent daemon credentials.
  if (await pinnedBoundary(active)) return { active, userChanged: false };
  const config = await readProfileConfig(active.name);
  const previousUserId = await readProfileUserId(active.name);
  const owner = await tokenOwner(userId);
  const minted = await selectDesktopToken(
    config.token,
    forceMint ? null : await readTokenRecord(active.name),
    owner,
    () => probeTokenValidity(active.name),
    () => mintPat(tokenFromRenderer),
  );
  await saveDesktopToken(active, minted, owner);
  // A running daemon must reload replacements after migration or revocation
  // as well as account switches; the IPC caller already gates that restart.
  const userChanged = (Boolean(previousUserId) && previousUserId !== userId) ||
    config.token !== minted.token;
  return { active, userChanged };
}

async function restartDaemonAfterUserSwitch(
  active: ActiveProfile,
): Promise<void> {
  // If we just rotated credentials onto a running daemon, restart it so the
  // in-memory token in the Go process matches the new config.
  const existing = await fetchProfileHealth(active);
  if (daemonStatusAlive(existing?.status)) {
    // Restart whether it's "running" or still "starting" — a booting daemon
    // already loaded the old token at startup, so it must be restarted to
    // pick up the rotated credentials.
    console.log(
      "[daemon] credentials changed — restarting daemon with new credentials",
    );
    // Credential rotation is a one-shot login intent, not poll-driven
    // maintenance: wait for bootstrap/recovery instead of dropping it.
    const restarted = await restartDaemon();
    if (!restarted.success) {
      console.warn(
        `[daemon] restart-on-user-switch failed: ${restarted.error ?? "unknown error"}`,
      );
    }
  }
}

async function loadPrefs(): Promise<DaemonPrefs> {
  try {
    const raw = await readFile(PREFS_PATH, "utf-8");
    const parsed = JSON.parse(raw);
    return { ...DEFAULT_PREFS, ...parsed };
  } catch {
    return { ...DEFAULT_PREFS };
  }
}

async function savePrefs(prefs: DaemonPrefs): Promise<void> {
  const dir = join(homedir(), ".multica");
  await mkdir(dir, { recursive: true });
  await writeFile(PREFS_PATH, JSON.stringify(prefs, null, 2), "utf-8");
}

async function clearToken(): Promise<void> {
  const active = await ensureActiveProfile();
  // Nothing of ours to clear yet, and the default CLI profile is not ours to
  // strip a token from.
  if (!active || await pinnedBoundary(active)) return;
  const config = await readProfileConfig(active.name);
  if ("token" in config) {
    delete config.token;
    await writeProfileConfig(active.name, config);
  }
  // Always drop the sidecar so a subsequent syncToken from any user is
  // treated as a fresh mint, not a reuse of a stale cached PAT.
  await removeProfileUserId(active.name);
}

// Result of a user-initiated daemon re-authentication. The distinction matters:
// only `session_invalid` justifies signing the user out of the whole app; a
// `transient` failure must keep them logged in so they can retry.
export type ReauthResult =
  | { ok: true }
  | { ok: false; reason: "session_invalid" }
  | { ok: false; reason: "transient"; message: string };

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

/**
 * Recover the local daemon from the "auth_expired" state. Replaces the stale
 * cached PAT after minting from the current session token, and restarts
 * the daemon so it loads the new credential.
 *
 * Failures are classified rather than collapsed: a 401 from the mint means the
 * session token itself is dead (`session_invalid` → the renderer drives a full
 * re-login); anything else — mint 5xx, a network blip, a config write error, a
 * restart hiccup — is `transient`, leaving the user signed in so they can retry.
 * This mirrors the conservative classification the startup probe already uses.
 */
// Only an exact locally recorded mint can be selected for rotation.
async function getDesktopTokenId(_jwt: string, userId: string): Promise<string | null> {
  const active = await ensureActiveProfile();
  if (!active || await pinnedBoundary(active) || await readProfileUserId(active.name) !== userId) return null;
  const cfg = await readProfileConfig(active.name);
  return ownedDesktopTokenId(await readTokenRecord(active.name), cfg.token, await tokenOwner(userId));
}

async function rotateDesktopToken(jwt: string, userId: string, id: string): Promise<{ ok: boolean; message?: string }> {
  let saved = false;
  try {
    if (await getDesktopTokenId(jwt, userId) !== id) throw new Error("The selected token is no longer this App's Desktop token. Refresh and try again.");
    const active = await ensureActiveProfile();
    if (!active) throw new Error("Desktop profile unavailable.");
    await assertProfileWritable(active);
    const status = await fetchHealth();
    if (status.externallyManaged || !["running", "stopped", "auth_expired"].includes(status.state))
      throw new Error("Daemon is busy or externally managed. Try again when it is idle.");
    const health = await fetchProfileHealth(active);
    if (!health && existsSync(profilePidPath(active.name)))
      throw new Error("Cannot confirm daemon task state. No token was changed.");
    if (health && (health.status !== "running" || !health.pid || health.active_task_count !== 0))
      throw new Error("Wait for running tasks to finish before replacing the Desktop token.");
    const replacement = await mintPat(jwt);
    await saveDesktopToken(active, replacement, await tokenOwner(userId));
    saved = true;
    setDesiredDaemonRunning(true, true);
    const result = await restartDaemon();
    if (!result.success) throw new Error("New token saved, but daemon restart failed. The old token has not been revoked; retry from daemon settings.");
    let ready = false;
    for (let attempt = 0; attempt < 30; attempt++) {
      const current = await fetchProfileHealth(active);
      if (current?.status === "running" && current.pid && current.server_url && urlsMatch(current.server_url, targetApiBaseUrl!) && (!health?.pid || current.pid !== health.pid)) { ready = true; break; }
      await new Promise(resolve => setTimeout(resolve, 1000));
    }
    if (!ready) throw new Error("New token saved, but reconnect is not confirmed. The old token has not been revoked; check daemon settings.");
    const revoked = await fetch(`${targetApiBaseUrl!.replace(/\/+$/, "")}/api/tokens/${encodeURIComponent(id)}`, {
      method: "DELETE", headers: { Authorization: `Bearer ${jwt}` }, signal: AbortSignal.timeout(10_000),
    });
    if (!revoked.ok && revoked.status !== 404) throw new Error("Daemon reconnected with the new token, but revoking the old token failed. Delete the old token from the token list.");
    return { ok: true };
  } catch (error) {
    return { ok: false, message: `${saved ? "" : "Old token retained. "}${errorMessage(error)}` };
  }
}

async function reauthenticate(
  token: string,
  userId: string,
): Promise<ReauthResult> {
  try {
    const active = await ensureActiveProfile();
    if (!active) throw new Error("Desktop profile unavailable.");
    await assertProfileWritable(active);
    // Keep the previous credential recoverable until the mint succeeds.
    await syncToken(token, userId, true);
  } catch (err) {
    if (isAuthStatusError(err)) return { ok: false, reason: "session_invalid" };
    return { ok: false, reason: "transient", message: errorMessage(err) };
  }
  const restart = await restartDaemon();
  if (!restart.success) {
    return {
      ok: false,
      reason: "transient",
      message: restart.error ?? "failed to restart daemon",
    };
  }
  return { ok: true };
}

function successfulRuntimeProbe(
  providers: string[],
  daemonRunning: boolean,
): Extract<LocalRuntimeProbe, { probeResult: "success" }> {
  const providerSummary: Record<string, number> = {};
  for (const rawProvider of providers) {
    const provider = rawProvider.trim().toLowerCase();
    if (!/^[a-z0-9][a-z0-9_-]{0,63}$/.test(provider)) continue;
    providerSummary[provider] = (providerSummary[provider] ?? 0) + 1;
  }
  const runtimeCount = Object.values(providerSummary).reduce(
    (sum, count) => sum + count,
    0,
  );
  return {
    probeResult: "success",
    runtimeCount,
    providerSummary,
    onlineCount: daemonRunning ? runtimeCount : 0,
    offlineCount: daemonRunning ? 0 : runtimeCount,
  };
}

async function probeLocalRuntimes(): Promise<LocalRuntimeProbe> {
  const health = await fetchHealth();
  if (health.state === "running") {
    return successfulRuntimeProbe(health.agents ?? [], true);
  }

  const active = await ensureActiveProfile();
  if (!active || await pinnedBoundary(active)) return { probeResult: "error" };
  const bin = await resolveCliBinary();
  if (!bin) return { probeResult: "error" };
  return new Promise((resolve) => {
    execFile(
      bin,
      ["daemon", "probe-runtimes", ...profileArgs(active.name)],
      { timeout: 15_000, env: desktopSpawnEnv(), maxBuffer: 64 * 1024 },
      (error, stdout) => {
        if (error) {
          resolve({ probeResult: "error" });
          return;
        }
        try {
          const parsed = JSON.parse(stdout) as {
            probe_result?: unknown;
            runtime_count?: unknown;
            provider_summary?: unknown;
          };
          if (
            parsed.probe_result !== "success" ||
            typeof parsed.runtime_count !== "number" ||
            !parsed.provider_summary ||
            typeof parsed.provider_summary !== "object" ||
            Array.isArray(parsed.provider_summary)
          ) {
            resolve({ probeResult: "error" });
            return;
          }
          const providers: string[] = [];
          for (const [provider, count] of Object.entries(
            parsed.provider_summary as Record<string, unknown>,
          )) {
            if (
              !Number.isInteger(count) ||
              (count as number) < 0 ||
              (count as number) > 1000
            ) {
              resolve({ probeResult: "error" });
              return;
            }
            providers.push(...Array<string>(count as number).fill(provider));
          }
          const probe = successfulRuntimeProbe(providers, false);
          resolve(
            probe.runtimeCount === parsed.runtime_count
              ? probe
              : { probeResult: "error" },
          );
        } catch {
          resolve({ probeResult: "error" });
        }
      },
    );
  });
}

// Env passed to every CLI child so the daemon process knows it was spawned
// by the Desktop app. The server uses this to mark runtimes as managed and
// hide CLI self-update UI. Computed lazily so it picks up the PATH fix
// applied by fix-path in main/index.ts — as a top-level const it would
// snapshot process.env at import time, before that block runs.
function desktopSpawnEnv(): NodeJS.ProcessEnv {
  return { ...daemonSpawnEnv(process.env), MULTICA_ZH_TW_DESKTOP_SELF_RELOAD: "1" };
}

function scheduleStatusRefresh(): void {
  setTimeout(() => void pollOnce(), 0);
}

// Safe before setup: pending trust is retained until the Desktop profile exists.
export function requestDaemonTrustRefresh(): void {
  pendingTrustRefresh = ++trustRefreshGeneration;
  trustRestartPID = null;
  scheduleStatusRefresh();
}

async function refreshDaemonTrustWhenIdle(): Promise<void> {
  const generation = pendingTrustRefresh;
  if (!generation) return;
  const active = await ensureActiveProfile();
  if (!active) return;
  const probed = await fetchProfileHealthWithPort(active);
  const running = probed?.health ?? null;
  if (await pinnedBoundary(active, running)) { pendingTrustRefresh = 0; return; }
  if (generation !== pendingTrustRefresh) return;
  if (!running) {
    if (await daemonPidIsConfirmedAbsent(active.name) && generation === pendingTrustRefresh) {
      pendingTrustRefresh = 0;
      trustRestartPID = null;
    }
    return;
  }
  if (running.status !== "running" || running.os !== normalizeHostOS(process.platform) ||
      running.profile !== active.name || running.launched_by !== "desktop" ||
      !urlsMatch(running.server_url ?? "", targetApiBaseUrl ?? "") ||
      !Number.isSafeInteger(running.pid) || !running.pid || running.pid < 1) return;
  if (trustRestartPID !== null && running.pid !== trustRestartPID) {
    // A different PID may also be an unrelated restart; confirm loaded digest.
    trustRestartPID = null;
  }
  if (trustRestartPID !== null && Date.now() - trustRestartRequestedAt < 30_000) return;
  if (running.active_task_count !== 0 ||
      running.running_task_count !== 0 || running.resource_wait_task_count !== 0) return;
  const ca = process.env.MULTICA_CA_CERT_FILE;
  if (!ca || await pinnedBoundary(active)) return;
  trustRestartPID = running.pid;
  trustRestartRequestedAt = Date.now();
  try {
    const digest = createHash("sha256").update(await readFile(ca)).digest("hex");
    const response = await fetch(`http://127.0.0.1:${probed!.port}/restart/idle`, {
      method: "POST", signal: AbortSignal.timeout(HEALTH_PROBE_TIMEOUT_MS),
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ expected_pid: running.pid, expected_ca_path: ca, expected_ca_sha256: digest }),
    });
    if (response.status === 200 && generation === pendingTrustRefresh) pendingTrustRefresh = 0;
    if (response.status !== 202 && generation === pendingTrustRefresh) trustRestartPID = null;
    // Busy, mismatched paths, and older daemons stay pending; never fall back
    // to the CLI stop command, which can force-kill an active daemon.
  } catch {
    // Shutdown can close the response connection. Check for a new PID before
    // retrying so a successful handoff is not mistaken for a failed request.
  }
}

async function startDaemon(
  recoveryProfile?: ActiveProfile,
): Promise<{ success: boolean; error?: string }> {
  if (desiredRunningBeforeUpdateStop !== null) return UPDATE_IN_PROGRESS;
  if (quitting) return { success: false, error: "Application is quitting" };
  const active = await ensureActiveProfile();
  if (!active) return { success: false, error: "Waiting for the service address" };
  if (await pinnedBoundary(active)) return { success: false, error: "This daemon is managed independently." };
  const bin = await resolveCliBinary();
  if (!bin) return { success: false, error: "multica CLI is not installed" };
  if (app.isPackaged && /-zh-tw\.\d+$/.test(app.getVersion())) {
    try { await verifyCLIEntries(bin, bin, managedCliPath(), process.env.PATH ?? "", process.platform); }
    catch (error) { return { success: false, error: error instanceof Error ? error.message : String(error) }; }
  }
  if (
    recoveryProfile &&
    !recoveryStartAllowed({
      desiredRunning: desiredDaemonRunning,
      externalDaemonObserved,
      expected: recoveryProfile,
      current: active,
    })
  ) {
    return { success: false, error: "Daemon recovery was superseded" };
  }
  const existing = await fetchProfileHealth(active);
  if (daemonStatusAlive(existing?.status)) {
    // A daemon is already up ("running") or booting ("starting") on this port —
    // don't spawn a second one (the CLI rejects that as "already running").
    // Let polling track it through to "running".
    externalDaemonObserved = isDaemonExternallyManaged(
      existing?.os,
      normalizeHostOS(process.platform),
    );
    scheduleStatusRefresh();
    return { success: true };
  }
  if (
    recoveryProfile &&
    !recoveryStartAllowed({
      desiredRunning: desiredDaemonRunning,
      externalDaemonObserved,
      expected: recoveryProfile,
      current: active,
    })
  ) {
    return { success: false, error: "Daemon recovery was superseded" };
  }

  if (desiredRunningBeforeUpdateStop !== null) return UPDATE_IN_PROGRESS;
  if (quitting) return { success: false, error: "Application is quitting" };
  if (await pinnedBoundary(active, existing)) return { success: false, error: "This daemon is managed independently." };
  if (recoveryProfile) recoveryPolicy.recordRecoveryAttempt(Date.now());
  currentState = "starting";
  // Begin a fresh auth-probe window for this attempt.
  startingSince = Date.now();
  authProbeDone = false;
  authExpired = false;
  sendStatus({ state: "starting" });

  const args = ["daemon", "start", ...profileArgs(active.name)];

  return new Promise((resolve) => {
    execFile(
      bin,
      args,
      { timeout: DAEMON_START_EXEC_TIMEOUT_MS, env: desktopSpawnEnv() },
      (err) => {
        if (err) {
          currentState = "stopped";
          sendStatus({ state: "stopped" });
          resolve({ success: false, error: err.message });
          return;
        }
        // Stay in "starting" until pollOnce confirms /health — the CLI
        // returning 0 only means the supervisor was spawned, not that the
        // daemon process is already listening.
        scheduleStatusRefresh();
        resolve({ success: true });
      },
    );
  });
}

/**
 * Fresh boundary preflight for stop/restart: read the active profile's CURRENT
 * /health and decide whether the daemon runs somewhere the app can't drive
 * (WSL2 etc.). Done per call rather than off the poll cache, so a lifecycle op
 * never shells out to a CLI that can't reach the daemon's process — even on
 * paths that didn't just poll (e.g. restart-on-user-switch in syncToken, which
 * calls restartDaemon directly). See #3916.
 */
async function lifecycleBlockedByForeignDaemon(): Promise<boolean> {
  const active = await ensureActiveProfile();
  if (!active) return false;
  const health = await fetchProfileHealth(active);
  return await pinnedBoundary(active, health) || daemonLifecycleUnreachable(
    async () => health?.os,
    normalizeHostOS(process.platform),
  );
}

async function stopDaemon(
  options: { force?: boolean; signal?: AbortSignal } = {},
): Promise<{ success: boolean; error?: string }> {
  const { signal } = options;
  if (signal?.aborted) return { success: true };
  // Central lifecycle guard: a daemon running in an environment we can't drive
  // (e.g. Linux in WSL2 behind a Windows desktop) can't be stopped by the
  // native CLI — it would act on the host process namespace and no-op, while
  // still flipping our state to "stopped". Bail as a successful no-op so every
  // caller (logout, quit, restart, the Runtime card) is covered in one place
  // rather than each remembering to check. Preflighted against live /health so
  // it holds even when no poll ran first. #3916.
  if (await lifecycleBlockedByForeignDaemon()) return { success: true };

  const active = await ensureActiveProfile();
  if (!active) return { success: true };
  // A queued update stop can expire while resolving the CLI/profile/preflight.
  if (signal?.aborted) return { success: true };
  if (await pinnedBoundary(active)) return { success: true };
  const bin = await resolveCliBinary();
  if (!bin) return { success: false, error: "multica CLI is not installed" };
  currentState = "stopping";
  // An explicit stop is a clean reset — drop any pending auth-failure verdict.
  authExpired = false;
  startingSince = null;
  sendStatus({ state: "stopping" });

  const args = ["daemon", "stop", ...(options.force ? ["--force"] : []), ...profileArgs(active.name)];

  return new Promise((resolve) => {
    // The CLI's forced stop includes the existing 40s shutdown budget.
    execFile(bin, args, { timeout: options.force ? 55_000 : 15_000 }, (err) => {
      if (err) {
        resolve({ success: false, error: err.message });
      } else {
        resolve({ success: true });
      }
      currentState = "stopped";
      sendStatus({ state: "stopped" });
    });
  });
}

async function restartDaemon(): Promise<{ success: boolean; error?: string }> {
  if (desiredRunningBeforeUpdateStop !== null) return UPDATE_IN_PROGRESS;
  if (quitting) return { success: false, error: "Application is quitting" };
  // Same central, live-preflighted guard as stopDaemon: we can neither stop nor
  // start a daemon we don't manage, so don't try (user-switch, reauth,
  // first-workspace, and any future restart caller all route through here).
  // #3916.
  if (await lifecycleBlockedByForeignDaemon()) return { success: true };
  const stopResult = await stopDaemon();
  if (!stopResult.success) return stopResult;
  return startDaemon();
}

async function daemonPidIsConfirmedAbsent(profile: string): Promise<boolean> {
  try {
    const raw = await readFile(profilePidPath(profile), "utf-8");
    const pid = parseDaemonPid(raw);
    if (pid === null) {
      console.warn(
        `[daemon] recovery deferred: ${profilePidPath(profile)} is invalid`,
      );
      return false;
    }
    return !daemonProcessExists(pid);
  } catch (err) {
    if (
      err &&
      typeof err === "object" &&
      "code" in err &&
      err.code === "ENOENT"
    ) {
      return true;
    }
    console.warn("[daemon] recovery deferred: unable to read daemon PID:", err);
    return false;
  }
}

async function daemonProcessIsConfirmedAbsent(profile: string): Promise<boolean> {
  const record = await readDaemonPortRecord(profile);
  return await daemonPidIsConfirmedAbsent(profile)
    && !(record && daemonProcessExists(record.pid));
}

async function attemptDaemonRecovery(active: ActiveProfile): Promise<void> {
  const startAllowed = () =>
    recoveryStartAllowed({
      desiredRunning: desiredDaemonRunning,
      externalDaemonObserved,
      expected: active,
      current: activeProfile,
    });
  const outcome = await runDaemonRecoveryAttempt({
    startAllowed,
    // A normal UI poll intentionally gives up after 2s. Before treating that
    // as process death, use an independent longer probe so a busy daemon is
    // not restarted merely because one health request was slow.
    confirmAlive: async () => {
      const health = await fetchProfileHealth(active, RECOVERY_HEALTH_PROBE_TIMEOUT_MS);
      if (!daemonStatusAlive(health?.status)) return false;
      externalDaemonObserved = await pinnedBoundary(active, health) || isDaemonExternallyManaged(
        health?.os,
        normalizeHostOS(process.platform),
      );
      recoveryPolicy.observe({
        desiredRunning: desiredDaemonRunning,
        externalDaemonObserved,
        lifecycleBusy: lifecycleOperations.inProgress,
        state: health?.status === "running" ? "running" : "starting",
        now: Date.now(),
      });
      scheduleStatusRefresh();
      return true;
    },
    pidConfirmedAbsent: () => daemonPidIsConfirmedAbsent(active.name),
    recordPidDeferral: () => recoveryPolicy.recordPidDeferral(Date.now()),
    recordPidAbsent: () => recoveryPolicy.recordPidAbsent(),
    // Force-kill leaves daemon.pid behind, and Windows can reuse the number
    // for another process. After three spaced deferrals, let the CLI recheck
    // health and attempt the start; an occupied port then fails safely into
    // the normal retry backoff and absolute budget.
    onPidFallback: () =>
      console.warn(
        "[daemon] daemon PID stayed unverifiable; proceeding through CLI safety checks",
      ),
    start: () => {
      console.warn("[daemon] managed daemon disappeared; attempting recovery");
      return startDaemon(active);
    },
    desiredRunning: () => desiredDaemonRunning,
    stop: () => stopDaemon(),
  });

  if (outcome.kind === "alive") {
    console.log("[daemon] recovery cancelled: longer health probe succeeded");
  } else if (outcome.kind === "pid_deferred") {
    console.warn(
      "[daemon] recovery deferred: daemon PID is still alive or could not be verified",
    );
  } else if (outcome.kind === "start_failed") {
    console.warn(
      `[daemon] recovery failed: ${outcome.error ?? "unknown error"}`,
    );
  }
}

function recoveryDecision(status: DaemonStatus) {
  return recoveryPolicy.observe({
    desiredRunning: desiredDaemonRunning,
    externalDaemonObserved,
    lifecycleBusy: lifecycleOperations.inProgress,
    state: status.state,
    now: Date.now(),
  });
}

async function pollOnce(): Promise<void> {
  if (quitting || statusPollInProgress) return;
  statusPollInProgress = true;
  try {
    const status = await fetchHealth();
    if (quitting) return;
    currentState = status.state;
    observeDaemonBoundary(status);
    const decision = recoveryDecision(status);
    sendStatus(
      decision === "pause" ? { ...status, state: "recovery_paused" } : status,
    );
    if (decision === "confirm") {
      const active = await ensureActiveProfile();
      if (active) {
        // Recovery can spend 10s confirming health plus 60s in the CLI start.
        // Do not hold the poll lock across it: startDaemon publishes
        // "starting" immediately, and subsequent polls keep that visible.
        void lifecycleOperations
          .runBackground(() => attemptDaemonRecovery(active))
          .catch((err) => {
            console.warn("[daemon] background recovery failed:", err);
          });
      }
    }
    // Retry a deferred version-mismatch restart once the daemon drains. Route
    // it through the same singleflight guard as user and recovery operations.
    if (pendingTrustRefresh) {
      void lifecycleOperations
        .runBackground(() => refreshDaemonTrustWhenIdle())
        .catch((err) => console.warn("[daemon] deferred trust refresh failed:", err));
    } else if (pendingVersionRestart && status.state === "running") {
      void lifecycleOperations
        .runBackground(() => ensureRunningDaemonVersionMatches())
        .catch((err) => {
          console.warn("[daemon] deferred version restart failed:", err);
        });
    }
  } finally {
    statusPollInProgress = false;
  }
}

function startPolling(): void {
  if (quitting || statusPollTimer || desiredRunningBeforeUpdateStop !== null) return;
  void pollOnce();
  statusPollTimer = setInterval(() => void pollOnce(), POLL_INTERVAL_MS);
}

/**
 * Ensures the CLI binary is available, then transitions into the normal
 * stopped/running state machine. Called once at startup and again on
 * user-triggered `daemon:retry-install`.
 */
async function bootstrapCli(): Promise<void> {
  const active = await ensureActiveProfile();
  if (!active || await pinnedBoundary(active)) {
    currentState = "stopped";
    startPolling();
    return;
  }
  const bin = await resolveCliBinary();
  if (!bin) {
    currentState = "cli_not_found";
    sendStatus({ state: "cli_not_found" });
    return;
  }
  currentState = "stopped";
  sendStatus({ state: "stopped" });
  startPolling();
}

function stopPolling(): void {
  if (statusPollTimer) {
    clearInterval(statusPollTimer);
    statusPollTimer = null;
  }
}

const LOG_TAIL_INITIAL_WINDOW_BYTES = 32 * 1024;
const LOG_TAIL_INITIAL_LINES = 200;
const LOG_TAIL_POLL_MS = 500;

async function readLogRange(
  path: string,
  startAt: number,
  length: number,
): Promise<string> {
  const handle = await open(path, "r");
  try {
    const buffer = Buffer.alloc(length);
    const { bytesRead } = await handle.read(buffer, 0, length, startAt);
    return buffer.subarray(0, bytesRead).toString("utf-8");
  } finally {
    await handle.close();
  }
}

function sendLines(win: BrowserWindow, text: string): void {
  if (win.isDestroyed()) return;
  const lines = text.split("\n").filter((line) => line.length > 0);
  for (const line of lines) {
    win.webContents.send("daemon:log-line", line);
  }
}

// Cross-platform tail -f replacement: read the tail of the file once, then
// poll its stat with fs.watchFile and forward any new bytes since the last
// known offset. watchFile works on macOS, Linux, and Windows; spawn("tail")
// would silently fail on Windows.
function startLogTail(win: BrowserWindow, retryCount = 0): void {
  stopLogTail();

  void ensureActiveProfile().then(async (active) => {
    // Before the renderer reports its apiUrl there is no Desktop-owned profile
    // yet, and therefore no log file of ours to tail. Retry rather than reach
    // for the default profile's log.
    const logPath = active ? profileLogPath(active.name) : null;
    if (!logPath || !existsSync(logPath)) {
      if (retryCount < LOG_TAIL_MAX_RETRIES) {
        setTimeout(() => startLogTail(win, retryCount + 1), LOG_TAIL_RETRY_MS);
      }
      return;
    }

    let position = 0;
    try {
      const initialStats = await stat(logPath);
      const windowBytes = Math.min(
        initialStats.size,
        LOG_TAIL_INITIAL_WINDOW_BYTES,
      );
      const startAt = initialStats.size - windowBytes;
      if (windowBytes > 0) {
        const text = await readLogRange(logPath, startAt, windowBytes);
        if (win.isDestroyed()) return;
        const lines = text
          .split("\n")
          .filter((line) => line.length > 0)
          .slice(-LOG_TAIL_INITIAL_LINES);
        for (const line of lines) {
          win.webContents.send("daemon:log-line", line);
        }
      }
      position = initialStats.size;
    } catch (err) {
      console.warn("[daemon] log tail initial read failed:", err);
      return;
    }

    const listener: StatsListener = (curr) => {
      if (win.isDestroyed()) {
        stopLogTail();
        return;
      }
      // File rotated/truncated — restart from the new beginning.
      if (curr.size < position) position = 0;
      if (curr.size === position) return;
      const from = position;
      const length = curr.size - from;
      position = curr.size;
      readLogRange(logPath, from, length)
        .then((text) => sendLines(win, text))
        .catch((err) => {
          console.warn("[daemon] log tail read failed:", err);
        });
    };

    watchFile(logPath, { interval: LOG_TAIL_POLL_MS }, listener);
    logTailWatcher = { path: logPath, listener };
  });
}

function stopLogTail(): void {
  if (logTailWatcher) {
    unwatchFile(logTailWatcher.path, logTailWatcher.listener);
    logTailWatcher = null;
  }
}

export function prepareDaemonForUpdateInstall(options: {
  interruptActiveTasks: boolean;
}): Promise<DaemonInstallPrep> {
  desiredRunningBeforeUpdateStop ??= desiredDaemonRunning;
  setDesiredDaemonRunning(false);
  stopPolling();
  return prepareDaemonForInstall({
    probe: async () => {
      const active = await ensureActiveProfile();
      if (!active) return null;
      const health = await fetchProfileHealth(active);
      return {
        alive: daemonStatusAlive(health?.status),
        pid: health?.pid,
        activeTasks: health?.active_task_count ?? 0,
        externallyManaged: isDaemonExternallyManaged(health?.os, normalizeHostOS(process.platform)),
      };
    },
    stop: async (signal) => {
      const result = await lifecycleOperations.runForeground(() => stopDaemon({ signal }));
      if (!result.success) throw new Error(result.error ?? "daemon stop failed");
    },
    processExists: daemonProcessExists,
    sleep: (ms) => new Promise((resolve) => setTimeout(resolve, ms)),
  }, options);
}

export function resumeDaemonAfterAbortedUpdateInstall(): void {
  if (desiredRunningBeforeUpdateStop === null) return;
  const desired = desiredRunningBeforeUpdateStop;
  desiredRunningBeforeUpdateStop = null;
  setDesiredDaemonRunning(desired, true);
  startPolling();
}

/**
 * `windowGetter` is the main window that hosts dialogs and progress;
 * `statusWindowsGetter` lists every main window that shows daemon status.
 */
/** The server the App is connected to, once the renderer has reported it. */
export function getTargetApiBaseUrl(): string | null {
  return targetApiBaseUrl;
}

let preserveDaemonOnQuit = false;

/**
 * Keep the daemon running through the next quit. Used when the App quits only
 * to replace its own bundle: stopping the daemon would cancel running agent
 * tasks after the 30-second shutdown drain. The relaunched App restarts the
 * daemon into its bundled CLI once the daemon is idle (version-mismatch path).
 */
export function preserveDaemonOnNextQuit(): () => void {
  const previous = preserveDaemonOnQuit;
  preserveDaemonOnQuit = true;
  return () => { preserveDaemonOnQuit = previous; };
}

export function setupDaemonManager(
  windowGetter: () => BrowserWindow | null,
  statusWindowsGetter: () => readonly BrowserWindow[] = () => {
    const window = windowGetter();
    return window ? [window] : [];
  },
  lifecycleBarrier?: Promise<void>,
): void {
  getMainWindow = windowGetter;
  getStatusWindows = statusWindowsGetter;

  let transactionPending = lifecycleBarrier !== undefined;
  if (lifecycleBarrier) {
    void lifecycleOperations.runForeground(async () => { await lifecycleBarrier; transactionPending = false; });
  }

  ipcMain.handle("daemon:set-target-api-url", (_e, url: string) => {
    const normalized = url || null;
    if (transactionPending) {
      if (targetApiBaseUrl !== normalized) { targetApiBaseUrl = normalized; invalidateActiveProfile(); }
      return;
    }
    if (targetApiBaseUrl === normalized) return;
    const setTarget = async () => {
      if (targetApiBaseUrl !== normalized) {
        console.log(`[daemon] target API URL set to ${normalized ?? "(none)"}`);
        setDesiredDaemonRunning(false);
        targetApiBaseUrl = normalized;
        invalidateActiveProfile();
        void lifecycleOperations.runBackground(() => bootstrapCli());
        await pollOnce();
      }
    };
    // Initial discovery must not wait for a CLI download: quit still needs the
    // profile's process evidence while installation is pending. Once a target
    // exists, serialize switches with token writes and daemon lifecycle work.
    return targetApiBaseUrl === null
      ? setTarget()
      : lifecycleOperations.runForeground(setTarget);
  });
  ipcMain.handle("daemon:start", () => {
    if (desiredRunningBeforeUpdateStop !== null) return UPDATE_IN_PROGRESS;
    externalDaemonObserved = false;
    setDesiredDaemonRunning(true, true);
    return lifecycleOperations.runForeground(() => startDaemon());
  });
  ipcMain.handle("daemon:stop", () => {
    if (desiredRunningBeforeUpdateStop !== null) {
      desiredRunningBeforeUpdateStop = false;
      return { success: true };
    }
    setDesiredDaemonRunning(false, true);
    return lifecycleOperations.runForeground(() => stopDaemon());
  });
  ipcMain.handle("daemon:restart", () => {
    if (desiredRunningBeforeUpdateStop !== null) return UPDATE_IN_PROGRESS;
    externalDaemonObserved = false;
    setDesiredDaemonRunning(true, true);
    return lifecycleOperations.runForeground(() => restartDaemon());
  });
  ipcMain.handle("daemon:get-status", () => transactionPending ? { state: "starting" } : fetchHealth());
  ipcMain.handle("daemon:probe-runtimes", () => lifecycleOperations.runForeground(() => probeLocalRuntimes()));
  ipcMain.handle("daemon:focus-environment", (_e, key: unknown) => transactionPending ? { ok: false, reason: "unavailable" } : focusEnvironment(key));
  // The host's OS name, available regardless of daemon state. The Runtimes
  // page uses it as a fallback identity for "this machine" when no
  // app-managed daemon is reporting a device name (e.g. the daemon runs
  // out-of-band in WSL2). See desktop-runtimes-page.tsx.
  ipcMain.handle("daemon:get-host-name", () => hostname());
  ipcMain.handle(
    "daemon:sync-token",
    (_event, token: string, userId: string) => lifecycleOperations.runForeground(async () => {
      const result = await syncToken(token, userId);
      if (result.userChanged) {
        await restartDaemonAfterUserSwitch(result.active);
      }
    }),
  );
  ipcMain.handle("daemon:desktop-token-id", (_event, jwt: string, userId: string) =>
    lifecycleOperations.runForeground(() => getDesktopTokenId(jwt, userId)));
  ipcMain.handle("daemon:rotate-desktop-token", (_event, jwt: string, userId: string, id: string) =>
    lifecycleOperations.runForeground(() => rotateDesktopToken(jwt, userId, id)));
  ipcMain.handle("daemon:clear-token", () => {
    setDesiredDaemonRunning(false, true);
    return lifecycleOperations.runForeground(() => clearToken());
  });
  ipcMain.handle(
    "daemon:reauthenticate",
    async (_event, token: string, userId: string): Promise<ReauthResult> => {
      setDesiredDaemonRunning(true, true);
      return lifecycleOperations.runForeground(() =>
        reauthenticate(token, userId),
      );
    },
  );
  ipcMain.handle("daemon:is-cli-installed", async () => {
    return lifecycleOperations.runForeground(async () => {
      const active = await ensureActiveProfile();
      if (!active || await pinnedBoundary(active)) return false;
      const bin = await resolveCliBinary();
      return bin !== null;
    });
  });
  ipcMain.handle("daemon:retry-install", async () => {
    cachedCliBinary = undefined;
    cliResolvePromise = null;
    // A retry-install may land a new CLI at a different version; drop the
    // cached version string so the next check re-reads the binary.
    cachedCliBinaryVersion = undefined;
    await lifecycleOperations.runForeground(() => bootstrapCli());
  });
  ipcMain.handle("daemon:get-prefs", () => loadPrefs());
  ipcMain.handle(
    "daemon:set-prefs",
    (_event, prefs: Partial<DaemonPrefs>) =>
      loadPrefs().then((cur) => {
        const merged = { ...cur, ...prefs };
        // Changing the preference still affects the next logged-in launch; it
        // does not start/stop the current session. The Settings copy explicitly
        // states that a launched daemon is supervised while Desktop stays open.
        return savePrefs(merged).then(() => merged);
      }),
  );
  ipcMain.handle("daemon:auto-start", async () => {
    const prefs = await loadPrefs();
    if (quitting || autoStartSuppressedAfterQuit) return;
    setDesiredDaemonRunning(prefs.autoStart);
    if (!prefs.autoStart) return;
    // Login auto-start is emitted once per session. Queue it behind bootstrap
    // instead of dropping it like a retryable poll operation.
    await lifecycleOperations.runForeground(async () => {
      if (quitting || autoStartSuppressedAfterQuit) return;
      const active = await ensureActiveProfile();
      if (!active || await pinnedBoundary(active)) return;
      const bin = await resolveCliBinary();
      if (!bin) return;
      const health = await fetchHealth();
      if (quitting || autoStartSuppressedAfterQuit) return;
      observeDaemonBoundary(health);
      if (health.state === "running") {
        // Daemon is up but may be running an older CLI than the one we just
        // bundled. Restart it so the new binary actually takes effect.
        await ensureRunningDaemonVersionMatches();
        return;
      }
      await startDaemon();
    });
  });

  // One tail at a time, streamed to the window that opened the log view
  // last; a window closing its view does not stop another window's stream.
  ipcMain.on("daemon:start-log-stream", (event) => {
    const win = BrowserWindow.fromWebContents(event.sender);
    if (!win) return;
    logTailOwner = event.sender;
    startLogTail(win);
  });

  ipcMain.on("daemon:stop-log-stream", (event) => {
    if (logTailOwner && logTailOwner !== event.sender) return;
    logTailOwner = null;
    stopLogTail();
  });

  // Reveal the daemon's log file in the user's default editor / Console
  // app. Acts as the escape hatch when the in-app log viewer isn't enough
  // (full history, complex search, copy-to-clipboard at scale).
  ipcMain.handle("daemon:open-log-file", async () => {
    if (transactionPending) return { success: false, error: "更新進行中。" };
    const active = await ensureActiveProfile();
    const logPath = active ? profileLogPath(active.name) : null;
    if (!logPath || !existsSync(logPath)) {
      return { success: false, error: "Log file not found yet" };
    }
    // shell.openPath returns "" on success, error string on failure.
    const error = await shell.openPath(logPath);
    return error === "" ? { success: true } : { success: false, error };
  });

  // First-run CLI install kicks off here. Status bar shows "Setting up…"
  // until the managed binary is on disk (instant on subsequent launches).
  currentState = "installing_cli";
  sendStatus({ state: "installing_cli" });
  if (lifecycleBarrier) void lifecycleBarrier.then(() => lifecycleOperations.runForeground(() => bootstrapCli()));
  else void lifecycleOperations.runBackground(() => bootstrapCli());

  let desiredBeforeQuit = false;
  const language = app.getPreferredSystemLanguages()[0] ?? "en";
  let hiddenQuitWindows: BrowserWindow[] = [];
  const hideQuitWindows = () => {
    hiddenQuitWindows = BrowserWindow.getAllWindows().filter((window) =>
      !window.isDestroyed() && window.isVisible(),
    );
    for (const window of hiddenQuitWindows) window.hide();
  };
  const restoreQuitWindows = () => {
    for (const window of hiddenQuitWindows) {
      if (!window.isDestroyed()) window.show();
    }
    hiddenQuitWindows = [];
  };
  const show = (options: MessageBoxOptions) => {
    const window = getMainWindow();
    return window && !window.isDestroyed() && window.isVisible()
      ? dialog.showMessageBox(window, options) : dialog.showMessageBox(options);
  };
  const handleQuit = createDaemonQuitHandler({
    loadPrefs,
    pause: () => {
      desiredBeforeQuit = desiredDaemonRunning;
      quitting = true;
      setDesiredDaemonRunning(false);
      stopPolling();
    },
    resume: (restoreDesired) => {
      restoreQuitWindows();
      if (!restoreDesired) autoStartSuppressedAfterQuit = true;
      quitting = false;
      setDesiredDaemonRunning(restoreDesired && desiredBeforeQuit);
      startPolling();
      if (!getMainWindow()) app.emit("activate");
    },
    stop: async () => {
      const active = await ensureActiveProfile();
      if (!active) return true;
      const health = await fetchProfileHealth(active);
      if (await pinnedBoundary(active, health) || isDaemonExternallyManaged(health?.os, normalizeHostOS(process.platform))) return true;
      const healthy = daemonStatusAlive(health?.status);
      const reportedRuns = health?.active_task_count;
      const activeRuns = healthy && typeof reportedRuns === "number"
        && Number.isSafeInteger(reportedRuns) && reportedRuns >= 0 ? reportedRuns : null;
      let busy = healthy
        ? activeRuns !== 0
        : currentState === "running" || currentState === "starting";
      if (!healthy && !busy) {
        busy = !await daemonProcessIsConfirmedAbsent(active.name);
      }
      if (busy && (await show(quitConfirmationOptions(language, activeRuns))).response !== 1) return false;
      // Reuse the lifecycle queue: an earlier start must finish before stop.
      // Do not queue behind an unresolved CLI installation during quit.
      if (!cachedCliBinary) {
        if (!healthy && await daemonProcessIsConfirmedAbsent(active.name)) return true;
        throw new Error("Multica CLI is not available");
      }
      const window = getMainWindow();
      window?.setProgressBar(2);
      // Make the accepted quit visible even while a prior start or cleanup drains.
      hideQuitWindows();
      try {
        const result = await lifecycleOperations.runForeground(() => stopDaemon({ force: true }));
        if (!result.success) throw new Error(result.error ?? "Could not stop the daemon");
      } finally {
        if (window && !window.isDestroyed()) window.setProgressBar(-1);
      }
      return true;
    },
    confirmIgnore: async (error) => {
      console.warn("[daemon] could not stop on quit:", error);
      restoreQuitWindows();
      const active = await ensureActiveProfile();
      const health = active ? await fetchProfileHealth(active) : null;
      const daemonStopped = Boolean(active && !daemonStatusAlive(health?.status)
        && await daemonProcessIsConfirmedAbsent(active.name));
      return (await show(quitFailureOptions(language, daemonStopped))).response === 1;
    },
    quit: () => { stopLogTail(); app.quit(); },
  });
  app.on("before-quit", (event) => {
    // The Windows updater registers first and owns its pending or approved quit.
    if (event.defaultPrevented) return;
    if (desiredRunningBeforeUpdateStop !== null) {
      setDesiredDaemonRunning(false);
      stopPolling();
      stopLogTail();
      return;
    }
    if (preserveDaemonOnQuit || transactionPending) return;
    handleQuit(event);
  });
}
