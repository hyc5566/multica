package daemonupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// A restarted daemon must be reauthenticated and prove new work before an
// interrupted acceptance can finish. The HTTP endpoint and daemon.port are
// real, while process inspection is a platform boundary fixture.
func TestAcceptanceAfterRestartRequiresNewEvidence(t *testing.T) {
	for _, phase := range []string{"awaiting_acceptance", "finalizing", "rollback_awaiting_acceptance", "rollback_finalizing", "legacy", "pid_reuse"} {
		t.Run(phase, func(t *testing.T) { testAcceptanceAfterRestart(t, phase) })
	}
}

func testAcceptanceAfterRestart(t *testing.T, phase string) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "update")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{Phase: "awaiting_acceptance", Profile: "test", DaemonID: "daemon", DaemonPID: 100, NewDaemonPID: 123, NewAppPID: 122, NewDaemonStartedAt: "old-start", GatePath: filepath.Join(profile, "daemon.maintenance"), GateReleaseRequested: true, GateIdentity: "original-gate", GateReleasedAt: time.Now().Add(-time.Hour), ServerURLSHA256: fixtureServerHash(), OldVersion: "0.8.1-zh-tw.4", NewVersion: "0.8.1-zh-tw.5", OldCommit: "old", NewCommit: "new", DataCompatibility: "multica-profile-v1", OldDataCompatibility: "multica-profile-v1", RuntimeIDs: []string{"r"}, Workspaces: map[string][]string{"w": {"r"}}, OldBundle: filepath.Join(root, "Applications", "Multica.app"), RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), StagedBundle: filepath.Join(work, "staging", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app")}
	rollback := phase == "rollback_awaiting_acceptance" || phase == "rollback_finalizing"
	j.Phase = phase
	if phase == "pid_reuse" {
		j.Phase = "awaiting_acceptance"
	}
	if phase == "legacy" {
		j.Phase = "awaiting_acceptance"
		j.Legacy = &LegacyState{CandidateProcesses: []LegacyProcessIdentity{{PID: 123, UID: os.Getuid(), StartedAt: "old-native", ExecutablePath: bundleCLI(j.OldBundle), Inode: 12, SHA256: "old-candidate"}}}
	}
	if rollback {
		j.RollbackDaemonPID, j.RollbackDaemonStartedAt = 123, "old-start"
		j.RollbackGateReleaseRequested, j.RollbackGateReleasedAt = true, j.GateReleasedAt
		j.OldBinarySHA256 = fixtureBundle(t, j.OldBundle, "old")
		j.NewBinarySHA256 = fixtureBundle(t, j.StagedBundle, "new")
		_, j.OldTreeSHA256, _ = legacyTreeManifest(j.OldBundle)
		_, j.CandidateTreeSHA256, _ = legacyTreeManifest(j.StagedBundle)
	} else {
		j.NewBinarySHA256 = fixtureBundle(t, j.OldBundle, "new")
		j.OldBinarySHA256 = fixtureBundle(t, j.RollbackBundle, "old")
		_, j.OldTreeSHA256, _ = legacyTreeManifest(j.RollbackBundle)
		_, j.CandidateTreeSHA256, _ = legacyTreeManifest(j.OldBundle)
	}
	version, commit, activeHash := j.NewVersion, j.NewCommit, j.NewBinarySHA256
	if rollback {
		version, commit, activeHash = j.OldVersion, j.OldCommit, j.OldBinarySHA256
	}
	inspect, list, signature, executable := inspectLegacy, enumerateLegacyApps, legacyVerifySignature, abortAppExecutable
	defer func() {
		inspectLegacy, enumerateLegacyApps, legacyVerifySignature, abortAppExecutable = inspect, list, signature, executable
	}()
	daemonPID, daemonStarted, daemonUID := 124, "new-start", os.Getuid()
	ambiguousApp, badHash := false, false
	if phase == "pid_reuse" {
		j.DaemonPID = daemonPID
	}
	inspectLegacy = func(pid int) (LegacyProcessIdentity, error) {
		switch pid {
		case 123:
			return LegacyProcessIdentity{}, ErrLegacyProcessExited
		case 124, 126:
			if badHash {
				return LegacyProcessIdentity{PID: pid, UID: daemonUID, StartedAt: "native", ExecutablePath: bundleCLI(j.OldBundle), Inode: 2, SHA256: "incorrect"}, nil
			}
			if pid != daemonPID {
				return LegacyProcessIdentity{}, ErrLegacyProcessExited
			}
			return LegacyProcessIdentity{PID: pid, UID: daemonUID, StartedAt: "native-" + daemonStarted, ExecutablePath: bundleCLI(j.OldBundle), Device: 1, Inode: 2, SHA256: activeHash}, nil
		case 125, 127:
			return LegacyProcessIdentity{PID: pid, UID: os.Getuid(), StartedAt: "native-app", ExecutablePath: filepath.Join(j.OldBundle, "Contents", "MacOS", "Multica"), Device: 1, Inode: 3, SHA256: "app-hash"}, nil
		default:
			return LegacyProcessIdentity{}, ErrLegacyProcessExited
		}
	}
	enumerateLegacyApps = func(string) ([]int, error) {
		if ambiguousApp {
			return []int{125, 127}, nil
		}
		return []int{125}, nil
	}
	legacyVerifySignature = func(context.Context, string) error { return nil }
	abortAppExecutable = func(context.Context, string) (string, error) { return "Multica", nil }
	beat := 0
	fresh := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		receipts := []any{}
		completed := time.Now().Add(-time.Hour / 2)
		if fresh {
			completed = time.Now()
		}
		receipts = append(receipts, map[string]any{"task_id": "task", "runtime_id": "r", "completed_at": completed, "ack_at": completed.Add(time.Second)})
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": daemonPID, "os": "darwin", "profile": "test", "daemon_id": "daemon", "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": version, "cli_commit": commit, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}, "maintenance_gate": map[string]any{"supported": true, "blocked": false, "path": j.GatePath}, "update_attestation": map[string]any{"binary_sha256": activeHash, "process_started_at": daemonStarted, "runtime_heartbeat_acks": map[string]int{"r": beat}, "task_receipts": receipts}})
	}))
	defer srv.Close()
	_, port, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	j.HealthPort, _ = strconv.Atoi(port)
	writePort := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(profile, "daemon.port"), []byte(fmt.Sprintf(`{"port":%d,"pid":%d}`, j.HealthPort, daemonPID)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writePort()
	a := newLocalAdapter(filepath.Join(work, "journal.json"))
	accept := a.ReceiptAccepted
	if rollback {
		accept = a.RollbackReceiptAccepted
	}
	daemonUID = os.Getuid() + 1
	if ok, err := accept(context.Background(), j); err == nil || ok {
		t.Fatal("wrong daemon UID accepted")
	}
	daemonUID = os.Getuid()
	badHash = true
	if ok, err := accept(context.Background(), j); err == nil || ok {
		t.Fatal("wrong mapped hash accepted")
	}
	badHash = false
	ambiguousApp = true
	if ok, err := accept(context.Background(), j); err == nil || ok {
		t.Fatal("ambiguous App accepted")
	}
	ambiguousApp = false
	// Reauthentication must stay pending with an old receipt and fewer than
	// three distinct heartbeat observations, then accept a fresh receipt.
	for beat = 0; beat <= 3; beat++ {
		ok, err := accept(context.Background(), j)
		if err != nil || ok {
			t.Fatalf("heartbeat %d, stale receipt: accepted=%v err=%v", beat, ok, err)
		}
	}
	// A coordinator crash reloads only the durable epoch and observations.
	data, err := os.ReadFile(a.journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var recovered Journal
	if err := json.Unmarshal(data, &recovered); err != nil {
		t.Fatal(err)
	}
	*j = recovered
	if ok, err := accept(context.Background(), j); err != nil || ok {
		t.Fatalf("stale receipt after journal reload: %v %v", ok, err)
	}
	if j.NewAppPID != 125 {
		t.Fatalf("new App was not authenticated: %d", j.NewAppPID)
	}
	// A second daemon restart invalidates all previous observations and receipts.
	daemonPID, daemonStarted, beat, fresh = 126, "second-start", 0, true
	writePort()
	for beat = 0; beat <= 2; beat++ {
		ok, err := accept(context.Background(), j)
		if err != nil || ok {
			t.Fatalf("second restart heartbeat %d: accepted=%v err=%v", beat, ok, err)
		}
	}
	beat = 3
	ok, err := accept(context.Background(), j)
	if err != nil || !ok {
		t.Fatalf("fresh receipt after three cycles: accepted=%v err=%v", ok, err)
	}
	// Clock rollback or a later durable gate-release bound must not admit a
	// receipt that is only newer than the epoch.
	if rollback {
		j.RollbackGateReleasedAt = time.Now().Add(time.Hour)
	} else {
		j.GateReleasedAt = time.Now().Add(time.Hour)
	}
	if ok, err := accept(context.Background(), j); err != nil || ok {
		t.Fatalf("receipt before gate release accepted: %v %v", ok, err)
	}
}
