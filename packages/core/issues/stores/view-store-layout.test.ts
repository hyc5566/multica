// @vitest-environment node
import { expect, it } from "vitest";
import { createStore } from "zustand/vanilla";
import { mergeViewStatePersisted, viewStoreSlice, type IssueViewState } from "./view-store";

it("restores compact layout and defaults old or invalid snapshots", () => {
  const defaults = createStore<IssueViewState>()(viewStoreSlice).getState();
  expect(mergeViewStatePersisted({ boardLayout: "compact" }, defaults).boardLayout).toBe("compact");
  for (const snapshot of [{}, { boardLayout: "unknown" }, { boardLayout: null }]) {
    expect(mergeViewStatePersisted(snapshot, defaults).boardLayout).toBe("default");
  }
});
