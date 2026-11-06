// @vitest-environment node
import { EventEmitter } from "node:events";
import { rootCertificates } from "node:tls";
import { X509Certificate } from "node:crypto";
import { describe, it, expect, vi } from "vitest";

const { connect } = vi.hoisted(() => ({ connect: vi.fn() }));
vi.mock("node:tls", async (original) => ({ ...await original<typeof import("node:tls")>(), connect }));
import { verifyLanCertificate } from "./lan-trust";

const cert = rootCertificates[0]!;
function socket(authorized: boolean, fingerprint = new X509Certificate(cert).fingerprint256) {
  const stream = Object.assign(new EventEmitter(), {
    authorized, destroy: vi.fn(), setTimeout: vi.fn(),
    getPeerCertificate: () => ({ fingerprint256: fingerprint }),
  });
  connect.mockReturnValue(stream);
  return stream;
}

describe("bundled LAN trust", () => {
  it("requires verified TLS and the same leaf Chromium received", async () => {
    const stream = socket(true);
    const result = verifyLanCertificate("https://10.1.24.90:45671/api/me", cert, cert);
    stream.emit("secureConnect");
    expect(await result).toBe(true);
    expect(connect).toHaveBeenLastCalledWith({ host: "10.1.24.90", port: 45671, ca: cert, rejectUnauthorized: true });
    const changed = socket(true, "different-leaf");
    const mismatch = verifyLanCertificate("wss://10.1.24.90:45671/ws", cert, cert);
    changed.emit("secureConnect");
    expect(await mismatch).toBe(false);
  });
  it("denies failed chains, connection errors, timeouts and unrelated origins", async () => {
    for (const event of ["secureConnect", "error", "timeout"]) {
      const stream = socket(false);
      const result = verifyLanCertificate("https://10.1.24.90:45671", cert, cert);
      if (event === "timeout") stream.setTimeout.mock.calls[0]![1]();
      else stream.emit(event);
      expect(await result).toBe(false);
    }
    connect.mockClear();
    expect(await verifyLanCertificate("https://example.com", cert, cert)).toBe(false);
    expect(await verifyLanCertificate("https://10.1.24.90:45673", cert, cert)).toBe(false);
    expect(await verifyLanCertificate("https://10.1.24.90:45671", "invalid", cert)).toBe(false);
    expect(connect).not.toHaveBeenCalled();
  });
});
