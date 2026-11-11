import { useCallback, useEffect, useState } from "react";
import { AlertCircle, ArrowDownToLine, Check, Loader2 } from "lucide-react";
import { Button } from "@multica/ui/components/ui/button";
import { Switch } from "@multica/ui/components/ui/switch";
import { useT } from "@multica/views/i18n";
import { SettingsCard, SettingsRow, SettingsTab } from "@multica/views/settings";
import { toast } from "sonner";
import { useUpdaterStatus } from "../hooks/use-updater-status";

type CheckState =
  | { status: "idle" }
  | { status: "checking" }
  | { status: "up-to-date" }
  | { status: "available"; latestVersion: string }
  | { status: "error"; message: string };

export function UpdatesSettingsTab() {
  const { t } = useT("settings");
  const [state, setState] = useState<CheckState>({ status: "idle" });
  const { status, statusError } = useUpdaterStatus();
  const [automaticUpdates, setAutomaticUpdates] = useState(true);
  const [preferencesReady, setPreferencesReady] = useState(false);
  const [savingPreference, setSavingPreference] = useState(false);
  const currentVersion = window.desktopAPI.appInfo.version;

  useEffect(() => {
    if (status && ["checking", "downloading", "ready"].includes(status.phase)) {
      setState((current) => current.status === "error" ? { status: "idle" } : current);
    }
  }, [status]);

  useEffect(() => {
    let mounted = true;
    void window.updater
      .getPreferences()
      .then((preferences) => {
        if (mounted) setAutomaticUpdates(preferences.automaticUpdates);
      })
      .catch(() => {
        // The main process falls back to enabled when preferences cannot be
        // read. Keep the same safe default if IPC itself becomes unavailable.
      })
      .finally(() => {
        if (mounted) setPreferencesReady(true);
      });

    return () => {
      mounted = false;
    };
  }, []);

  const handleAutomaticUpdatesChange = useCallback(
    async (enabled: boolean) => {
      setSavingPreference(true);
      try {
        const preferences = await window.updater.setAutomaticUpdates(enabled);
        setAutomaticUpdates(preferences.automaticUpdates);
        toast.success(t(($) => $.auto_save.toast_saved), {
          id: "settings-auto-save",
        });
      } catch {
        toast.error(t(($) => $.desktop.updates.automatic_updates_save_failed));
      } finally {
        setSavingPreference(false);
      }
    },
    [t],
  );

  const handleCheck = useCallback(async () => {
    setState({ status: "checking" });
    try {
      const result = await window.updater.checkForUpdates();
      if (!result.ok) {
        setState({ status: "error", message: result.error });
        return;
      }
      setState(
        result.available
          ? { status: "available", latestVersion: result.latestVersion }
          : { status: "up-to-date" },
      );
    } catch (error) {
      setState({ status: "error", message: error instanceof Error ? error.message : String(error) });
    }
  }, []);

  const handleInstall = useCallback(async () => {
    try {
      await window.updater.installUpdate();
    } catch (error) {
      setState({ status: "error", message: error instanceof Error ? error.message : String(error) });
    }
  }, []);

  return (
    <SettingsTab
      title={t(($) => $.desktop.updates.title)}
    >
      <SettingsCard>
        <SettingsRow label={t(($) => $.desktop.updates.current_version)}>
          <span className="font-mono text-caption text-muted-foreground">
            v{currentVersion}
          </span>
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.automatic_updates_title)}
          description={t(($) => $.desktop.updates.automatic_updates_description)}
        >
          <Switch
            checked={automaticUpdates}
            onCheckedChange={handleAutomaticUpdatesChange}
            disabled={!preferencesReady || savingPreference}
            aria-label={t(($) => $.desktop.updates.automatic_updates_title)}
          />
        </SettingsRow>

        <SettingsRow
          label={t(($) => $.desktop.updates.check_section_title)}
          align="start"
          description={
            <>
              <p>{t(($) => $.desktop.updates.check_section_description)}</p>
              {state.status === "up-to-date" && status?.phase === "idle" && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <Check className="size-3.5 text-success" />
                  {t(($) => $.desktop.updates.up_to_date)}
                </p>
              )}
              {state.status === "available" && status?.phase !== "downloading" && status?.phase !== "ready" && (
                <p className="mt-2 inline-flex items-center gap-1.5">
                  <ArrowDownToLine className="size-3.5 text-primary" />
                  {t(($) => $.desktop.updates.available, { version: state.latestVersion })}
                </p>
              )}
              {status?.phase === "downloading" && <p role="status">{t(($) => $.desktop.updates.progress, { percent: Math.round(status.percent ?? 0) })}</p>}
              {status?.phase === "ready" && <p role="status">{t(($) => $.desktop.updates.ready, { version: status.version ?? "" })}</p>}
              {status?.phase === "installing" && <p role="status">{t(($) => $.desktop.updates.installing)}</p>}
              {status?.phase === "unsupported" && <p>{t(($) => $.desktop.updates.unsupported)}</p>}
              {(statusError || status?.phase === "error") && <p role="alert" className="text-destructive">{status?.error || t(($) => $.desktop.updates.status_failed)}</p>}
              {state.status === "error" && status?.phase !== "error" && (
                <p className="mt-2 inline-flex items-center gap-1.5 text-destructive">
                  <AlertCircle className="size-3.5" />
                  {state.message}
                </p>
              )}
            </>
          }
        >
          <div className="flex flex-wrap gap-2">
          {status?.phase === "ready" && <Button size="sm" onClick={handleInstall}>{t(($) => $.desktop.updates.restart_now)}</Button>}
          <Button
            variant="outline"
            size="sm"
            onClick={handleCheck}
            disabled={state.status === "checking" || status?.phase === "checking" || status?.phase === "unsupported"}
          >
            {state.status === "checking" || status?.phase === "checking" ? (
              <>
                <Loader2 className="size-3.5 animate-spin" />
                {status?.phase === "downloading"
                  ? t(($) => $.desktop.updates.progress, { percent: Math.round(status.percent ?? 0) })
                  : t(($) => $.desktop.updates.checking)}
              </>
            ) : t(($) => $.desktop.updates.check_now)}
          </Button>
          </div>
        </SettingsRow>
      </SettingsCard>
    </SettingsTab>
  );
}
