// @vitest-environment node
import { spawn, spawnSync, type ChildProcess } from "node:child_process";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, expect, it } from "vitest";
import { SWAP_SCRIPT } from "./zh-tw-update-swap";

describe("Taiwan app bundle swap", () => {
  let root = "";
  let oldProcess: ChildProcess | undefined;
  afterEach(({ task }) => {
    oldProcess?.kill();
    oldProcess = undefined;
    // Only terminate the fake executable spawned by this test, if still alive.
    const childPid = root && join(root, "child.pid");
    if (childPid && existsSync(childPid)) {
      try { process.kill(Number(readFileSync(childPid, "utf8")), "SIGKILL"); } catch { /* already exited */ }
    }
    if (root && task.result?.state === "fail") {
      const log = join(root, "swap.log");
      console.error(`Swap fixture retained at ${root}\n${existsSync(log) ? readFileSync(log, "utf8") : "No swap log"}`);
    } else if (root) rmSync(root, { recursive: true, force: true });
    root = "";
  });

  function setup(behavior: "ready" | "exit" | "wait" | "ignore-term" = "ready") {
    root = mkdtempSync(join(tmpdir(), "zh-tw 更新 "));
    const bin = join(root, "bin");
    mkdirSync(bin);
    writeFileSync(join(bin, "open"), '#!/bin/sh\nprintf "%s\\n" "$2" >> "$OPENED"\n', { mode: 0o755 });
    writeFileSync(join(bin, "xattr"), "#!/bin/sh\nexit 0\n", { mode: 0o755 });
    const script = join(root, "swap.sh");
    // A fresh macOS executable can take over 100ms to start. Preserve the OS
    // polling cadence while shortening the old App's exit window.
    writeFileSync(script, SWAP_SCRIPT.replace("seq 1 240", "seq 1 2"), { mode: 0o700 });
    const current = join(root, "應用 程式", "Multica 繁中版.app");
    const next = join(root, "staging", "Multica 繁中版.app");
    const rollback = join(root, "rollback", "Multica 繁中版.app");
    const ready = join(root, "ready");
    const result = join(root, "result");
    const executable = "Multica 繁中版";
    mkdirSync(current, { recursive: true });
    mkdirSync(join(next, "Contents", "MacOS"), { recursive: true });
    writeFileSync(join(current, "version"), "old");
    writeFileSync(join(next, "version"), "new");
    const action = behavior === "exit" ? "exit 7" : behavior === "ready"
      ? '[ "$1" = "--zh-tw-update-token=fixture-token" ] || exit 8\ntouch "$READY"\nexec sleep 10'
      : behavior === "ignore-term" ? "trap '' TERM\nexec sleep 10" : "exec sleep 10";
    writeFileSync(join(next, "Contents", "MacOS", executable), `#!/bin/sh\necho $$ > "$CHILD_PID"\n${action}\n`, { mode: 0o755 });
    const env = { ...process.env, PATH: `${bin}:${process.env.PATH}`, READY: ready, CHILD_PID: join(root, "child.pid"), OPENED: join(root, "opened") };
    function run(pid = "99999999", staged = next) {
      const processResult = spawnSync("bash", [script, pid, current, staged, rollback, join(root, "swap.log"), executable, "fixture-token", ready, result, "4"], { env, timeout: 10000 });
      return { status: processResult.status, outcome: readFileSync(result, "utf8").trim() };
    }
    return { current, next, rollback, run };
  }

  it("keeps rollback after the new child confirms readiness", () => {
    const { current, rollback, run } = setup();
    expect(run()).toEqual({ status: 0, outcome: "installed" });
    expect(readFileSync(join(current, "version"), "utf8")).toBe("new");
    expect(readFileSync(join(rollback, "version"), "utf8")).toBe("old");
  });

  it("restores the old bundle when the staged bundle is missing", () => {
    const { current, rollback, run } = setup();
    expect(run("99999999", join(root, "missing.app"))).toEqual({ status: 1, outcome: "restored" });
    expect(readFileSync(join(current, "version"), "utf8")).toBe("old");
    expect(existsSync(rollback)).toBe(false);
    expect(readFileSync(join(root, "opened"), "utf8").trim()).toBe(current);
  });

  it("restores when the new child exits before readiness", () => {
    const { current, run } = setup("exit");
    expect(run()).toEqual({ status: 1, outcome: "restored" });
    const pid = Number(readFileSync(join(root, "child.pid"), "utf8"));
    expect(Number.isInteger(pid) && pid > 0).toBe(true);
    expect(readFileSync(join(current, "version"), "utf8")).toBe("old");
    expect(readFileSync(join(root, "opened"), "utf8").trim()).toBe(current);
  });

  it("stops a child that never reports ready before restoring", () => {
    const { current, run } = setup("wait");
    expect(run()).toEqual({ status: 1, outcome: "restored" });
    expect(readFileSync(join(current, "version"), "utf8")).toBe("old");
    const pid = Number(readFileSync(join(root, "child.pid"), "utf8"));
    expect(() => process.kill(pid, 0)).toThrow();
  });

  it("leaves both bundles in place when the new child ignores termination", () => {
    const { current, rollback, run } = setup("ignore-term");
    expect(run()).toEqual({ status: 1, outcome: "recovery-required" });
    expect(readFileSync(join(current, "version"), "utf8")).toBe("new");
    expect(readFileSync(join(rollback, "version"), "utf8")).toBe("old");
    expect(existsSync(join(root, "opened"))).toBe(false);
  }, 10000);


  it("does not move bundles while the old process remains alive", () => {
    const { current, next, rollback, run } = setup();
    oldProcess = spawn("sleep", ["10"], { stdio: "ignore" });
    expect(run(String(oldProcess.pid))).toEqual({ status: 1, outcome: "quit-timeout" });
    expect(readFileSync(join(current, "version"), "utf8")).toBe("old");
    expect(readFileSync(join(next, "version"), "utf8")).toBe("new");
    expect(existsSync(rollback)).toBe(false);
  });
});
