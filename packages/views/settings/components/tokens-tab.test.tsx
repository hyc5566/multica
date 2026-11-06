import { fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, expect, it, vi } from "vitest";
import { renderWithI18n } from "../../test/i18n";
import { TokensTab } from "./tokens-tab";
const { list, revoke, rotate, identify } = vi.hoisted(() => ({list:vi.fn(), revoke:vi.fn(), rotate:vi.fn(), identify:vi.fn()}));
vi.mock("@multica/core/api", () => ({ api: { listPersonalAccessTokens:list, revokePersonalAccessToken:revoke } }));
vi.mock("sonner", () => ({ toast: { success:vi.fn(), error:vi.fn() } }));
const control = { getCurrentTokenId:identify, rotate };
beforeEach(() => {
  vi.resetAllMocks();
  list.mockResolvedValue([{ id:"current", name:"Multica Desktop", token_prefix:"mul_fake", created_at:"2026-09-10" }]);
  identify.mockResolvedValue("current");
  rotate.mockResolvedValue(undefined);
});
async function openDialog() {
  const button=await screen.findByRole("button", {name:/Revoke.*Multica Desktop/i});
  await waitFor(()=>expect(button).toBeEnabled());
  fireEvent.click(button);
}
it("cancel preserves the Desktop token", async () => {
  renderWithI18n(<TokensTab desktopTokenControl={control} />);
  await openDialog();
  expect(await screen.findByText("Regenerate Desktop API token?")).toBeInTheDocument();
  fireEvent.click(screen.getByRole("button", {name:"Cancel"}));
  expect(revoke).not.toHaveBeenCalled();
  expect(rotate).not.toHaveBeenCalled();
});
it("confirmed Desktop deletion uses rotation, never ordinary revoke", async () => {
  renderWithI18n(<TokensTab desktopTokenControl={control} />);
  await openDialog();
  fireEvent.click(screen.getByRole("button", {name:"Regenerate and reconnect"}));
  await waitFor(()=>expect(rotate).toHaveBeenCalledWith("current"));
  expect(revoke).not.toHaveBeenCalled();
});
it("identity failure disables deletion", async () => {
  identify.mockRejectedValue(new Error("offline"));
  renderWithI18n(<TokensTab desktopTokenControl={control} />);
  await waitFor(()=>expect(identify).toHaveBeenCalled());
  expect(revoke).not.toHaveBeenCalled();
  expect(screen.queryByRole("button", {name:/Revoke.*Multica Desktop/i})).not.toBeInTheDocument();
});
it("does not rotate another machine's token with the same name", async () => {
  identify.mockResolvedValue("different-token");
  renderWithI18n(<TokensTab desktopTokenControl={control} />);
  await openDialog();
  expect(screen.queryByText("Regenerate Desktop API token?")).not.toBeInTheDocument();
  expect(rotate).not.toHaveBeenCalled();
});
