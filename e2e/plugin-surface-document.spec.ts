import { expect, test } from "@playwright/test";
import { buildSurfaceFrameDocument } from "../packages/views/plugins/surface-document";

test.describe("hosted plugin document lifecycle", () => {
  test("a host-authored frame reload reconnects without reporting hostile navigation", async ({ page }) => {
    await page.route("https://plugin-content.example.test/**", (route) => route.fulfill({
      contentType: "text/html",
      body: `<!doctype html><script>
        const channel = new MessageChannel();
        parent.postMessage({ type: 'multica:plugin-bridge-connect', version: 2, challenge: 'proof' }, '*', [channel.port1]);
      </script>`,
    }));
    await page.setContent(`<!doctype html><script>
      window.surfaceState = { connected: 0, terminal: 0 };
      addEventListener('message', event => {
        if (event.data?.type === 'multica:plugin-bridge-connect') window.surfaceState.connected++;
        if (['multica:plugin-surface-navigated', 'multica:plugin-surface-navigation-blocked'].includes(event.data?.type)) window.surfaceState.terminal++;
      });
    </script><iframe id="surface" sandbox="allow-scripts allow-same-origin"></iframe>`);
    for (const name of ["first", "second"]) {
      const document = buildSurfaceFrameDocument({ url: `https://plugin-content.example.test/${name}`, bridgeToken: "proof" });
      await page.locator("#surface").evaluate((frame, srcdoc) => {
        (frame as HTMLIFrameElement).srcdoc = srcdoc;
      }, document);
      await expect.poll(() => page.evaluate(() =>
        (window as unknown as { surfaceState: { connected: number } }).surfaceState.connected,
      )).toBe(name === "first" ? 1 : 2);
    }
    expect(await page.evaluate(() =>
      (window as unknown as { surfaceState: { terminal: number } }).surfaceState.terminal,
    )).toBe(0);
  });

  test("relays a hosted document's bootstrap failure through its outer frame", async ({ page }) => {
    await page.route("https://plugin-content.example.test/**", (route) => route.fulfill({
      contentType: "text/html",
      body: `<!doctype html><script>parent.postMessage({type: 'multica:plugin-surface-error'}, '*');</script>`,
    }));
    await page.setContent(`<!doctype html><script>
      window.surfaceErrors = 0;
      addEventListener('message', event => {
        if (event.data?.type === 'multica:plugin-surface-error') window.surfaceErrors++;
      });
    </script><iframe id="surface" sandbox="allow-scripts allow-same-origin"></iframe>`);
    const document = buildSurfaceFrameDocument({ url: "https://plugin-content.example.test/error", bridgeToken: "proof" });
    await page.locator("#surface").evaluate((frame, srcdoc) => {
      (frame as HTMLIFrameElement).srcdoc = srcdoc;
    }, document);
    await expect.poll(() => page.evaluate(() =>
      (window as unknown as { surfaceErrors: number }).surfaceErrors,
    )).toBe(1);
  });
});
