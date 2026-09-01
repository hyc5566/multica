// @vitest-environment jsdom

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, fireEvent, screen, waitFor } from "@testing-library/react";
import type { Agent } from "@multica/core/types";
import { renderWithI18n } from "../../test/i18n";

vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQuery: () => ({ data: undefined, isSuccess: false }),
}));

vi.mock("../../common/avatar-upload-control", () => ({
  AvatarUploadControl: () => <div data-testid="avatar-upload" />,
}));

vi.mock("./inspector/runtime-picker", () => ({
  RuntimePicker: ({ onChange }: { onChange: (value: string) => void }) => (
    <button type="button" onClick={() => onChange("runtime-2")}>
      change runtime
    </button>
  ),
}));

vi.mock("./inspector/model-picker", () => ({
  ModelPicker: ({ onChange }: { onChange: (value: string) => void }) => (
    <button type="button" onClick={() => onChange("model-2")}>
      change model
    </button>
  ),
}));

vi.mock("./inspector/thinking-prop-row", () => ({
  ThinkingSettingField: ({ onChange }: { onChange: (value: string) => void }) => (
    <button type="button" onClick={() => onChange("high")}>
      change thinking
    </button>
  ),
}));

vi.mock("./inspector/service-tier-setting-field", () => ({
  ServiceTierSettingField: ({ onChange }: { onChange: (value: string) => void }) => (
    <button type="button" onClick={() => onChange("priority")}>
      change speed
    </button>
  ),
}));

import { AgentDetailInspector } from "./agent-detail-inspector";

const agent = {
  id: "agent-1",
  workspace_id: "workspace-1",
  name: "Lambda",
  description: "Original",
  runtime_id: "runtime-1",
  model: "model-1",
  thinking_level: "medium",
  service_tier: "",
  max_concurrent_tasks: 1,
} as Agent;

function renderInspector(onUpdate: (id: string, data: Record<string, unknown>) => Promise<void>) {
  return renderWithI18n(
    <AgentDetailInspector
      agent={agent}
      runtime={null}
      runtimes={[]}
      members={[]}
      currentUserId="user-1"
      canEdit
      onUpdate={onUpdate}
    />,
  );
}

describe("AgentDetailInspector explicit save", () => {
  afterEach(cleanup);

  it("keeps edits local, resets them, and submits accumulated fields once", async () => {
    const onUpdate = vi.fn(async () => {});
    renderInspector(onUpdate);

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Draft name" },
    });
    fireEvent.click(screen.getByRole("button", { name: "change model" }));

    expect(onUpdate).not.toHaveBeenCalled();
    expect(screen.getAllByRole("button", { name: "Reset" }).length).toBeGreaterThan(0);

    fireEvent.click(screen.getAllByRole("button", { name: "Reset" })[0]!);
    expect(screen.getByLabelText("Name")).toHaveValue("Lambda");
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Saved name" },
    });
    fireEvent.click(screen.getByRole("button", { name: "change thinking" }));
    fireEvent.click(screen.getAllByRole("button", { name: "Save changes" })[0]!);

    await waitFor(() => {
      expect(onUpdate).toHaveBeenCalledTimes(1);
    });
    expect(onUpdate).toHaveBeenCalledWith("agent-1", {
      name: "Saved name",
      thinking_level: "high",
    });
    expect(screen.queryByRole("button", { name: "Save changes" })).toBeNull();
  });

  it("keeps a failed draft available for retry or reset", async () => {
    const onUpdate = vi.fn(async () => {
      throw new Error("save failed");
    });
    renderInspector(onUpdate);

    fireEvent.change(screen.getByLabelText("Description"), {
      target: { value: "Unsaved description" },
    });
    fireEvent.click(screen.getAllByRole("button", { name: "Save changes" })[0]!);

    await waitFor(() => expect(onUpdate).toHaveBeenCalledTimes(1));
    expect(screen.getByLabelText("Description")).toHaveValue(
      "Unsaved description",
    );
    expect(screen.getAllByRole("button", { name: "Reset" }).length).toBeGreaterThan(0);
  });
});
