// @vitest-environment node
import { describe, expect, it, vi } from "vitest";
import {
  desktopTokenRecord,
  ownedDesktopTokenId,
  selectDesktopToken,
} from "./desktop-token";

const owner = { machine: "host-a:installation-a", userId: "user", serverUrl: "https://api.test" };
const original = { id: "token-a", token: "mul_12345678_original" };
const replacement = { id: "token-b", token: "mul_12345678_replacement" };

describe("Desktop token provenance", () => {
  it("identifies only the complete locally minted credential, never its shared prefix", () => {
    const record = desktopTokenRecord(original, owner);
    expect(ownedDesktopTokenId(record, original.token, owner)).toBe(original.id);
    expect(ownedDesktopTokenId(record, replacement.token, owner)).toBeNull();
    expect(ownedDesktopTokenId(null, original.token, owner)).toBeNull();
    expect(ownedDesktopTokenId({ id: original.id }, original.token, owner)).toBeNull();
  });

  it("does not claim a copied profile, another account, or another server", () => {
    const record = desktopTokenRecord(original, owner);
    for (const changed of [
      { ...owner, machine: "host-b:installation-b" },
      { ...owner, machine: "host-b:installation-a" },
      { ...owner, userId: "other-user" },
      { ...owner, serverUrl: "https://another.test" },
    ]) expect(ownedDesktopTokenId(record, original.token, changed)).toBeNull();
  });

  it("reuses this installation's mint across login and inconclusive network probes", async () => {
    const mint = vi.fn(async () => replacement);
    for (const status of ["ok", "unknown"]) {
      expect(await selectDesktopToken(original.token, desktopTokenRecord(original, owner), owner,
        async () => status, mint)).toEqual(original);
    }
    expect(mint).not.toHaveBeenCalled();
  });

  it("replaces a revoked local credential exactly once", async () => {
    const mint = vi.fn(async () => replacement);
    expect(await selectDesktopToken(original.token, desktopTokenRecord(original, owner), owner,
      async () => "auth_expired", mint)).toEqual(replacement);
    expect(mint).toHaveBeenCalledTimes(1);
  });

  it("mints for legacy or externally pasted credentials without adopting or revoking them", async () => {
    const probe = vi.fn(async () => "ok");
    const mint = vi.fn(async () => replacement);
    expect(await selectDesktopToken(original.token, null, owner, probe, mint)).toEqual(replacement);
    expect(probe).not.toHaveBeenCalled();
    expect(mint).toHaveBeenCalledTimes(1);
    // The mint result is immediately reusable; rotation saves this result
    // directly instead of feeding it back as an external login credential.
    expect(await selectDesktopToken(replacement.token, desktopTokenRecord(replacement, owner),
      owner, probe, mint)).toEqual(replacement);
    expect(mint).toHaveBeenCalledTimes(1);
  });

  it("mints separately when the same external PAT is supplied on two machines", async () => {
    const a = vi.fn(async () => original);
    const b = vi.fn(async () => replacement);
    const external = "mul_shared_external";
    const tokenA = await selectDesktopToken(external, null, owner, async () => "ok", a);
    const tokenB = await selectDesktopToken(external, null,
      { ...owner, machine: "host-b:installation-b" }, async () => "ok", b);
    expect(tokenA.token).not.toBe(tokenB.token);
    expect(tokenA.token).not.toBe(external);
    expect(tokenB.token).not.toBe(external);
  });
});
