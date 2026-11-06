import { createHash, randomUUID } from "crypto";
import { link, mkdir, readFile, rm, writeFile } from "fs/promises";
import { hostname, homedir } from "os";
import { join } from "path";

export interface MintedDesktopToken {
  id: string;
  token: string;
}

export interface DesktopTokenOwner {
  machine: string;
  userId: string;
  serverUrl: string;
}

export interface DesktopTokenRecord extends DesktopTokenOwner {
  id: string;
  digest: string;
}

// Credential provenance is independent of the daemon's machine/runtime ID.
// A copied profile cannot claim ownership on another Desktop installation.
export async function desktopTokenMachine(): Promise<string> {
  const dir = join(homedir(), ".multica");
  const path = join(dir, "desktop-installation.id");
  await mkdir(dir, { recursive: true });
  // Publish a complete UUID so concurrent Desktop processes cannot read an
  // empty file between its exclusive creation and write.
  try {
    await readFile(path, "utf8");
  } catch (error) {
    if ((error as NodeJS.ErrnoException).code !== "ENOENT") throw error;
    const id = randomUUID();
    const temporary = join(dir, ".desktop-installation-" + id + ".tmp");
    await writeFile(temporary, id, { flag: "wx", mode: 0o600 });
    try {
      await link(temporary, path);
    } catch (error) {
      if ((error as NodeJS.ErrnoException).code !== "EEXIST") throw error;
    } finally {
      await rm(temporary, { force: true });
    }
  }
  const installation = (await readFile(path, "utf8")).trim();
  if (!/^[0-9a-f]{8}(?:-[0-9a-f]{4}){3}-[0-9a-f]{12}$/i.test(installation)) {
    throw new Error("Desktop installation identity is invalid");
  }
  return `${hostname()}:${installation}`;
}

export function desktopTokenRecord(
  minted: MintedDesktopToken,
  owner: DesktopTokenOwner,
): DesktopTokenRecord {
  return {
    ...owner,
    id: minted.id,
    digest: createHash("sha256").update(minted.token).digest("hex"),
  };
}

export function ownedDesktopTokenId(
  record: unknown,
  token: unknown,
  owner: DesktopTokenOwner,
): string | null {
  if (!record || typeof record !== "object" ||
    typeof token !== "string" || !token.startsWith("mul_")) return null;
  const value = record as Partial<DesktopTokenRecord>;
  if (typeof value.id !== "string" || !value.id ||
    value.machine !== owner.machine || value.userId !== owner.userId ||
    value.serverUrl !== owner.serverUrl ||
    value.digest !== createHash("sha256").update(token).digest("hex")) return null;
  return value.id;
}

// Renderer credentials (including pasted PATs) authorize a mint; they never
// establish ownership. Only a recorded local mint can be reused or rotated.
export async function selectDesktopToken(
  cached: unknown,
  record: unknown,
  owner: DesktopTokenOwner,
  probe: () => Promise<string>,
  mint: () => Promise<MintedDesktopToken>,
): Promise<MintedDesktopToken> {
  const id = ownedDesktopTokenId(record, cached, owner);
  if (id && await probe() !== "auth_expired") return { id, token: cached as string };
  return mint();
}
