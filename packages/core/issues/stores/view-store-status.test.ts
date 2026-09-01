// @vitest-environment node
import { describe, expect, it, beforeEach } from "vitest";
import { createStore, type StoreApi } from "zustand/vanilla";
import {
  DEFAULT_BOARD_COLUMN_WIDTH,
  MAX_BOARD_COLUMN_WIDTH,
  MIN_BOARD_COLUMN_WIDTH,
  mergeViewStatePersisted,
  viewStoreSlice,
  type IssueViewState,
} from "./view-store";
import { baselineFromQuery } from "../../issue-views/baseline";

/**
 * Column visibility and the status filter used to be the same field. That was
 * only ever correct while a category held exactly one status; since MUL-6243 a
 * category can hold several, and the two questions have different answers.
 */
describe("column visibility vs status filter", () => {
  let store: StoreApi<IssueViewState>;
  beforeEach(() => {
    store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
  });

  // The regression: hiding one column wrote the OTHER six built-in keys into
  // statusFilters, so the query then excluded every custom status too — hiding
  // Backlog silently dropped a QA card sitting in the In Review column.
  it("hiding a column does not touch the status filter", () => {
    store.getState().hideStatus("backlog");

    expect(store.getState().hiddenStatusCategories).toEqual(["backlog"]);
    expect(store.getState().statusFilters).toEqual([]);
  });

  it("showing a column restores it without inventing a filter", () => {
    store.getState().hideStatus("backlog");
    store.getState().hideStatus("done");
    store.getState().showStatus("backlog");

    expect(store.getState().hiddenStatusCategories).toEqual(["done"]);
    expect(store.getState().statusFilters).toEqual([]);
  });

  it("hiding the same column twice is idempotent", () => {
    store.getState().hideStatus("backlog");
    store.getState().hideStatus("backlog");

    expect(store.getState().hiddenStatusCategories).toEqual(["backlog"]);
  });

  it("a custom status filter survives hiding and showing a column", () => {
    store.getState().toggleStatusFilter("qa");
    store.getState().hideStatus("backlog");
    store.getState().showStatus("backlog");

    expect(store.getState().statusFilters).toEqual(["qa"]);
  });

  it("reset restores every column", () => {
    store.getState().hideStatus("backlog");
    store.getState().clearFilters();

    expect(store.getState().hiddenStatusCategories).toEqual([]);
  });
});

describe("saved view baseline", () => {
  // The regression: the baseline dropped any status filter that was not one of
  // the 7 built-ins, so reopening a view saved with a custom status filter came
  // back showing MORE than it was saved with.
  it("keeps a custom status filter", () => {
    const baseline = baselineFromQuery({ statusFilters: ["in_review", "qa"] });

    expect(baseline.status.has("qa")).toBe(true);
    expect(baseline.status.has("in_review")).toBe(true);
  });

  it("still drops values it cannot represent", () => {
    const baseline = baselineFromQuery({ statusFilters: ["", "qa"] });

    expect([...baseline.status]).toEqual(["qa"]);
  });
});

describe("board column widths", () => {
  let store: StoreApi<IssueViewState>;

  beforeEach(() => {
    store = createStore<IssueViewState>()((set) => viewStoreSlice(set));
  });

  it("switches between the only two exposed board densities", () => {
    expect(store.getState().boardColumnDensity).toBe("default");
    store.getState().setBoardColumnDensity("compact");
    expect(store.getState().boardColumnDensity).toBe("compact");
    store.getState().setBoardColumnDensity("default");
    expect(store.getState().boardColumnDensity).toBe("default");
  });

  it("stores sparse per-group overrides and resets all columns", () => {
    store.getState().setBoardColumnWidth("status:todo", 360);
    store.getState().setBoardColumnWidth("status:done", 420);

    expect(store.getState().boardColumnWidths).toEqual({
      "status:todo": 360,
      "status:done": 420,
    });

    store
      .getState()
      .setBoardColumnWidth("status:todo", DEFAULT_BOARD_COLUMN_WIDTH);
    expect(store.getState().boardColumnWidths).toEqual({
      "status:done": 420,
    });

    store.getState().resetBoardColumnWidths();
    expect(store.getState().boardColumnWidths).toEqual({});
  });

  it("clamps interactive and persisted widths to the supported range", () => {
    store.getState().setBoardColumnWidth("status:todo", 40);
    store.getState().setBoardColumnWidth("status:done", 900);

    expect(store.getState().boardColumnWidths).toEqual({
      "status:todo": MIN_BOARD_COLUMN_WIDTH,
      "status:done": MAX_BOARD_COLUMN_WIDTH,
    });

    const merged = mergeViewStatePersisted(
      {
        boardColumnWidths: {
          "status:todo": 100,
          "status:done": 800,
          invalid: "wide",
          nan: Number.NaN,
        },
      },
      store.getState(),
    );
    expect(merged.boardColumnWidths).toEqual({
      "status:todo": MIN_BOARD_COLUMN_WIDTH,
      "status:done": MAX_BOARD_COLUMN_WIDTH,
    });
  });

  it("ignores an invalid persisted board density", () => {
    store.getState().setBoardColumnDensity("compact");
    const merged = mergeViewStatePersisted(
      { boardColumnDensity: "freeform" },
      store.getState(),
    );
    expect(merged.boardColumnDensity).toBe("compact");
  });
});
