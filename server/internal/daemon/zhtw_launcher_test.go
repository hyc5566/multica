package daemon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// The zh-tw Desktop swaps its bundle, and the CLI this daemon runs from, in
// place. With its opt-in the daemon follows the new binary through the
// idle-only, claim-barrier self-reload, and never pulls a release itself.
func TestAutoUpdateLoop_ZhTWDesktopFollowsBundledBinary(t *testing.T) {
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

	// Busy: the reload waits.
	d.activeTasks.Store(1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { d.autoUpdateLoop(ctx); close(done) }()
	time.Sleep(30 * time.Millisecond)
	if restartCalls.Load() != 0 {
		t.Fatal("restarted while a task was running")
	}
	// Idle: it restarts into the new binary.
	d.activeTasks.Store(0)
	waitFor(t, func() bool { return restartCalls.Load() == 1 }, "no restart once idle")
	cancel()
	<-done
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
