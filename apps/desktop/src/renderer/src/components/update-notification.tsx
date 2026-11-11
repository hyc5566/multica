import { useEffect, useState } from "react";
import { Button } from "@multica/ui/components/ui/button";
import { useT } from "@multica/views/i18n";
import { useUpdaterStatus } from "../hooks/use-updater-status";

export function UpdateNotification() {
  const { t } = useT("settings");
  const { status, statusError, refreshStatus } = useUpdaterStatus();
  const [actionError, setActionError] = useState<string | null>(null);
  const [pending, setPending] = useState(false);

  useEffect(() => { window.updater.rendererReady(); }, []);
  useEffect(() => {
    if (status && ["checking", "downloading", "ready"].includes(status.phase)) setActionError(null);
  }, [status]);

  const act = async (action: () => Promise<unknown>) => {
    setActionError(null);
    setPending(true);
    try {
      const result = await action();
      if (result && typeof result === "object" && "ok" in result && result.ok === false && "error" in result) {
        setActionError(String(result.error));
      }
    } catch (error) {
      setActionError(error instanceof Error ? error.message : String(error));
    } finally {
      setPending(false);
    }
  };

  if (!statusError && !actionError && (!status || !["downloading", "ready", "installing", "pending", "error"].includes(status.phase) || (status.phase === "ready" && status.deferred))) return null;

  return (
    <div role="status" className="fixed bottom-4 right-4 z-50 w-80 rounded-lg border border-border bg-background p-4 shadow-lg">
      <p className="text-body font-medium">{t(($) => $.desktop.updates.title)}</p>
      {status?.phase === "downloading" && <p>{t(($) => $.desktop.updates.progress, { percent: Math.round(status.percent ?? 0) })}</p>}
      {status?.phase === "installing" && <p>{t(($) => $.desktop.updates.installing)}</p>}
      {status?.phase === "pending" && <p>{status.message}</p>}
      {status?.phase === "ready" && <>
        <p>{t(($) => $.desktop.updates.ready, { version: status.version ?? "" })}</p>
        <p className="text-caption text-muted-foreground">{t(($) => $.desktop.updates.defer_explanation)}</p>
      </>}
      {(statusError || status?.phase === "error" || actionError) && <p role="alert" className="text-destructive">{actionError || status?.error || t(($) => $.desktop.updates.status_failed)}</p>}
      <div className="mt-2 flex flex-wrap gap-2">
        {status?.phase === "ready" && <>
          {status.notesUrl && <Button variant="outline" size="sm" onClick={() => void window.desktopAPI.openExternal(status.notesUrl!)}>{t(($) => $.desktop.updates.release_notes)}</Button>}
          <Button variant="outline" size="sm" disabled={pending} onClick={() => void act(() => window.updater.deferUpdate())}>{t(($) => $.desktop.updates.later)}</Button>
          <Button size="sm" disabled={pending} onClick={() => void act(() => window.updater.installUpdate())}>{t(($) => $.desktop.updates.restart_now)}</Button>
        </>}
        {(status?.phase === "error" || statusError) && <Button size="sm" disabled={pending} onClick={() => void act(async () => {
          try { return await window.updater.checkForUpdates(); }
          finally { await refreshStatus(); }
        })}>{t(($) => $.desktop.updates.retry)}</Button>}
      </div>
    </div>
  );
}
