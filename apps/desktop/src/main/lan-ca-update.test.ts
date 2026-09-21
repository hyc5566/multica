// @vitest-environment node
import { EventEmitter } from "node:events";
import { mkdtempSync, readFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { rootCertificates } from "node:tls";
import { X509Certificate } from "node:crypto";
import { afterEach, describe, expect, it, vi } from "vitest";
const { get } = vi.hoisted(() => ({ get: vi.fn() }));
vi.mock("node:https", () => ({ get }));
import { acceptCaUpdate, CA_MANIFEST_URL, downloadCaManifest, loadCachedCa, MAX_MANIFEST_BYTES, parseCaManifest, usableCaCertificates, writeAtomic } from "./lan-ca-update";
const cert = rootCertificates.find((pem) => {
  const c = new X509Certificate(pem);
  return c.ca && c.subject === c.issuer && c.verify(c.publicKey) && Date.parse(c.validFrom) < Date.now() && Date.parse(c.validTo) > Date.now() + 86400000;
})!;
const value = { version: 1, expiresAt: new Date(Date.now() + 86400000).toISOString(), certificates: [cert] };
afterEach(() => vi.useRealTimers());

describe("CA manifest trust boundary", () => {
  it("accepts public roots, rejects invalid input and expired remote manifests", () => {
    expect(parseCaManifest(JSON.stringify(value))).toEqual(value);
    for (const candidate of [null, {}, {...value,version:0}, {...value,version:1.5}, {...value,expiresAt:"2000-01-01T00:00:00Z"}, {...value,certificates:[]}, {...value,certificates:[cert,cert]}, {...value,certificates:["-----BEGIN PRIVATE KEY-----"]}, {...value,certificates:[cert+cert]}]) {
      expect(() => parseCaManifest(JSON.stringify(candidate))).toThrow();
    }
    expect(() => parseCaManifest(" ".repeat(MAX_MANIFEST_BYTES + 1))).toThrow();
    const future = Date.parse(new X509Certificate(cert).validTo) + 1;
    const cached = parseCaManifest(JSON.stringify(value), future, true);
    expect(cached.version).toBe(1);
    expect(usableCaCertificates(cached, future)).toEqual([]);
  });
  it("keeps verified cached trust offline and rejects rollback or reused versions", () => {
    const dir = mkdtempSync(join(tmpdir(), "ca-cache-"));
    try {
      const path = join(dir, "manifest.json");
      expect(loadCachedCa(path)).toBeUndefined();
      writeAtomic(path, JSON.stringify({...value,expiresAt:"2000-01-01T00:00:00Z"}));
      expect(loadCachedCa(path)?.version).toBe(1);
      expect(JSON.parse(readFileSync(path,"utf8")).certificates).toEqual([cert]);
      expect(acceptCaUpdate(value, value)).toBe(false);
      expect(acceptCaUpdate(value, {...value, version:2})).toBe(true);
      expect(() => acceptCaUpdate({...value,version:2}, value)).toThrow("rollback");
      expect(() => acceptCaUpdate(value, {...value,expiresAt:"2030-01-01"})).toThrow("reused");
    } finally { rmSync(dir, {recursive:true,force:true}); }
  });
  it("uses only public TLS roots, refuses redirects and bounds response size/time", async () => {
    vi.useFakeTimers();
    const request = Object.assign(new EventEmitter(), {destroy:vi.fn(function(this: EventEmitter, error?: Error) { if(error) this.emit("error",error); this.emit("close"); })});
    get.mockReturnValue(request);
    const run = (status: number) => {
      const promise = downloadCaManifest();
      const response = Object.assign(new EventEmitter(),{statusCode:status});
      get.mock.lastCall![2](response);
      return {promise,response};
    };
    const ok = run(200);
    expect(get.mock.lastCall![0]).toBe(CA_MANIFEST_URL);
    expect(get.mock.lastCall![1]).toEqual({ca:[...rootCertificates],rejectUnauthorized:true});
    ok.response.emit("data",Buffer.from(JSON.stringify(value))); ok.response.emit("end"); request.emit("close");
    expect(parseCaManifest(await ok.promise)).toEqual(value);
    const redirect = run(302); await expect(redirect.promise).rejects.toThrow("HTTP 302");
    const large = run(200); large.response.emit("data",Buffer.alloc(MAX_MANIFEST_BYTES+1));
    await expect(large.promise).rejects.toThrow("too large");
    const pending = downloadCaManifest(); const check = expect(pending).rejects.toThrow("timeout");
    await vi.advanceTimersByTimeAsync(8000); await check;
  });
});
