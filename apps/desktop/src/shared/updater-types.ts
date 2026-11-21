export interface UpdaterPreferences {
  automaticUpdates: boolean;
}

export type UpdaterFailureReason = "unavailable" | "network" | "unknown";

export type UpdaterDownloadState =
  | { status: "idle" }
  | { status: "downloading"; version: string }
  | { status: "downloaded"; version: string }
  | { status: "error"; reason: UpdaterFailureReason };

export interface UpdaterStatus {
  phase: "idle" | "checking" | "downloading" | "ready" | "installing" | "pending" | "error" | "unsupported";
  currentVersion: string;
  version?: string;
  percent?: number;
  notesUrl?: string;
  error?: string;
  message?: string;
  /** Defers automatic installation for this App session. */
  deferred?: boolean;
}

export type ManualUpdateCheckResult =
  | {
      ok: true;
      currentVersion: string;
      latestVersion: string;
      available: boolean;
      downloadState: UpdaterDownloadState;
    }
  | { ok: false; error: string; reason: UpdaterFailureReason };

export type ZhTWManualUpdateCheckResult =
  | { ok: true; currentVersion: string; latestVersion: string; available: boolean }
  | { ok: false; error: string };
