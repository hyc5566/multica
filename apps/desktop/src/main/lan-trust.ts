import { X509Certificate } from "node:crypto";
import { connect, getCACertificates, setDefaultCACertificates } from "node:tls";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import type { App } from "electron";

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

export function installLanTrust(app: App): void {
  const caPath = join(process.resourcesPath, "s90-ca.crt");
  // Generic packages retain OS trust; the zh-tw release script requires this resource.
  if (!existsSync(caPath)) return;
  const ca = readFileSync(caPath, "utf8");
  const roots = [...getCACertificates("default"), ca];
  setDefaultCACertificates(roots);
  mkdirSync(app.getPath("userData"), { recursive: true });
  const bundle = join(app.getPath("userData"), "ca-bundle.crt");
  writeFileSync(bundle, roots.join("\n"), { mode: 0o600 });
  // Child Go CLIs retain public roots as well as the bundled LAN root.
  process.env.SSL_CERT_FILE = bundle;
  process.env.MULTICA_DAEMON_AUTO_UPDATE = "false";
  process.env.MULTICA_DAEMON_AUTO_RELOAD = "false";
  app.on("certificate-error", (event, _contents, url, _error, certificate, callback) => {
    event.preventDefault();
    void verifyLanCertificate(url, certificate.data, ca).then(callback, () => callback(false));
  });
}
