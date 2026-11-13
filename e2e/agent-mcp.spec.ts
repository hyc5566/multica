import { test, expect, type Page } from "@playwright/test";
import { TestApiClient } from "./fixtures";
import { waitForPageText } from "./helpers";

// Exercise the current Agent Settings → Apps flow without a live provider.
// Authentication and workspace creation use the real backend; only the
// feature flag, membership role, agent, and Connected Apps APIs are mocked.
const E2E_WORKER =
  process.env.TEST_PARALLEL_INDEX ?? process.env.TEST_WORKER_INDEX ?? "0";
const E2E_RUN_ID =
  process.env.E2E_RUN_ID ?? `${Date.now().toString(36)}-${process.pid.toString(36)}`;
const EMAIL = `e2e-mcp-${E2E_WORKER}-${E2E_RUN_ID}@multica.ai`;
const NAME = "E2E MCP User";
const AGENT_ID = "11111111-1111-4111-8111-111111111111";
const OTHER_USER_ID = "99999999-9999-4999-8999-999999999999";

type Grant = { toolkit_slug: string; account_scope: "personal"; level: "write" };

async function loginCapturingUser(page: Page) {
  const api = new TestApiClient();
  const data = await api.login(EMAIL, NAME);
  const userId: string | undefined = data?.user?.id;
  if (!userId) throw new Error("login did not return a user id");
  const workspace = await api.ensureWorkspace(
    `E2E MCP WS ${E2E_WORKER}`,
    `e2e-mcp-${E2E_WORKER}-${E2E_RUN_ID}`,
  );
  await api.markUserOnboarded();
  const token = api.getToken();
  if (!token) throw new Error("login did not return a token");
  await page.addInitScript((t) => {
    localStorage.setItem("multica_token", t);
    localStorage.setItem("multica:chat:isOpen", "false");
  }, token);
  return { slug: workspace.slug, workspaceId: workspace.id, userId };
}

function mockAgent(ownerId: string, workspaceId: string) {
  return {
    id: AGENT_ID,
    workspace_id: workspaceId,
    runtime_id: "22222222-2222-4222-8222-222222222222",
    name: "MCP Test Agent",
    description: "",
    instructions: "",
    avatar_url: null,
    runtime_mode: "local",
    runtime_config: {},
    custom_args: [],
    visibility: "workspace",
    permission_mode: "private",
    status: "idle",
    max_concurrent_tasks: 1,
    model: "",
    owner_id: ownerId,
    skills: [],
    created_at: "2026-06-30T00:00:00Z",
    updated_at: "2026-06-30T00:00:00Z",
    archived_at: null,
    archived_by: null,
  };
}

async function mockApis(
  page: Page,
  { ownerId, userId, workspaceId, role }: {
    ownerId: string; userId: string; workspaceId: string; role: "member" | "admin";
  },
) {
  let apps: Grant[] = [];
  let captured: unknown;
  await page.route("**/api/config", async (route) => {
    const response = await route.fetch();
    const config = await response.json();
    await route.fulfill({ response, json: {
      ...config,
      feature_flags: { ...config.feature_flags, composio_mcp_apps: true },
    } });
  });
  // Explicitly distinguish a member who owns the agent from a workspace admin
  // editing someone else's agent. Preserve all other membership fields.
  await page.route("**/api/workspaces/*/members", async (route) => {
    const response = await route.fetch();
    const members = await response.json();
    await route.fulfill({ response, json: members.map((member: { user_id: string }) =>
      member.user_id === userId ? { ...member, role } : member,
    ) });
  });
  await page.route("**/api/apps/catalog", (route) => route.fulfill({
    json: { catalog_mode: "all", apps: [
      { slug: "notion", name: "Notion", curated: true },
      { slug: "slack", name: "Slack", curated: true },
    ] },
  }));
  await page.route("**/api/apps", (route) => route.fulfill({
    json: {
      catalog_mode: "all", can_manage: role === "admin", team: [],
      personal: ["notion", "slack"].map((toolkit_slug) => ({
        id: `conn-${toolkit_slug}`, toolkit_slug, status: "active",
        connected_at: "2026-06-30T00:00:00Z", last_used_at: null,
      })),
      agent_apps: apps.map((app) => ({ ...app, agent_id: AGENT_ID })),
    },
  }));
  await page.route(`**/api/agents/${AGENT_ID}/apps`, async (route) => {
    if (route.request().method() === "PUT") {
      captured = route.request().postDataJSON();
      apps = (captured as { apps: Grant[] }).apps;
    }
    await route.fulfill({ json: { apps: apps.map((app) => ({ ...app, agent_id: AGENT_ID })) } });
  });
  await page.route("**/api/agents**", (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    if (request.method() === "GET" && path.endsWith("/api/agents")) {
      return route.fulfill({ json: [mockAgent(ownerId, workspaceId)] });
    }
    if (request.method() === "GET" && path.endsWith(`/api/agents/${AGENT_ID}`)) {
      return route.fulfill({ json: mockAgent(ownerId, workspaceId) });
    }
    return route.fallback();
  });
  return () => captured;
}

test.describe("Agent settings: connected apps", () => {
  for (const scenario of [
    { title: "agent owner can grant app access", owner: true, role: "member" as const },
    { title: "workspace admin can grant access to another owner's agent", owner: false, role: "admin" as const },
  ]) {
    test(scenario.title, async ({ page }) => {
      const { slug, userId, workspaceId } = await loginCapturingUser(page);
      const getRequest = await mockApis(page, {
        ownerId: scenario.owner ? userId : OTHER_USER_ID,
        userId, workspaceId, role: scenario.role,
      });
      await page.goto(`/${slug}/agents/${AGENT_ID}`, { waitUntil: "domcontentloaded" });
      await waitForPageText(page, "MCP Test Agent");
      await page.getByRole("button", { name: "Settings", exact: true }).click();
      const dialog = page.getByRole("dialog");
      await dialog.getByRole("tab", { name: "Apps", exact: true }).click();
      await expect(dialog.getByText("Notion", { exact: true })).toBeVisible();
      await expect(dialog.getByText("Slack", { exact: true })).toBeVisible();
      await dialog.getByRole("switch", { name: "Enable Notion" }).click();
      await expect.poll(getRequest).toEqual({
        apps: [{ toolkit_slug: "notion", account_scope: "personal", level: "write" }],
      });
    });
  }
});
