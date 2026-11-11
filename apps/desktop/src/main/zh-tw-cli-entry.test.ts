// @vitest-environment node
import { mkdtemp, mkdir, rm, symlink, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, expect, it } from "vitest";
import { verifyCLIEntries } from "./zh-tw-cli-entry";

let root = "";
afterEach(async () => { if (root) await rm(root, { recursive: true, force: true }); root = ""; });

it("accepts a matching App entry and rejects independent PATH and managed copies", async () => {
  root = await mkdtemp(join(tmpdir(), "cli-entry-"));
  const app = join(root, "App.app", "multica");
  const pathDir = join(root, "path");
  const managed = join(root, "managed", "multica");
  await mkdir(join(root, "App.app")); await mkdir(pathDir); await mkdir(join(root, "managed"));
  await writeFile(app, "app");
  await verifyCLIEntries(app, app, managed, pathDir, "linux");
  await symlink(app, join(pathDir, "multica"));
  await verifyCLIEntries(app, join(pathDir, "multica"), managed, pathDir, "linux");
  await writeFile(managed, "independent");
  await expect(verifyCLIEntries(app, app, managed, pathDir, "linux")).rejects.toThrow(/獨立 CLI/);
  await rm(managed); await rm(join(pathDir, "multica")); await writeFile(join(pathDir, "multica"), "independent");
  await expect(verifyCLIEntries(app, app, managed, pathDir, "linux")).rejects.toThrow(/獨立 CLI/);
});
