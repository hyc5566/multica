// @vitest-environment node
import { spawnSync } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
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
  appBundlePath,
  DEFAULT_RELEASE_MANIFEST_URL,
  findAppUpdate,
  isNewerZhTWVersion,
  manifestUrlFor,
  parseReleaseManifest,
  platformKey,
  SWAP_SCRIPT,
} from "./zh-tw-app-update";

const sha = "a".repeat(64);
const manifest = (desktop: Record<string, unknown>, version = "0.6.1-zh-tw.2") =>
  JSON.stringify({
    version,
    commit: "b".repeat(40),
    notes_url: "https://github.com/hyc5566/multica/releases/tag/zh-tw-v0.6.1-zh-tw.2",
    cli: { install_url: "https://s90.example/install.sh", install_sha256: sha },
    desktop,
  });
const macAsset = { url: "https://s90.example/app.zip", sha256: sha.toUpperCase(), size: 123 };

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

describe("swap script", () => {
  let root = "";
  afterEach(() => {
    if (root) rmSync(root, { recursive: true, force: true });
  });

  function setup() {
    root = mkdtempSync(join(tmpdir(), "zh-tw-swap-"));
    const bin = join(root, "bin");
    mkdirSync(bin);
    // `open` and `xattr` are macOS tools; record calls instead.
    writeFileSync(join(bin, "open"), `#!/bin/sh\necho "$2" >> "${root}/opened"\n`, { mode: 0o755 });
    writeFileSync(join(bin, "xattr"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
    const script = join(root, "swap.sh");
    writeFileSync(script, SWAP_SCRIPT, { mode: 0o700 });
    const current = join(root, "Applications", "Multica 繁中版.app");
    const next = join(root, "staging", "Multica 繁中版.app");
    mkdirSync(current, { recursive: true });
    mkdirSync(next, { recursive: true });
    writeFileSync(join(current, "version"), "old");
    writeFileSync(join(next, "version"), "new");
    const rollback = join(root, "rollback", "Multica 繁中版.app");
    const env = { ...process.env, PATH: `${bin}:${process.env.PATH}` };
    return { script, current, next, rollback, env };
  }

  it("installs the new bundle and keeps the old one for rollback", () => {
    const { script, current, next, rollback, env } = setup();
    const result = spawnSync("bash", [script, "999999", current, next, rollback, join(root, "swap.log")], { env });
    expect(result.status).toBe(0);
    expect(readFileSync(join(current, "version"), "utf8")).toBe("new");
    expect(readFileSync(join(rollback, "version"), "utf8")).toBe("old");
    expect(readFileSync(join(root, "opened"), "utf8").trim()).toBe(current);
  });

  it("restores the old bundle when the new one cannot be moved in", () => {
    const { script, current, rollback, env } = setup();
    const missing = join(root, "staging", "missing.app");
    const result = spawnSync("bash", [script, "999999", current, missing, rollback, join(root, "swap.log")], { env });
    expect(result.status).toBe(1);
    expect(readFileSync(join(current, "version"), "utf8")).toBe("old");
    expect(existsSync(rollback)).toBe(false);
  });
});
