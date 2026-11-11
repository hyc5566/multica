//go:build linux

package daemonupdate

import (
	"context"
	"encoding/json"
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

func fixtureProcessStart(pid int) (string, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return "", fmt.Errorf("invalid process stat")
	}
	fields := strings.Fields(string(data[end+2:]))
	if len(fields) <= 19 || fields[0] == "Z" {
		return "", fmt.Errorf("process absent or zombie")
	}
	return fields[19], nil
}

// Real bundles, exchanges, journals and subprocesses; the native macOS process
// and codesign/ditto interfaces are replaced because this fixture runs on Linux.
func TestLegacyLocalTransactionSuccessAndFailures(t *testing.T) {
	for _, fault := range []string{"success", "resume", "hash", "identity", "heartbeat", "daemon-start", "daemon-exit", "app-start", "app-exit", "tampered-rollback"} {
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
			if err := os.MkdirAll(profile, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(work, 0700); err != nil {
				t.Fatal(err)
			}
			oldApp := exec.Command("/bin/sleep", "120")
			oldDaemon := exec.Command("/bin/sleep", "120")
			for _, p := range []*exec.Cmd{oldApp, oldDaemon} {
				if err := p.Start(); err != nil {
					t.Fatal(err)
				}
			}
			var j *Journal
			starts := map[int]string{}
			stopOwned := func(pid int) {
				if started, ok := starts[pid]; ok {
					if actual, err := fixtureProcessStart(pid); err == nil && actual == started {
						_ = syscall.Kill(pid, syscall.SIGKILL)
					}
				}
			}
			t.Cleanup(func() {
				for _, p := range []*exec.Cmd{oldApp, oldDaemon} {
					if p.ProcessState == nil {
						_ = p.Process.Kill()
						_ = p.Wait()
					}
				}
				if j != nil {
					for _, p := range j.Legacy.CandidateProcesses {
						stopOwned(p.PID)
					}
					if j.Legacy.RecoveryDaemon != nil {
						stopOwned(j.Legacy.RecoveryDaemon.PID)
					}
					if j.Legacy.RecoveryApp != nil {
						stopOwned(j.Legacy.RecoveryApp.PID)
					}
				}
			})
			oldBundle := filepath.Join(root, "Applications", "Multica.app")
			staged := filepath.Join(work, "staging", "Multica.app")
			if fault == "hash" || fault == "identity" || fault == "heartbeat" || fault == "tampered-rollback" {
				marker := fault
				if fault == "tampered-rollback" {
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
			for attempts := 0; attempts < 100; attempts++ {
				data, err := os.ReadFile(filepath.Join(root, "fixture-server-port"))
				if err == nil {
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
			pidPath := filepath.Join(profile, "daemon.pid")
			portPath := filepath.Join(profile, "daemon.port")
			if err := os.WriteFile(pidPath, []byte(strconv.Itoa(oldDaemon.Process.Pid)), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(portPath, []byte(fmt.Sprintf(`{"pid":%d,"port":%d}`, oldDaemon.Process.Pid, port)), 0600); err != nil {
				t.Fatal(err)
			}
			observations := filepath.Join(root, "runtime-observations")
			oldScript := fmt.Sprintf("#!/bin/sh\ncase \" $* \" in *' runtime list '*) printf x >> %q; printf '[{\"id\":\"r\",\"daemon_id\":\"daemon\",\"status\":\"online\",\"last_seen_at\":\"%%s\"}]' \"$(/bin/date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)\"; exit 0;; esac\nprintf '%%s' \"$$\" > %q\nprintf '{\"pid\":%%s,\"port\":%d}' \"$$\" > %q\nexec /bin/sleep 120\n", observations, pidPath, port, portPath)
			oldHash := fixtureBundle(t, oldBundle, oldScript)
			if err := os.WriteFile(filepath.Join(oldBundle, "Contents", "Resources", "app.asar"), []byte("approved App contents"), 0600); err != nil {
				t.Fatal(err)
			}
			oldAppPath := filepath.Join(oldBundle, "Contents", "MacOS", "Multica")
			if err := os.MkdirAll(filepath.Dir(oldAppPath), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(oldAppPath, []byte("#!/bin/sh\nexec /bin/sleep 120\n"), 0700); err != nil {
				t.Fatal(err)
			}
			oldAppHash, _ := hashFile(oldAppPath)
			newScript := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$$\" > %q\nprintf '{\"pid\":%%s,\"port\":%d}' \"$$\" > %q\nexec /bin/sleep 120\n", pidPath, port, portPath)
			if fault == "daemon-start" {
				newScript = "#!/nonexistent/multica-fixture\n"
			}
			if fault == "daemon-exit" {
				newScript = "#!/bin/sh\nexit 1\n"
			}
			newHash := fixtureBundle(t, staged, newScript)
			token := "11111111-1111-4111-8111-111111111111"
			ready := filepath.Join(work, "ready-"+token)
			newAppPath := filepath.Join(staged, "Contents", "MacOS", "Multica")
			if err := os.MkdirAll(filepath.Dir(newAppPath), 0700); err != nil {
				t.Fatal(err)
			}
			appScript := fmt.Sprintf("#!/bin/sh\nprintf '{\"token\":\"%s\",\"pid\":%%s}' \"$$\" > %q\nexec /bin/sleep 120\n", token, ready)
			if fault == "app-start" {
				appScript = "#!/nonexistent/multica-fixture\n"
			}
			if fault == "app-exit" {
				appScript = "#!/bin/sh\nexit 1\n"
			}
			if err := os.WriteFile(newAppPath, []byte(appScript), 0700); err != nil {
				t.Fatal(err)
			}
			evidence := filepath.Join(root, "evidence")
			if err := os.WriteFile(evidence, []byte("approved"), 0600); err != nil {
				t.Fatal(err)
			}
			evidenceHash, _ := hashFile(evidence)
			oldAppIdentity := LegacyProcessIdentity{PID: oldApp.Process.Pid, UID: os.Getuid(), StartedAt: "old-app", ExecutablePath: oldAppPath, Inode: 1, SHA256: oldAppHash}
			oldDaemonIdentity := LegacyProcessIdentity{PID: oldDaemon.Process.Pid, UID: os.Getuid(), StartedAt: "old-daemon", ExecutablePath: bundleCLI(oldBundle), Inode: 2, SHA256: oldHash}
			v := LegacyInventory{Profile: "desktop-test", Bundle: oldBundle, CLIEntry: bundleCLI(oldBundle), WorkDir: work, OldVersion: "0.8.1-zh-tw.3", OldBinarySHA256: oldHash, OldDataCompatibility: "multica-profile-v1", CompatibilityEvidenceSHA256: evidenceHash, DaemonID: "daemon", ServerURLSHA256: fixtureServerHash(), Workspaces: map[string][]string{"w": {"r"}}, Daemon: oldDaemonIdentity, Apps: []LegacyProcessIdentity{oldAppIdentity}}
			j = &Journal{Version: 1, Token: token, Phase: "prepared", Profile: v.Profile, CLIEntry: v.CLIEntry, DaemonID: v.DaemonID, ServerURLSHA256: v.ServerURLSHA256, DaemonPID: oldDaemon.Process.Pid, DaemonStartedAt: oldDaemonIdentity.StartedAt, HealthPort: port, AppPID: oldApp.Process.Pid, GatePath: filepath.Join(profile, "daemon.maintenance"), OldBundle: oldBundle, StagedBundle: staged, RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app"), OldBinarySHA256: oldHash, NewBinarySHA256: newHash, OldVersion: v.OldVersion, NewVersion: "0.8.1-zh-tw.4", NewCommit: strings.Repeat("a", 40), OldDataCompatibility: v.OldDataCompatibility, DataCompatibility: v.OldDataCompatibility, RuntimeIDs: []string{"r"}, Workspaces: v.Workspaces, NewAppExecutable: "Multica", ReadyFile: ready, HandoffFile: filepath.Join(work, "handoff-"+token), Legacy: &LegacyState{InventorySHA256: strings.Repeat("a", 64), Inventory: v, CompatibilityEvidencePath: evidence}}
			origTimeout := candidateVerificationTimeout
			if fault == "heartbeat" {
				candidateVerificationTimeout = 5 * time.Second
			}
			defer func() { candidateVerificationTimeout = origTimeout }()
			origInspect, origStop, origList, origCopy, origSignature := inspectLegacy, stopLegacy, enumerateLegacyApps, legacyCopyBundle, legacyVerifySignature
			defer func() {
				inspectLegacy, stopLegacy, enumerateLegacyApps, legacyCopyBundle, legacyVerifySignature = origInspect, origStop, origList, origCopy, origSignature
			}()
			legacyVerifySignature = func(context.Context, string) error { return nil }
			legacyCopyBundle = func(ctx context.Context, from, to string) error {
				return exec.CommandContext(ctx, "/bin/cp", "-a", from, to).Run()
			}
			inspectLegacy = func(pid int) (LegacyProcessIdentity, error) {
				start, err := fixtureProcessStart(pid)
				if err != nil {
					return LegacyProcessIdentity{}, ErrLegacyProcessExited
				}
				if old, ok := starts[pid]; ok && old != start {
					return LegacyProcessIdentity{}, fmt.Errorf("fixture PID reused")
				}
				starts[pid] = start
				if pid == oldApp.Process.Pid {
					return oldAppIdentity, nil
				}
				if pid == oldDaemon.Process.Pid {
					return oldDaemonIdentity, nil
				}
				p := LegacyProcessIdentity{PID: pid, UID: os.Getuid(), StartedAt: "native", Inode: uint64(pid)}
				switch {
				case pid == j.NewDaemonPID:
					p.ExecutablePath = bundleCLI(j.OldBundle)
					p.SHA256 = j.NewBinarySHA256
					if fault == "tampered-rollback" {
						if err := os.WriteFile(filepath.Join(j.RollbackBundle, "Contents", "Resources", "app.asar"), []byte("tampered"), 0600); err != nil {
							t.Fatal(err)
						}
					}
				case pid == j.NewAppPID:
					p.ExecutablePath = filepath.Join(j.OldBundle, "Contents", "MacOS", "Multica")
					p.SHA256, _ = hashFile(p.ExecutablePath)
				case j.Legacy.RecoveryStartRequested && j.Legacy.RecoveryDaemon == nil:
					p.ExecutablePath = bundleCLI(j.OldBundle)
					p.SHA256 = j.OldBinarySHA256
				case j.Legacy.RecoveryDaemon != nil && pid == j.Legacy.RecoveryDaemon.PID:
					return *j.Legacy.RecoveryDaemon, nil
				case j.Legacy.RecoveryAppOpenRequested:
					p.ExecutablePath = oldAppPath
					p.SHA256 = oldAppHash
				default:
					return LegacyProcessIdentity{}, fmt.Errorf("unknown pid")
				}
				return p, nil
			}
			enumerateLegacyApps = func(string) ([]int, error) {
				if oldApp.ProcessState == nil {
					return []int{oldApp.Process.Pid}, nil
				}
				if j.NewAppPID > 0 {
					if _, err := fixtureProcessStart(j.NewAppPID); err == nil {
						return []int{j.NewAppPID}, nil
					}
				}
				if j.Legacy.RecoveryApp != nil {
					return []int{j.Legacy.RecoveryApp.PID}, nil
				}
				return nil, nil
			}
			stopLegacy = func(p LegacyProcessIdentity) error {
				if p.PID == oldApp.Process.Pid {
					_ = oldApp.Process.Kill()
					_ = oldApp.Wait()
					return nil
				}
				if p.PID == oldDaemon.Process.Pid {
					_ = oldDaemon.Process.Kill()
					_ = oldDaemon.Wait()
					return nil
				}
				start, err := fixtureProcessStart(p.PID)
				if err != nil || starts[p.PID] != start {
					return fmt.Errorf("fixture process identity changed")
				}
				if err := syscall.Kill(p.PID, syscall.SIGKILL); err != nil {
					return err
				}
				for i := 0; i < 100; i++ {
					if _, err := fixtureProcessStart(p.PID); err != nil {
						return nil
					}
					time.Sleep(20 * time.Millisecond)
				}
				return fmt.Errorf("fixture process did not exit")
			}
			path := filepath.Join(work, "journal.json")
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			if fault == "resume" {
				a := &legacyLocalAdapter{newLocalAdapter(path)}
				j.Phase = "preparing"
				if err := SaveJournal(path, *j); err != nil {
					t.Fatal(err)
				}
				if err := a.Prepare(ctx, j); err != nil {
					t.Fatal(err)
				}
				j.Phase = "stopping_old"
				if err := SaveJournal(path, *j); err != nil {
					t.Fatal(err)
				}
				if err := a.StopOld(ctx, j); err != nil {
					t.Fatal(err)
				}
				j.Phase = "swapping"
				if err := SaveJournal(path, *j); err != nil {
					t.Fatal(err)
				}
				if err := a.SwapBundles(ctx, j); err != nil {
					t.Fatal(err)
				}
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var recovered Journal
				if err := json.Unmarshal(data, &recovered); err != nil {
					t.Fatal(err)
				}
				j = &recovered
			}
			done, err := AdvanceLegacy(ctx, path, j, &legacyLocalAdapter{newLocalAdapter(path)})
			if err := syscall.Kill(serverPID, 0); err != nil {
				t.Fatalf("fixture Server stopped: %v", err)
			}
			if fault == "tampered-rollback" {
				if err == nil || done || j.Phase != "rolling_back" || !strings.Contains(err.Error(), "tree") {
					t.Fatalf("corruption accepted: %v %v %s", done, err, j.Phase)
				}
				if e := verifyBundleHash(j.RollbackBundle, oldHash); e != nil {
					t.Fatal("test changed the CLI hash")
				}
				if got, e := hashFile(filepath.Join(j.RollbackBundle, "Contents", "MacOS", "Multica")); e != nil || got != oldAppHash {
					t.Fatal("test changed the main App executable")
				}
				if _, e := os.Stat(j.GatePath); e != nil {
					t.Fatal("gate released after corrupted rollback")
				}
				return
			}
			if fault != "success" && fault != "resume" {
				if err == nil || done || j.Phase != "restored_legacy" || !j.Legacy.RecoveryNeedsRedrive {
					t.Fatalf("rollback done=%v err=%v phase=%s", done, err, j.Phase)
				}
				if e := verifyBundleHash(oldBundle, oldHash); e != nil {
					t.Fatal(e)
				}
				data, e := os.ReadFile(observations)
				if e != nil || len(data) < 4 {
					t.Fatalf("insufficient restored heartbeat observations: %d %v", len(data), e)
				}
				if j.Legacy.RecoveryDaemon == nil || j.Legacy.RecoveryDaemon.SHA256 != oldHash || j.Legacy.RecoveryApp == nil || j.Legacy.RecoveryApp.SHA256 != oldAppHash || !j.Legacy.PossibleMissedClaims {
					t.Fatal("restored identity or re-drive boundary missing")
				}
				if _, e := os.Stat(j.GatePath); !os.IsNotExist(e) {
					t.Fatal("legacy recovery gate not released")
				}
				if _, e := os.Stat(filepath.Join(work, "ledger.json")); !os.IsNotExist(e) {
					t.Fatal("ledger written after legacy recovery")
				}
				return
			}
			if err != nil || done || j.Phase != "awaiting_acceptance" {
				t.Fatalf("pending done=%v err=%v phase=%s", done, err, j.Phase)
			}
			if err := os.WriteFile(filepath.Join(root, "receipt"), []byte("1"), 0600); err != nil {
				t.Fatal(err)
			}
			done, err = AdvanceLegacy(ctx, path, j, &legacyLocalAdapter{newLocalAdapter(path)})
			if err != nil || !done || j.Phase != "complete" {
				t.Fatalf("finish done=%v err=%v phase=%s", done, err, j.Phase)
			}
			if err := verifyBundleHash(j.RollbackBundle, oldHash); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(work, "baseline")); !os.IsNotExist(err) {
				t.Fatal("baseline duplicate remained after legacy acceptance")
			}
			if _, err := os.Stat(filepath.Join(work, "legacy-coordinator")); !os.IsNotExist(err) {
				t.Fatal("retained coordinator remained after legacy acceptance")
			}
			ledgerBytes, err := os.ReadFile(filepath.Join(work, "ledger.json"))
			if err != nil {
				t.Fatal(err)
			}
			var ledger struct {
				Previous map[string]string `json:"previous"`
				Possible bool              `json:"legacy_possible_missed_claims"`
			}
			if json.Unmarshal(ledgerBytes, &ledger) != nil || ledger.Previous["binary_sha256"] != oldHash || ledger.Previous["commit"] != "" || !ledger.Possible {
				t.Fatal("legacy ledger did not preserve the approved predecessor and claim gap")
			}
			if err := syscall.Kill(serverPID, 0); err != nil {
				t.Fatalf("fixture Server stopped after acceptance: %v", err)
			}
			if fault == "success" {
				testLegacyThenNormalRetention(t, ctx, root, work, profile, port, serverPID, j)
			}
		})
	}
}

func testLegacyThenNormalRetention(t *testing.T, ctx context.Context, root, work, profile string, port, serverPID int, legacy *Journal) {
	t.Helper()
	for _, p := range legacy.Legacy.CandidateProcesses {
		if _, err := fixtureProcessStart(p.PID); err == nil {
			if err := stopLegacy(p); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := os.Remove(filepath.Join(root, "receipt")); err != nil {
		t.Fatal(err)
	}
	staged := filepath.Join(work, "staging", "Next.app")
	pHash := fixtureBundle(t, staged, "next version CLI")
	proc := exec.Command("/bin/sleep", "120")
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Process.Kill(); _ = proc.Wait() })
	if err := os.WriteFile(filepath.Join(profile, "daemon.pid"), []byte(strconv.Itoa(proc.Process.Pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "daemon.port"), []byte(fmt.Sprintf(`{"pid":%d,"port":%d}`, proc.Process.Pid, port)), 0600); err != nil {
		t.Fatal(err)
	}
	token := "22222222-2222-4222-8222-222222222222"
	j := &Journal{Version: 1, Token: token, Phase: "swapping", Profile: legacy.Profile, CLIEntry: legacy.CLIEntry, DaemonID: legacy.DaemonID, ServerURLSHA256: legacy.ServerURLSHA256, DaemonPID: 99999990, NewDaemonPID: proc.Process.Pid, NewDaemonStartedAt: fmt.Sprintf("start-%d", proc.Process.Pid), HealthPort: port, AppPID: 99999991, GatePath: legacy.GatePath, OldBundle: legacy.OldBundle, StagedBundle: staged, RollbackBundle: legacy.RollbackBundle, OlderBundle: legacy.OlderBundle, OldBinarySHA256: legacy.NewBinarySHA256, NewBinarySHA256: pHash, PreviousRollbackSHA256: legacy.OldBinarySHA256, OldVersion: legacy.NewVersion, NewVersion: "0.8.1-zh-tw.5", OldCommit: legacy.NewCommit, NewCommit: strings.Repeat("c", 40), OldDataCompatibility: legacy.DataCompatibility, DataCompatibility: legacy.DataCompatibility, RuntimeIDs: legacy.RuntimeIDs, Workspaces: legacy.Workspaces}
	path := filepath.Join(work, "journal.json")
	a := newLocalAdapter(path)
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	if err := a.SwapBundles(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundleHash(j.OlderBundle, legacy.OldBinarySHA256); err != nil {
		t.Fatal("previous version was removed before acceptance", err)
	}
	if err := releaseGate(j); err != nil {
		t.Fatal(err)
	}
	j.GateReleasedAt = time.Now().UTC().Add(-time.Second)
	j.Phase = "finalizing"
	if err := SaveJournal(path, *j); err != nil {
		t.Fatal(err)
	}
	if err := a.FinalizeRetention(ctx, j); err == nil {
		t.Fatal("normal cleanup succeeded without a receipt")
	}
	if err := verifyBundleHash(j.OlderBundle, legacy.OldBinarySHA256); err != nil {
		t.Fatal("previous version was removed without receipt", err)
	}
	if err := os.WriteFile(filepath.Join(root, "receipt"), []byte("1"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.FinalizeRetention(ctx, j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(j.OlderBundle); !os.IsNotExist(err) {
		t.Fatal("third version remained after acceptance")
	}
	if err := verifyBundleHash(j.OldBundle, pHash); err != nil {
		t.Fatal(err)
	}
	if err := verifyBundleHash(j.RollbackBundle, legacy.NewBinarySHA256); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Kill(serverPID, 0); err != nil {
		t.Fatal("fixture Server stopped during second update")
	}
}
