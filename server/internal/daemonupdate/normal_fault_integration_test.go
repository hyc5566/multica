//go:build linux

package daemonupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestNormalLocalTransactionFaultsRestoreOriginalBundle(t *testing.T) {
	for _, fault := range []string{"hash", "identity", "heartbeat", "daemon-start", "daemon-exit", "app-start", "app-exit", "tampered-failed"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(cli.TaskConfigRootEnv, root)
			pathDir := filepath.Join(root, "path")
			if err := os.MkdirAll(pathDir, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", pathDir)
			profile := filepath.Join(root, "profiles", "desktop-test")
			work := filepath.Join(root, "zh-tw-update")
			for _, p := range []string{profile, work} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "normal"), []byte("1"), 0600); err != nil {
				t.Fatal(err)
			}
			if fault == "hash" || fault == "identity" || fault == "heartbeat" || fault == "tampered-failed" {
				marker := fault
				if fault == "tampered-failed" {
					marker = "hash"
				}
				if err := os.WriteFile(filepath.Join(root, "fault-"+marker), []byte("1"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			server := exec.Command(self, "-test.run=^TestLegacyFixtureServerProcess$")
			server.Env = append(os.Environ(), "MULTICA_LEGACY_FIXTURE_ROOT="+root)
			if err := server.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Process.Kill(); _ = server.Wait() })
			serverPID := server.Process.Pid
			port := 0
			for i := 0; i < 100; i++ {
				data, e := os.ReadFile(filepath.Join(root, "fixture-server-port"))
				if e == nil {
					port, _ = strconv.Atoi(string(data))
					if port > 0 {
						break
					}
				}
				time.Sleep(20 * time.Millisecond)
			}
			if port == 0 {
				t.Fatal("fixture Server did not start")
			}
			oldBundle := filepath.Join(root, "Applications", "Multica.app")
			staged := filepath.Join(work, "staging", "Multica.app")
			pidPath := filepath.Join(profile, "daemon.pid")
			portPath := filepath.Join(profile, "daemon.port")
			oldScript := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$$\" > %q\nprintf '{\"pid\":%%s,\"port\":%d}' \"$$\" > %q\nexec /bin/sleep 120\n", pidPath, port, portPath)
			oldHash := fixtureBundle(t, oldBundle, oldScript)
			newScript := oldScript + "# candidate\n"
			if fault == "daemon-start" {
				newScript = "#!/nonexistent/multica-fixture\n"
			}
			if fault == "daemon-exit" {
				newScript = "#!/bin/sh\nexit 1\n"
			}
			newHash := fixtureBundle(t, staged, newScript)
			if err := os.WriteFile(filepath.Join(staged, "Contents", "Resources", "app.asar"), []byte("candidate resources"), 0600); err != nil {
				t.Fatal(err)
			}
			appPath := filepath.Join(staged, "Contents", "MacOS", "Multica")
			if err := os.MkdirAll(filepath.Dir(appPath), 0700); err != nil {
				t.Fatal(err)
			}
			appScript := "#!/bin/sh\nexec /bin/sleep 120\n"
			if fault == "app-start" {
				appScript = "#!/nonexistent/multica-fixture\n"
			}
			if fault == "app-exit" {
				appScript = "#!/bin/sh\nexit 1\n"
			}
			if err := os.WriteFile(appPath, []byte(appScript), 0700); err != nil {
				t.Fatal(err)
			}
			oldPID := 99999990
			if err := os.WriteFile(pidPath, []byte(strconv.Itoa(oldPID)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(portPath, []byte(fmt.Sprintf(`{"pid":%d,"port":%d}`, oldPID, port)), 0600); err != nil {
				t.Fatal(err)
			}
			token := "11111111-1111-4111-8111-111111111111"
			j := &Journal{Version: 1, Token: token, Phase: "swapping", Profile: "desktop-test", CLIEntry: bundleCLI(oldBundle), DaemonID: "daemon", ServerURLSHA256: fixtureServerHash(), DaemonPID: oldPID, DaemonStartedAt: "old-start", HealthPort: port, AppPID: 99999991, GatePath: filepath.Join(profile, "daemon.maintenance"), OldBundle: oldBundle, StagedBundle: staged, RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app"), OldBinarySHA256: oldHash, NewBinarySHA256: newHash, OldVersion: "0.8.1-zh-tw.4", NewVersion: "0.8.1-zh-tw.5", OldCommit: strings.Repeat("a", 40), NewCommit: strings.Repeat("b", 40), OldDataCompatibility: "multica-profile-v1", DataCompatibility: "multica-profile-v1", RuntimeIDs: []string{"r"}, Workspaces: map[string][]string{"w": {"r"}}, NewAppExecutable: "Multica", ReadyFile: filepath.Join(work, "ready-"+token), HandoffFile: filepath.Join(work, "handoff-"+token)}
			starts := map[int]string{}
			record := func(pid int) {
				if start, e := fixtureProcessStart(pid); e == nil {
					starts[pid] = start
				}
			}
			t.Cleanup(func() {
				for pid, start := range starts {
					if actual, e := fixtureProcessStart(pid); e == nil && actual == start {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			})
			savedInspect, savedStop, savedRequired, savedExists, savedStarted, savedReopen, savedTimeout := inspectLegacy, stopLegacy, requireNativeCandidate, rollbackProcessExists, rollbackProcessStarted, reopenBundle, candidateVerificationTimeout
			defer func() {
				inspectLegacy, stopLegacy, requireNativeCandidate, rollbackProcessExists, rollbackProcessStarted, reopenBundle, candidateVerificationTimeout = savedInspect, savedStop, savedRequired, savedExists, savedStarted, savedReopen, savedTimeout
			}()
			requireNativeCandidate = func(*Journal) bool { return true }
			if fault == "heartbeat" {
				candidateVerificationTimeout = 5 * time.Second
			}
			inspectLegacy = func(pid int) (LegacyProcessIdentity, error) {
				start, e := fixtureProcessStart(pid)
				if e != nil {
					return LegacyProcessIdentity{}, ErrLegacyProcessExited
				}
				if old, ok := starts[pid]; ok && old != start {
					return LegacyProcessIdentity{}, fmt.Errorf("fixture PID reused")
				}
				starts[pid] = start
				hash := j.NewBinarySHA256
				if pid == j.RollbackDaemonPID {
					hash = j.OldBinarySHA256
				}
				return LegacyProcessIdentity{PID: pid, UID: os.Getuid(), StartedAt: start, ExecutablePath: bundleCLI(j.OldBundle), Inode: uint64(pid), SHA256: hash}, nil
			}
			stopLegacy = func(p LegacyProcessIdentity) error {
				actual, e := fixtureProcessStart(p.PID)
				if e != nil || actual != starts[p.PID] {
					return fmt.Errorf("fixture process changed")
				}
				return syscall.Kill(p.PID, syscall.SIGKILL)
			}
			rollbackProcessExists = func(pid int) (bool, error) { _, e := fixtureProcessStart(pid); return e == nil, nil }
			rollbackProcessStarted = record
			reopened := 0
			reopenBundle = func(path string) error {
				if path != oldBundle || verifyBundleHash(path, oldHash) != nil {
					return fmt.Errorf("wrong restored bundle")
				}
				reopened++
				return nil
			}
			path := filepath.Join(work, "journal.json")
			a := newLocalAdapter(path)
			_, j.OldTreeSHA256, _ = legacyTreeManifest(j.OldBundle)
			if err := acquireGate(path, j); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if err := a.SwapBundles(ctx, j); err != nil {
				t.Fatal(err)
			}
			j.Phase = "verifying_new"
			if err := SaveJournal(path, *j); err != nil {
				t.Fatal(err)
			}
			done, err := Advance(ctx, path, j, a)
			if err == nil || done || j.Phase != "rollback_awaiting_acceptance" {
				t.Fatalf("fault did not restore: done=%v err=%v phase=%s", done, err, j.Phase)
			}
			if err := verifyBundleHash(oldBundle, oldHash); err != nil {
				t.Fatal(err)
			}
			if reopened != 1 || j.RollbackDaemonPID <= 0 || j.RollbackDaemonStartedAt == "" {
				t.Fatal("restored identity or App missing")
			}
			if err := syscall.Kill(serverPID, 0); err != nil {
				t.Fatal("fixture Server stopped")
			}
			failed := filepath.Join(work, "failed-"+token+".app")
			if err := verifyBundleHash(failed, newHash); err != nil {
				t.Fatal("failed bundle was not retained before rollback receipt", err)
			}
			if err := os.WriteFile(filepath.Join(root, "receipt"), []byte("1"), 0600); err != nil {
				t.Fatal(err)
			}
			if fault == "tampered-failed" {
				resources := filepath.Join(failed, "Contents", "Resources", "app.asar")
				if err := os.WriteFile(resources, []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
				done, err = Advance(ctx, path, j, a)
				if done || err == nil || j.Phase != "rollback_finalizing" {
					t.Fatalf("tampered cleanup reported success: %v %v %s", done, err, j.Phase)
				}
				if err := verifyBundleHash(failed, newHash); err != nil {
					t.Fatal("tampered fixture changed CLI hash")
				}
				if err := os.WriteFile(resources, []byte("candidate resources"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			done, err = Advance(ctx, path, j, a)
			if err != nil || done || j.Phase != "restored" {
				t.Fatalf("rollback acceptance: %v %v %s", done, err, j.Phase)
			}
			if _, err := os.Stat(failed); !os.IsNotExist(err) {
				t.Fatal("failed bundle remained after rollback receipt")
			}
			if err := syscall.Kill(serverPID, 0); err != nil {
				t.Fatal("fixture Server stopped after rollback acceptance")
			}
		})
	}
}
