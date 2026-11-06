import { z } from "zod";
import { X509Certificate } from "node:crypto";
import { get } from "node:https";
import { rootCertificates } from "node:tls";
import { readFileSync, renameSync, writeFileSync } from "node:fs";

// Public Web PKI is the bootstrap trust, independent of the rotating LAN CA.
export const CA_MANIFEST_URL = "https://raw.githubusercontent.com/hyc5566/multica/zh-tw/config/s90-ca-manifest.json";
export const MAX_MANIFEST_BYTES = 64 * 1024;
const caManifestSchema = z.object({
  version: z.number().int().positive().max(Number.MAX_SAFE_INTEGER),
  expiresAt: z.string().datetime({ offset: true }),
  certificates: z.array(z.string()).min(1).max(4),
});
export type CaManifest = z.infer<typeof caManifestSchema>;

export function parseCaManifest(text: string, now = Date.now(), cached = false): CaManifest {
  if (Buffer.byteLength(text) > MAX_MANIFEST_BYTES) throw new Error("CA manifest too large");
  const value = caManifestSchema.parse(JSON.parse(text));
  if (!cached && Date.parse(value.expiresAt) <= now) throw new Error("Expired CA manifest");
  const fingerprints = new Set<string>();
  for (const pem of value.certificates) {
    if (typeof pem !== "string" || !/^-----BEGIN CERTIFICATE-----\s+[A-Za-z0-9+/=\s]+-----END CERTIFICATE-----\s*$/.test(pem)) {
      throw new Error("CA manifest must contain only public certificates");
    }
    const cert = new X509Certificate(pem);
    if (!cert.ca || cert.subject !== cert.issuer || !cert.verify(cert.publicKey) ||
        (!cached && (Date.parse(cert.validFrom) > now || Date.parse(cert.validTo) <= now)) || fingerprints.has(cert.fingerprint256)) {
      throw new Error("Invalid, duplicate or expired CA root");
    }
    fingerprints.add(cert.fingerprint256);
  }
  return { version: value.version, expiresAt: value.expiresAt, certificates: value.certificates };
}

export function downloadCaManifest(): Promise<string> {
  return new Promise((resolve, reject) => {
    const request = get(CA_MANIFEST_URL, { ca: [...rootCertificates], rejectUnauthorized: true }, (response) => {
      // No redirects: never acquire trust from an unexpected origin.
      if (response.statusCode !== 200) {
        request.destroy();
        reject(new Error(`CA manifest HTTP ${response.statusCode}`));
        return;
      }
      const chunks: Buffer[] = [];
      let size = 0;
      response.on("data", (chunk: Buffer) => {
        size += chunk.length;
        if (size > MAX_MANIFEST_BYTES) {
          reject(new Error("CA manifest too large"));
          request.destroy();
        }
        else chunks.push(chunk);
      });
      response.on("end", () => resolve(Buffer.concat(chunks).toString("utf8")));
      response.on("error", reject);
    });
    // Absolute deadline also covers DNS/connect and slow trickle responses.
    const deadline = setTimeout(() => request.destroy(new Error("CA manifest timeout")), 8000);
    request.on("close", () => clearTimeout(deadline));
    request.on("error", reject);
  });
}

export function writeAtomic(path: string, text: string): void {
  const next = `${path}.next`;
  writeFileSync(next, text, { mode: 0o600 });
  renameSync(next, path);
}

export function loadCachedCa(path: string): CaManifest | undefined {
  try { return parseCaManifest(readFileSync(path, "utf8"), Date.now(), true); }
  catch { return undefined; }
}

export function acceptCaUpdate(current: CaManifest | undefined, next: CaManifest): boolean {
  if (current && next.version < current.version) throw new Error("CA manifest rollback rejected");
  if (current && next.version === current.version) {
    if (JSON.stringify(next) !== JSON.stringify(current)) throw new Error("CA manifest version reused");
    return false;
  }
  return true;
}

// Keep the accepted version even when one overlapping root later expires.
export function usableCaCertificates(manifest: CaManifest, now = Date.now()): string[] {
  return manifest.certificates.filter((pem) => {
    const cert = new X509Certificate(pem);
    return Date.parse(cert.validFrom) <= now && Date.parse(cert.validTo) > now;
  });
}
