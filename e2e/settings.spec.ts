import { test, expect } from "@playwright/test";
import { appRail, loginAsDefault } from "./helpers";

test.describe("Settings", () => {
  test("updating workspace name reflects in the rail immediately", async ({
    page,
  }) => {
    const workspaceSlug = await loginAsDefault(page);

    await page.goto(`/${workspaceSlug}/settings?tab=workspace`, { waitUntil: "domcontentloaded" });
    const nameInput = page.getByRole("textbox", { name: "Name", exact: true });
    await expect(nameInput).toBeVisible();
    const originalName = await nameInput.inputValue();
    const switcher = appRail(page).getByRole("button", { name: "Switch workspace" });
    const newName = "Renamed WS " + Date.now();

    // Workspace details save on blur; the switcher must update without reload.
    await nameInput.fill(newName);
    await nameInput.blur();
    await switcher.click();
    await expect(page.getByRole("menuitem", { name: newName })).toBeVisible();
    await page.keyboard.press("Escape");

    await nameInput.fill(originalName);
    await nameInput.blur();
    await switcher.click();
    await expect(page.getByRole("menuitem", { name: originalName })).toBeVisible();

  });

  // Keep the hosted provider boundary mocked while exercising the current
  // Connected apps page, callback redirect, toast, and refreshed account list.
  test("connecting an app shows a toast and refreshes the account list", async ({ page }) => {
    const workspaceSlug = await loginAsDefault(page);
    let connected = false;
    let requestBody: unknown;

    await page.route("**/api/config", async (route) => {
      const response = await route.fetch();
      const config = await response.json();
      await route.fulfill({ response, json: {
        ...config,
        feature_flags: { ...config.feature_flags, composio_mcp_apps: true },
      } });
    });
    // The settings navigation checks provider availability independently.
    await page.route("**/api/integrations/composio/toolkits", (route) =>
      route.fulfill({ json: [{ slug: "notion", name: "Notion", connectable: true }] }),
    );
    await page.route("**/api/apps/catalog", (route) => route.fulfill({
      json: { catalog_mode: "all", apps: [{ slug: "notion", name: "Notion", curated: true }] },
    }));
    await page.route("**/api/apps", (route) => route.fulfill({
      json: {
        catalog_mode: "all", can_manage: true, team: [], agent_apps: [],
        personal: connected ? [{
          id: "conn-notion-1", toolkit_slug: "notion", status: "active",
          connected_at: "2026-06-30T00:00:00Z", last_used_at: null,
        }] : [],
      },
    }));
    await page.route("**/api/apps/personal/connect", (route) => {
      requestBody = route.request().postDataJSON();
      connected = true;
      // The backend callback returns this slug-less URL; the proxy supplies
      // the current workspace before the Apps page consumes the parameter.
      return route.fulfill({ json: { redirect_url: "/settings?connected=notion&tab=apps" } });
    });

    await page.goto(`/${workspaceSlug}/settings?tab=apps`, { waitUntil: "domcontentloaded" });
    await page.getByRole("button", { name: "Connect my account" }).click();
    const dialog = page.getByRole("dialog");
    const notion = dialog.getByRole("listitem").filter({ hasText: "Notion" });
    await expect(notion).toBeVisible();
    await notion.getByRole("button", { name: "Connect", exact: true }).click();
    await expect.poll(() => requestBody).toEqual({ toolkit_slug: "notion" });
    await expect(page.getByText("Account connected", { exact: true })).toBeVisible({ timeout: 10000 });
    await expect(page.getByText("Notion", { exact: true })).toBeVisible();
    await expect(page.getByRole("button", { name: /Disconnect/ }).first()).toBeVisible();
    await expect(page).not.toHaveURL(/connected=notion/);
  });
});
