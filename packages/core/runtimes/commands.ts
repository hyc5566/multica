export const CLOUD_SERVER_URL = "https://api.multica.ai";
export const CLOUD_APP_URL = "https://multica.ai";

export function normalizeCommandURL(url: string | undefined): string {
  const value = url?.trim().replace(/\/+$/, "") ?? "";
  return /^[a-zA-Z0-9:/._~-]*$/.test(value)
    ? value
    : `'${value.replaceAll("'", "'\\''")}'`;
}

export function operatorInstallCommand(raw = ""): string | undefined {
  const value = raw.trim();
  try {
    const url = new URL(value);
    if (url.protocol === "https:" && url.hostname && !url.username && !url.password
      && !/[?#\\'"`$;|&<>()\s]/.test(value)) {
      return `curl -fsSL -- '${value}' | bash`;
    }
  } catch {
    // A missing or invalid operator URL keeps the platform installer.
  }
  return undefined;
}

export type DaemonSetupTarget = "cloud" | "self_host" | "unknown";
export type DaemonConfigStatus = "loading" | "ready" | "error";

export interface DaemonSetupConfig {
  status: DaemonConfigStatus;
  target: DaemonSetupTarget;
  serverUrl?: string;
  appUrl?: string;
  installUrl?: string;
}

export type DaemonCommands =
  | { status: "ready"; setupCmd: string; tokenCmd: string; cli: string }
  | { status: "loading" | "error" | "unknown" | "incomplete" };

export function daemonCommands(config: DaemonSetupConfig): DaemonCommands {
  if (config.status !== "ready") return { status: config.status };
  const cli = operatorInstallCommand(config.installUrl) ? '"$HOME/.local/bin/multica"' : "multica";
  const login = cli === "multica" ? "login --token <YOUR_TOKEN>" : "login --token";

  if (config.target === "self_host") {
    const normalizedServerUrl = normalizeCommandURL(config.serverUrl);
    const normalizedAppUrl = normalizeCommandURL(config.appUrl);
    if (!normalizedServerUrl || !normalizedAppUrl) return { status: "incomplete" };
    return {
      status: "ready",
      cli,
      setupCmd: `${cli} setup self-host --server-url ${normalizedServerUrl} --app-url ${normalizedAppUrl}`,
      tokenCmd: `${cli} config set server_url ${normalizedServerUrl}
${cli} config set app_url ${normalizedAppUrl}
${cli} ${login}
${cli} daemon start`,
    };
  }

  // Empty URLs are also returned by misconfigured self-hosted servers.
  if (config.target !== "cloud") return { status: "unknown" };
  return {
    status: "ready",
    cli,
    setupCmd: `${cli} setup`,
    tokenCmd: `${cli} config set server_url ${CLOUD_SERVER_URL}
${cli} config set app_url ${CLOUD_APP_URL}
${cli} ${login}
${cli} daemon start`,
  };
}
