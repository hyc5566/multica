package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestRestartTargetPrefersZhTWLauncher(t *testing.T) {
	d, _ := newSelfReloadTestDaemon(t, "0.6.1-zh-tw.1")
	launcher := filepath.Join(t.TempDir(), "multica")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env bash\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv(cli.ZhTWLauncherEnv, launcher)

	got, err := d.restartTargetBinary()
	if err != nil || got != launcher {
		t.Fatalf("restartTargetBinary = %q, %v; want launcher %q", got, err, launcher)
	}
}

func TestRestartTargetIgnoresMissingZhTWLauncher(t *testing.T) {
	d, _ := newSelfReloadTestDaemon(t, "0.6.1-zh-tw.1")
	t.Setenv(cli.ZhTWLauncherEnv, filepath.Join(t.TempDir(), "missing"))

	got, err := d.restartTargetBinary()
	if err != nil || filepath.Base(got) != "multica" || got == os.Getenv(cli.ZhTWLauncherEnv) {
		t.Fatalf("restartTargetBinary = %q, %v; want the resolved executable", got, err)
	}
}

func TestAutoUpdateLoop_ZhTWDesktopRequiresCoordinatedUpdate(t *testing.T) {
	shortenLoopTimers(t)
	d, restartCalls := newSelfReloadTestDaemon(t, "0.6.1-zh-tw.1")
	d.cfg.LaunchedBy = "desktop"
	d.cfg.AutoUpdateEnabled = true
	d.runUpdateFn = func(string) (string, error) {
		t.Fatal("Desktop-managed daemon must not pull a release")
		return "", nil
	}
	stubSelfVersion(t, "0.6.1-zh-tw.2", nil)
	t.Setenv("MULTICA_ZH_TW_DESKTOP_SELF_RELOAD", "1")

	d.autoUpdateLoop(context.Background())
	if restartCalls.Load() != 0 {
		t.Fatal("Desktop must not reload outside its transaction")
	}
}

func TestZhTWStandaloneRefusesSelfUpdateAndReload(t *testing.T) {
	d, restarts := newSelfReloadTestDaemon(t, "0.6.1-zh-tw.1")
	d.cfg.AutoUpdateEnabled = true
	probes := stubSelfVersion(t, "0.6.1-zh-tw.2", nil)
	d.autoUpdateLoop(context.Background())
	if probes.Load() != 0 || restarts.Load() != 0 {
		t.Fatal("standalone Taiwan daemon must be updated by verified procedure")
	}
	if _, err := d.runUpdate("0.6.1-zh-tw.2"); err == nil {
		t.Fatal("installer update unexpectedly allowed")
	}
}

func TestAutoUpdateLoop_DesktopWithoutOptInStillSkips(t *testing.T) {
	shortenLoopTimers(t)
	d, restartCalls := newSelfReloadTestDaemon(t, "0.6.1-zh-tw.1")
	d.cfg.LaunchedBy = "desktop"
	versionProbes := stubSelfVersion(t, "0.6.1-zh-tw.2", nil)
	t.Setenv("MULTICA_ZH_TW_DESKTOP_SELF_RELOAD", "")
	d.autoUpdateLoop(context.Background()) // returns immediately
	if restartCalls.Load() != 0 || versionProbes.Load() != 0 {
		t.Fatal("Desktop-managed daemon without the opt-in must not reload")
	}
}

func TestZhTWDesktopSelfReloadCapability(t *testing.T) {
	d := &Daemon{cfg: Config{LaunchedBy: "desktop", AutoReloadEnabled: true}}
	t.Setenv("MULTICA_ZH_TW_DESKTOP_SELF_RELOAD", "1")
	d.cfg.CLIVersion = "0.6.1-zh-tw.1"
	if d.zhTWDesktopSelfReload() {
		t.Fatal("Taiwan Desktop must not report independent reload")
	}
	d.cfg.AutoReloadEnabled = false
	if d.zhTWDesktopSelfReload() {
		t.Fatal("auto-reload turned off: the daemon cannot follow the bundle")
	}
	d.cfg = Config{AutoReloadEnabled: true}
	if d.zhTWDesktopSelfReload() {
		t.Fatal("a standalone daemon is not Desktop-managed")
	}
	t.Setenv("MULTICA_ZH_TW_DESKTOP_SELF_RELOAD", "")
	d.cfg = Config{LaunchedBy: "desktop", AutoReloadEnabled: true}
	if d.zhTWDesktopSelfReload() {
		t.Fatal("no opt-in, no capability")
	}
}
