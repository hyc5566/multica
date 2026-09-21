import { X509Certificate } from "node:crypto";
import { connect, getCACertificates, setDefaultCACertificates } from "node:tls";
import { existsSync, mkdirSync, readFileSync, mkdtempSync, linkSync, renameSync, rmSync } from "node:fs";
import { join } from "node:path";
import type { App } from "electron";
import { acceptCaUpdate, downloadCaManifest, loadCachedCa, parseCaManifest, usableCaCertificates, writeAtomic } from "./lan-ca-update";

const LAN_ORIGIN = "https://10.1.24.90:45671";

// Let Node/OpenSSL validate the entire chain, hostname and validity period.
// Comparing the leaf also binds this check to Chromium's actual connection.
export function verifyLanCertificate(url: string, pem: string, ca: string): Promise<boolean> {
  let target: URL;
  let fingerprint: string;
  try {
    target = new URL(url.replace(/^wss:/, "https:"));
    if (target.origin !== LAN_ORIGIN) return Promise.resolve(false);
    fingerprint = new X509Certificate(pem).fingerprint256;
  } catch {
    return Promise.resolve(false);
  }
  return new Promise((resolve) => {
    const socket = connect({
      host: target.hostname,
      port: Number(target.port),
      ca,
      rejectUnauthorized: true,
    });
    const finish = (ok: boolean) => { socket.destroy(); resolve(ok); };
    socket.setTimeout(5000, () => finish(false));
    socket.once("error", () => finish(false));
    socket.once("secureConnect", () => {
      finish(socket.authorized && socket.getPeerCertificate().fingerprint256 === fingerprint);
    });
  });
}

export async function installLanTrust(app: App, onChanged: () => void = () => {}): Promise<void> {
  const caPath = join(process.resourcesPath, "s90-ca.crt");
  // Generic packages retain OS trust; the zh-tw release script requires this resource.
  if (!existsSync(caPath)) return;
  const bundledCa = readFileSync(caPath, "utf8");
  const publicRoots = getCACertificates("default");
  mkdirSync(app.getPath("userData"), { recursive: true });
  const cache = join(app.getPath("userData"), "s90-ca-manifest.json");
  const bundle = join(app.getPath("userData"), "ca-bundle.crt");
  let manifest = loadCachedCa(cache);
  let ca = "";
  const trustFor = (value = manifest) => {
    const nextCa = [...new Set((value ? usableCaCertificates(value) : [bundledCa]).map((pem) => pem.trim()))].join("\n");
    return { ca: nextCa, roots: nextCa ? [...publicRoots, nextCa] : publicRoots };
  };
  const initial = trustFor();
  writeAtomic(bundle, initial.roots.join("\n"));
  setDefaultCACertificates(initial.roots);
  ca = initial.ca;
  // Child Go CLIs retain public roots as well as the bundled LAN root.
  process.env.SSL_CERT_FILE = bundle;
  process.env.MULTICA_CA_CERT_FILE = bundle;
  process.env.MULTICA_DAEMON_AUTO_UPDATE = "false";
  process.env.MULTICA_DAEMON_AUTO_RELOAD = "false";
  let checking = false;
  let stopped = false;
  const refresh = async () => {
    if (checking || stopped) return;
    checking = true;
    try {
      const next = parseCaManifest(await downloadCaManifest());
      if (stopped || !acceptCaUpdate(manifest, next)) return;
      const previousTrust = trustFor();
      const nextTrust = trustFor(next);
      const previousCa = ca;
      const staging = mkdtempSync(join(app.getPath("userData"), ".ca-update-"));
      let bundleReplaced = false;
      let removeStaging = true;
      try {
        // Stage both files before touching active trust. Hard-link backup allows
        // rollback by rename even if the disk subsequently runs out of space.
        writeAtomic(join(staging, "bundle.crt"), nextTrust.roots.join("\n"));
        writeAtomic(join(staging, "s90-ca-manifest.json"), JSON.stringify(next));
        linkSync(bundle, join(staging, "previous-bundle.crt"));
        setDefaultCACertificates(nextTrust.roots);
        renameSync(join(staging, "bundle.crt"), bundle);
        bundleReplaced = true;
        renameSync(join(staging, "s90-ca-manifest.json"), cache);
        manifest = next;
        ca = nextTrust.ca;
      } catch (error) {
        try {
          if (bundleReplaced) renameSync(join(staging, "previous-bundle.crt"), bundle);
        } catch {
          removeStaging = false; // Keep the original bundle available for recovery.
          console.error("[CA] Could not restore the previous bundle; daemon refresh was not requested");
        } finally {
          setDefaultCACertificates(previousTrust.roots);
          ca = previousTrust.ca;
        }
        throw error;
      } finally {
        try { if (removeStaging) rmSync(staging, { recursive: true, force: true }); }
        catch { console.warn("[CA] Could not remove update staging files"); }
      }
      // Existing daemons have their own TLS pool; apply only at a safe boundary.
      if (previousCa !== ca) onChanged();
    } catch (error) {
      console.warn("[CA] Update not applied:", error instanceof Error ? error.message : "unknown error");
    } finally { checking = false; }
  };
  const timer = setInterval(() => void refresh(), 60 * 60 * 1000);
  timer.unref();
  app.once("before-quit", () => { stopped = true; clearInterval(timer); });
  app.on("certificate-error", (event, _contents, url, _error, certificate, callback) => {
    event.preventDefault();
    void verifyLanCertificate(url, certificate.data, ca).then(callback, () => callback(false));
  });
  // A new OS user may have no cache while the server already uses the next CA.
  await refresh();
  if (!stopped) onChanged(); // Also reconcile a daemon surviving an App restart.
}
