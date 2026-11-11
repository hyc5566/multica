package daemonupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestUnrecordedDaemonLaunchNeverSpawnsAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name  string
		stale bool
		mode  string
	}{
		{"candidate-missing-pidfile", false, "candidate"}, {"candidate-stale-pidfile", true, "candidate"},
		{"rollback-missing-pidfile", false, "rollback"}, {"rollback-stale-pidfile", true, "rollback"},
		{"rollback-unknown-candidate-missing-pidfile", false, "unknown"}, {"rollback-unknown-candidate-stale-pidfile", true, "unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(cli.TaskConfigRootEnv, root)
			profile := filepath.Join(root, "profiles", "desktop-test")
			work := filepath.Join(root, "zh-tw-update")
			for _, p := range []string{profile, work} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(root, "spawned")
			script := "#!/bin/sh\nprintf spawned > " + marker + "\n"
			j := fixtureJournal("verifying_new")
			j.Profile = "desktop-test"
			j.Token = "token"
			j.GatePath = filepath.Join(profile, "daemon.maintenance")
			j.DaemonPID = 99999990
			j.AppPID = 99999991
			j.OldBundle = filepath.Join(root, "Applications", "Multica.app")
			j.StagedBundle = filepath.Join(work, "staging", "Multica.app")
			j.RollbackBundle = filepath.Join(work, "rollback", "Multica.app")
			j.OlderBundle = filepath.Join(work, "older", "Multica.app")
			j.OldBinarySHA256 = fixtureBundle(t, j.OldBundle, script)
			j.NewBinarySHA256 = fixtureBundle(t, j.StagedBundle, "candidate")
			if tc.mode == "candidate" {
				j.OldBinarySHA256 = fixtureBundle(t, j.RollbackBundle, "old")
				j.NewBinarySHA256 = fixtureBundle(t, j.OldBundle, script)
				j.StartRequested = true
			}
			if tc.mode == "rollback" {
				j.RollbackStartRequested = true
			}
			if tc.mode == "unknown" {
				j.StartRequested = true
			}
			if tc.stale {
				if err := os.WriteFile(filepath.Join(profile, "daemon.pid"), []byte("99999992"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			path := filepath.Join(work, "journal.json")
			if err := acquireGate(path, j); err != nil {
				t.Fatal(err)
			}
			a := newLocalAdapter(path)
			var err error
			if tc.mode == "candidate" {
				err = a.StartAndVerify(context.Background(), j)
			} else {
				err = a.Rollback(context.Background(), j)
			}
			if err == nil || !strings.Contains(err.Error(), "identity was not journalled") {
				t.Fatalf("unexpected result: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("spawned another daemon")
			}
		})
	}
}
