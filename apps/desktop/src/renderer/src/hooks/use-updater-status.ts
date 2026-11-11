import { useCallback, useEffect, useRef, useState } from "react";
import type { UpdaterStatus } from "../../../shared/updater-types";

export function useUpdaterStatus() {
  const [status, setStatus] = useState<UpdaterStatus | null>(null);
  const [statusError, setStatusError] = useState(false);
  const revision = useRef(0);
  const mounted = useRef(false);

  const refreshStatus = useCallback(async () => {
    const request = ++revision.current;
    try {
      const snapshot = await window.updater.getStatus();
      if (mounted.current && revision.current === request) {
        setStatus(snapshot);
        setStatusError(false);
      }
    } catch {
      if (mounted.current && revision.current === request) setStatusError(true);
    }
  }, []);

  useEffect(() => {
    mounted.current = true;
    const unsubscribe = window.updater.onStatusChange((next) => {
      revision.current += 1;
      setStatusError(false);
      setStatus(next);
    });
    void refreshStatus();
    return () => {
      mounted.current = false;
      revision.current += 1;
      unsubscribe();
    };
  }, [refreshStatus]);

  return { status, statusError, refreshStatus };
}
