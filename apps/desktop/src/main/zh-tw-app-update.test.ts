// @vitest-environment node
import { createHash } from "node:crypto";
import { chmodSync, existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it, vi } from "vitest";

vi.mock("electron", () => ({
  app: {},
  dialog: {},
  Notification: {},
  shell: {},
}));

import {
  acknowledgeUpdateReady,
  appBundlePath,
  downloadVerified,
  fetchManifest,
  bundleNotUpdatableReason,
  DEFAULT_RELEASE_MANIFEST_URL,
  findAppUpdate,
  isNewerZhTWVersion,
  manifestUrlFor,
  parseReleaseManifest,
  platformKey,
} from "./zh-tw-app-update";

const sha = "a".repeat(64);
const manifest = (desktop: Record<string, unknown>, version = "0.6.1-zh-tw.2") =>
  JSON.stringify({
    version,
    source_id: "hyc5566/multica",
    data_compatibility: "multica-profile-v1",
    coordinated_update_protocol: 1,
    commit: "b".repeat(40),
    notes_url: "https://github.com/hyc5566/multica/releases/tag/zh-tw-v0.6.1-zh-tw.2",
    cli: { install_url: "https://s90.example/install.sh", install_sha256: sha },
    desktop: {},
    coordinated_desktop: desktop,
  });
const macAsset = { url: "https://s90.example/app.zip", sha256: sha.toUpperCase(), size: 123, binary_sha256: sha, binary_version: "0.6.1-zh-tw.2", binary_commit: "b".repeat(40), data_compatibility: "multica-profile-v1", bundle_id: "ai.multica.desktop.zh-tw.local" };

describe("zh-tw app update channel", () => {
  it("compares only Taiwan release versions", () => {
    expect(isNewerZhTWVersion("0.6.1-zh-tw.10", "0.6.1-zh-tw.9")).toBe(true);
    expect(isNewerZhTWVersion("0.7.0-zh-tw.1", "0.6.9-zh-tw.4")).toBe(true);
    expect(isNewerZhTWVersion("0.6.1-zh-tw.1", "0.6.1-zh-tw.1")).toBe(false);
    expect(isNewerZhTWVersion("0.6.1-zh-tw.1", "0.6.1-zh-tw.2")).toBe(false);
    // Builds from source report git-describe versions and never auto-update.
    expect(isNewerZhTWVersion("0.6.1-zh-tw.1", "0.6.0-131-g2ca7196c3b")).toBe(false);
  });

  it("offers the Apple Silicon package when the channel is newer", () => {
    const parsed = parseReleaseManifest(manifest({ "darwin-arm64": macAsset }));
    expect(findAppUpdate(parsed, "0.6.1-zh-tw.1", "darwin-arm64")).toEqual({
      version: "0.6.1-zh-tw.2",
      url: macAsset.url,
      sha256: sha,
      size: 123,
      notesUrl: "https://github.com/hyc5566/multica/releases/tag/zh-tw-v0.6.1-zh-tw.2",
      binarySha256: sha,
      binaryVersion: "0.6.1-zh-tw.2",
      binaryCommit: "b".repeat(40),
      dataCompatibility: "multica-profile-v1",
      bundleId: "ai.multica.desktop.zh-tw.local",
    });
    expect(findAppUpdate(parsed, "0.6.1-zh-tw.2", "darwin-arm64")).toBeNull();
    expect(findAppUpdate(parsed, "0.6.1-zh-tw.1", null)).toBeNull();
    expect(findAppUpdate(parseReleaseManifest(manifest({})), "0.6.1-zh-tw.1", "darwin-arm64")).toBeNull();
  });

  it("rejects unsafe or malformed channel documents", () => {
    expect(() => parseReleaseManifest(manifest({ "darwin-arm64": { ...macAsset, url: "http://s90.example/a.zip" } }))).toThrow();
    expect(() => parseReleaseManifest(manifest({ "darwin-arm64": { ...macAsset, sha256: "short" } }))).toThrow();
    expect(() => parseReleaseManifest(manifest({ "darwin-arm64": macAsset }, "0.6.1"))).toThrow();
    expect(() => parseReleaseManifest("x".repeat(70 * 1024))).toThrow(/too large/);
  });

  it("refuses an old manifest lacking coordinated binary evidence", () => {
    const parsed = parseReleaseManifest(manifest({ "darwin-arm64": { url: macAsset.url, size: 123, sha256: sha } }));
    expect(() => findAppUpdate(parsed, "0.6.1-zh-tw.1", "darwin-arm64")).toThrow(/整組更新/);
    const legacy = parseReleaseManifest(JSON.stringify({ version: "0.6.1-zh-tw.2", desktop: { "darwin-arm64": macAsset } }));
    expect(() => findAppUpdate(legacy, "0.6.1-zh-tw.1", "darwin-arm64")).toThrow(/不支援整組協調更新/);
  });

  it("follows the connected server and falls back to s90", () => {
    expect(manifestUrlFor("https://mlc.example:8443/api")).toBe("https://mlc.example:8443/downloads/latest.json");
    expect(manifestUrlFor(null)).toBe(DEFAULT_RELEASE_MANIFEST_URL);
    expect(manifestUrlFor("http://localhost:8080")).toBe(DEFAULT_RELEASE_MANIFEST_URL);
  });

  it("recognizes only Apple Silicon bundles", () => {
    expect(platformKey("darwin", "arm64")).toBe("darwin-arm64");
    expect(platformKey("darwin", "x64")).toBeNull();
    expect(platformKey("linux", "x64")).toBeNull();
    expect(appBundlePath("/Applications/Multica 繁中版.app/Contents/MacOS/Multica 繁中版")).toBe("/Applications/Multica 繁中版.app");
    expect(appBundlePath("/opt/multica/multica")).toBeNull();
  });
});

describe("bundle location", () => {
  it("refuses translocated, mounted and read-only bundles before downloading", async () => {
    expect(await bundleNotUpdatableReason("/private/var/folders/x/AppTranslocation/ABC/d/Multica 繁中版.app")).toMatch(/下載項目/);
    expect(await bundleNotUpdatableReason("/Volumes/Multica/Multica 繁中版.app")).toMatch(/下載項目/);
    const dir = mkdtempSync(join(tmpdir(), "zh-tw-bundle-"));
    try {
      expect(await bundleNotUpdatableReason(join(dir, "Multica 繁中版.app"))).toBeNull();
      chmodSync(dir, 0o555);
      if (process.getuid?.() !== 0) {
        expect(await bundleNotUpdatableReason(join(dir, "Multica 繁中版.app"))).toMatch(/沒有權限/);
      }
    } finally {
      chmodSync(dir, 0o755);
      rmSync(dir, { recursive: true, force: true });
    }
  });
});


describe("update download and readiness", () => {
  let dir = "";
  afterEach(() => {
    vi.unstubAllGlobals();
    if (dir) rmSync(dir, { recursive: true, force: true });
    dir = "";
  });
  function fixture() {
    dir = mkdtempSync(join(tmpdir(), "zh-tw-download-"));
    const contents = "test app archive";
    return {
      contents,
      path: join(dir, "下載.zip"),
      update: { version: "0.6.1-zh-tw.2", url: "https://example.test/app.zip", size: Buffer.byteLength(contents), sha256: createHash("sha256").update(contents).digest("hex") },
    };
  }
  it("downloads and validates content with redirects disabled and progress below 100", async () => {
    const { contents, path, update } = fixture();
    const request = vi.fn().mockResolvedValue(new Response(contents));
    vi.stubGlobal("fetch", request);
    const progress = vi.fn();
    await downloadVerified(update, path, progress, new AbortController().signal);
    expect(readFileSync(path, "utf8")).toBe(contents);
    expect(request).toHaveBeenCalledWith(update.url, expect.objectContaining({ redirect: "error" }));
    expect(progress).toHaveBeenCalled();
    expect(progress.mock.calls.every(([value]) => value >= 0 && value <= 99)).toBe(true);
    expect(progress).toHaveBeenLastCalledWith(99);
  });
  it.each([
    ["short", "tiny", undefined, /大小或 SHA-256/],
    ["oversized", "test app archive extra", undefined, /超過/],
    ["wrong digest", "test app archive", "0".repeat(64), /大小或 SHA-256/],
  ])("rejects %s downloads", async (_name, body, digest, error) => {
    const { path, update } = fixture();
    vi.stubGlobal("fetch", vi.fn().mockResolvedValue(new Response(body)));
    await expect(downloadVerified({ ...update, sha256: digest ?? update.sha256 }, path, vi.fn(), new AbortController().signal)).rejects.toThrow(error);
  });
  it("propagates offline and abort failures", async () => {
    const { path, update } = fixture();
    vi.stubGlobal("fetch", vi.fn().mockRejectedValue(new Error("offline")));
    await expect(downloadVerified(update, path, vi.fn(), new AbortController().signal)).rejects.toThrow("offline");
    const controller = new AbortController();
    controller.abort();
    vi.stubGlobal("fetch", vi.fn().mockImplementation((_url, options: RequestInit) => {
      options.signal?.throwIfAborted();
      return Promise.resolve(new Response("test app archive"));
    }));
    await expect(downloadVerified(update, path, vi.fn(), controller.signal)).rejects.toMatchObject({ name: "AbortError" });
  });
  it("rejects oversized streamed manifests and HTTP errors", async () => {
    const request = vi.fn().mockResolvedValueOnce(new Response(new ReadableStream({
      start(controller) {
        controller.enqueue(new Uint8Array(32768));
        controller.enqueue(new Uint8Array(32769));
        controller.close();
      },
    }))).mockResolvedValueOnce(new Response("error", { status: 503 }));
    vi.stubGlobal("fetch", request);
    const signal = new AbortController().signal;
    await expect(fetchManifest("https://example.test/latest.json", signal)).rejects.toThrow(/too large/);
    await expect(fetchManifest("https://example.test/latest.json", signal)).rejects.toThrow(/HTTP 503/);
    expect(request).toHaveBeenCalledWith("https://example.test/latest.json", expect.objectContaining({ redirect: "error" }));
  });
  it("does not acknowledge a legacy renderer-only update", async () => {
    fixture();
    const token = "f6f5f574-5dea-42c2-91c1-7bc09ca9da51";
    const version = "0.6.1-zh-tw.2";
    const bundle = "/Applications/Multica 繁中版.app";
    const argv = [`--zh-tw-update-token=${token}`];
    const ready = join(dir, `ready-${token}`);
    // No token must not try to read even a nonexistent pending record.
    await expect(acknowledgeUpdateReady(join(dir, "missing"), version, bundle, [])).resolves.toBeUndefined();
    writeFileSync(join(dir, "pending-launch.json"), JSON.stringify({ token, version, bundle }));
    await acknowledgeUpdateReady(dir, version, bundle, ["--zh-tw-update-token=f6f5f574-5dea-42c2-91c1-7bc09ca9da52"]);
    await acknowledgeUpdateReady(dir, "0.6.1-zh-tw.3", bundle, argv);
    await acknowledgeUpdateReady(dir, version, "/Applications/Other.app", argv);
    expect(existsSync(ready)).toBe(false);
    await acknowledgeUpdateReady(dir, version, bundle, argv);
    expect(existsSync(ready)).toBe(false);
  });
});
