import { act, renderHook, waitFor } from "@testing-library/react";
import { expect, it, vi } from "vitest";
import type { UpdaterStatus } from "../../../shared/updater-types";
import { useUpdaterStatus } from "./use-updater-status";

it("keeps a newer event when a refresh snapshot arrives late", async () => {
  const idle: UpdaterStatus = { phase: "idle", currentVersion: "1.0" };
  let listener!: (status: UpdaterStatus) => void;
  let resolve!: (status: UpdaterStatus) => void;
  const getStatus = vi.fn().mockResolvedValueOnce(idle).mockReturnValueOnce(new Promise((done) => { resolve = done; }));
  Object.defineProperty(window, "updater", { configurable: true, value: {
    getStatus,
    onStatusChange: (callback: typeof listener) => { listener = callback; return vi.fn(); },
  } });
  const { result } = renderHook(() => useUpdaterStatus());
  await waitFor(() => expect(result.current.status?.phase).toBe("idle"));
  let refresh!: Promise<void>;
  act(() => { refresh = result.current.refreshStatus(); });
  act(() => listener({ ...idle, phase: "ready", version: "1.1" }));
  await act(async () => { resolve(idle); await refresh; });
  expect(result.current.status).toMatchObject({ phase: "ready", version: "1.1" });
});
