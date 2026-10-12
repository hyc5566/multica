package daemon

import (
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
