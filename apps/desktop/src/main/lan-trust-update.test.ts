// @vitest-environment node
import { EventEmitter } from "node:events";
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { X509Certificate } from "node:crypto";
import { rootCertificates } from "node:tls";
import type { App } from "electron";
import { afterEach, expect, it, vi } from "vitest";
const { download, setTrust, failCache } = vi.hoisted(() => ({ download: vi.fn(), setTrust:vi.fn(), failCache: { once: false } }));
vi.mock("./lan-ca-update", async (original) => {
  const module = await original<typeof import("./lan-ca-update")>();
  return {...module, downloadCaManifest:download, writeAtomic:(path:string, text:string) => {
    if(failCache.once && path.endsWith("s90-ca-manifest.json")) { failCache.once=false; throw new Error("disk failure"); }
    module.writeAtomic(path,text);
  }};
});
vi.mock("node:tls", async (original) => ({...await original<typeof import("node:tls")>(),getCACertificates:()=>[],setDefaultCACertificates:setTrust}));
import { installLanTrust } from "./lan-trust";
const roots = rootCertificates.filter((pem) => {
  const c = new X509Certificate(pem);
  return c.ca && c.subject === c.issuer && c.verify(c.publicKey) && Date.parse(c.validFrom)<Date.now() && Date.parse(c.validTo)>Date.now()+86400000;
}).slice(0,2);
afterEach(() => { vi.unstubAllEnvs(); vi.useRealTimers(); vi.restoreAllMocks(); });

it("refreshes per-user trust atomically, preserves offline trust, avoids redundant restart and stops on quit", async () => {
  vi.useFakeTimers();
  const dir = mkdtempSync(join(tmpdir(),"lan-trust-"));
  const original = Object.getOwnPropertyDescriptor(process,"resourcesPath");
  Object.defineProperty(process,"resourcesPath",{value:dir,configurable:true});
  const app = Object.assign(new EventEmitter(),{getPath:()=>dir});
  const changed = vi.fn();
  for (const name of ["SSL_CERT_FILE","MULTICA_CA_CERT_FILE","MULTICA_DAEMON_AUTO_UPDATE","MULTICA_DAEMON_AUTO_RELOAD"]) vi.stubEnv(name,process.env[name]);
  const manifest = (version:number, certificates=roots) => JSON.stringify({version,expiresAt:new Date(Date.now()+86400000).toISOString(),certificates});
  const warning = vi.spyOn(console,"warn").mockImplementation(()=>{});
  try {
    writeFileSync(join(dir,"s90-ca.crt"),roots[0]!);
    download.mockResolvedValueOnce(manifest(1,[roots[0]!]));
    installLanTrust(app as unknown as App,changed);
    await vi.advanceTimersByTimeAsync(0);
    expect(changed).toHaveBeenCalledTimes(1); // Startup checks a surviving daemon by digest.
    failCache.once=true;
    download.mockResolvedValueOnce(manifest(2));
    await vi.advanceTimersByTimeAsync(3600000);
    expect(JSON.parse(readFileSync(join(dir,"s90-ca-manifest.json"),"utf8")).version).toBe(1);
    expect(readFileSync(join(dir,"ca-bundle.crt"),"utf8")).not.toContain(roots[1]!.trim());
    expect(setTrust.mock.lastCall![0].join("\n")).not.toContain(roots[1]!.trim());
    download.mockResolvedValueOnce(manifest(2));
    await vi.advanceTimersByTimeAsync(3600000);
    expect(changed).toHaveBeenCalledTimes(2);
    const bundle = join(dir,"ca-bundle.crt");
    expect(process.env.MULTICA_CA_CERT_FILE).toBe(bundle);
    expect(readFileSync(bundle,"utf8")).toContain(roots[1]!.trim());
    const saved = readFileSync(bundle,"utf8");
    download.mockRejectedValueOnce(new Error("offline"));
    await vi.advanceTimersByTimeAsync(3600000);
    expect(readFileSync(bundle,"utf8")).toBe(saved);
    expect(warning).toHaveBeenCalled();
    download.mockResolvedValueOnce(manifest(1,[roots[0]!]));
    await vi.advanceTimersByTimeAsync(3600000);
    expect(readFileSync(bundle,"utf8")).toBe(saved);
    expect(changed).toHaveBeenCalledTimes(2);
    app.emit("before-quit");
    const calls=download.mock.calls.length;
    await vi.advanceTimersByTimeAsync(3600000);
    expect(download).toHaveBeenCalledTimes(calls);
    expect(setTrust.mock.lastCall![0].join("\n")).toContain(roots[1]!.trim());
  } finally {
    app.emit("before-quit");
    if(original) Object.defineProperty(process,"resourcesPath",original); else Reflect.deleteProperty(process,"resourcesPath");
    rmSync(dir,{recursive:true,force:true});
  }
});

it("waits for bootstrap and lets an authoritative manifest retire the bundled root", async () => {
  const dir=mkdtempSync(join(tmpdir(),"ca-bootstrap-"));
  const original=Object.getOwnPropertyDescriptor(process,"resourcesPath");
  Object.defineProperty(process,"resourcesPath",{value:dir,configurable:true});
  const app=Object.assign(new EventEmitter(),{getPath:()=>dir});
  for(const name of ["SSL_CERT_FILE","MULTICA_CA_CERT_FILE","MULTICA_DAEMON_AUTO_UPDATE","MULTICA_DAEMON_AUTO_RELOAD"]) vi.stubEnv(name,process.env[name]);
  let finish!: (value:string)=>void;
  download.mockReturnValueOnce(new Promise<string>((resolve)=>{finish=resolve;}));
  try {
    writeFileSync(join(dir,"s90-ca.crt"),roots[0]!);
    let ready=false;
    const startup=installLanTrust(app as unknown as App).then(()=>{ready=true;});
    await Promise.resolve();
    expect(ready).toBe(false);
    finish(JSON.stringify({version:2,expiresAt:new Date(Date.now()+86400000).toISOString(),certificates:[roots[1]]}));
    await startup;
    const bundle=readFileSync(join(dir,"ca-bundle.crt"),"utf8");
    expect(bundle).toContain(roots[1]!.trim());
    expect(bundle).not.toContain(roots[0]!.trim());
  } finally {
    app.emit("before-quit");
    if(original) Object.defineProperty(process,"resourcesPath",original); else Reflect.deleteProperty(process,"resourcesPath");
    rmSync(dir,{recursive:true,force:true});
  }
});

it("keeps the rollback floor when cached roots expire and can bootstrap a newer root", async () => {
  vi.useFakeTimers();
  const validRoots=rootCertificates.map(pem=>({pem,cert:new X509Certificate(pem)})).filter(({cert})=>cert.ca&&cert.subject===cert.issuer&&cert.verify(cert.publicKey));
  validRoots.sort((a,b)=>Date.parse(a.cert.validTo)-Date.parse(b.cert.validTo));
  const old=validRoots[0]!, next=validRoots.at(-1)!;
  const now=Math.max(Date.now(),Date.parse(old.cert.validTo)+1000);
  vi.setSystemTime(now);
  const dir=mkdtempSync(join(tmpdir(),"ca-expired-"));
  const original=Object.getOwnPropertyDescriptor(process,"resourcesPath");
  Object.defineProperty(process,"resourcesPath",{value:dir,configurable:true});
  const app=Object.assign(new EventEmitter(),{getPath:()=>dir});
  for(const name of ["SSL_CERT_FILE","MULTICA_CA_CERT_FILE","MULTICA_DAEMON_AUTO_UPDATE","MULTICA_DAEMON_AUTO_RELOAD"]) vi.stubEnv(name,process.env[name]);
  setTrust.mockImplementation((list:string[])=>{if(list.some(pem=>pem==="")) throw new Error("empty PEM");});
  const warning=vi.spyOn(console,"warn").mockImplementation(()=>{});
  try {
    writeFileSync(join(dir,"s90-ca.crt"),old.pem);
    writeFileSync(join(dir,"s90-ca-manifest.json"),JSON.stringify({version:5,expiresAt:"2000-01-01T00:00:00Z",certificates:[old.pem]}));
    download.mockResolvedValueOnce(JSON.stringify({version:4,expiresAt:new Date(now+86400000).toISOString(),certificates:[next.pem]}));
    await installLanTrust(app as unknown as App);
    expect(warning).toHaveBeenCalledWith(expect.any(String),expect.stringContaining("rollback"));
    expect(JSON.parse(readFileSync(join(dir,"s90-ca-manifest.json"),"utf8")).version).toBe(5);
    download.mockResolvedValueOnce(JSON.stringify({version:6,expiresAt:new Date(now+86400000).toISOString(),certificates:[next.pem]}));
    await vi.advanceTimersByTimeAsync(3600000);
    expect(readFileSync(join(dir,"ca-bundle.crt"),"utf8")).toContain(next.pem.trim());
  } finally {
    app.emit("before-quit");
    if(original) Object.defineProperty(process,"resourcesPath",original); else Reflect.deleteProperty(process,"resourcesPath");
    rmSync(dir,{recursive:true,force:true});
  }
});
