// @vitest-environment node
import { expect, it } from "vitest";
import { createStore } from "zustand/vanilla";
import { mergeViewStatePersisted, viewStoreSlice, type IssueViewState } from "./view-store";

it("restores explicit layouts and defaults old or invalid snapshots to compact", () => {
  const defaults = createStore<IssueViewState>()(viewStoreSlice).getState();
  expect(mergeViewStatePersisted({ boardLayout: "compact" }, defaults).boardLayout).toBe("compact");
  expect(mergeViewStatePersisted({ boardLayout: "default" }, defaults).boardLayout).toBe("default");
  for (const snapshot of [{}, { boardLayout: "unknown" }, { boardLayout: null }]) {
    expect(mergeViewStatePersisted(snapshot, defaults).boardLayout).toBe("compact");
  }
});
