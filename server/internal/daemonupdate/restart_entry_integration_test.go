package daemonupdate

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

// RunJournal and RunLegacyJournal use real files, locking and HTTP. Native
// macOS process inspection, App enumeration and codesign are mocked below.
func TestRestartEntryResumesDurableTransaction(t *testing.T) {
	for _, phase := range []string{"awaiting_acceptance", "finalizing", "rollback_awaiting_acceptance", "rollback_finalizing", "legacy"} {
		t.Run(phase, func(t *testing.T) { testRestartEntry(t, phase) })
	}
}

func testRestartEntry(t *testing.T, phase string) {
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
	bundle := filepath.Join(root, "Applications", "Multica.app")
	rollbackPath := filepath.Join(work, "rollback", "Multica.app")
	staged := filepath.Join(work, "staging", "Next.app")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selfHash, err := hashFile(self)
	if err != nil {
		t.Fatal(err)
	}
	copySelf := func(path string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(bundleCLI(path)), 0700); err != nil {
			t.Fatal(err)
		}
		src, err := os.Open(self)
		if err != nil {
			t.Fatal(err)
		}
		defer src.Close()
		dst, err := os.OpenFile(bundleCLI(path), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = io.Copy(dst, src); err != nil {
			t.Fatal(err)
		}
		if err = dst.Close(); err != nil {
			t.Fatal(err)
		}
	}
	isRollback := strings.HasPrefix(phase, "rollback_")
	var oldHash, newHash, oldTree, newTree string
	if isRollback {
		copySelf(bundle)
		oldHash = selfHash
		newHash = fixtureBundle(t, staged, "candidate")
		_, oldTree, err = legacyTreeManifest(bundle)
		if err != nil {
			t.Fatal(err)
		}
		_, newTree, err = legacyTreeManifest(staged)
	} else {
		copySelf(bundle)
		newHash = selfHash
		oldHash = fixtureBundle(t, rollbackPath, "old")
		_, oldTree, err = legacyTreeManifest(rollbackPath)
		if err != nil {
			t.Fatal(err)
		}
		_, newTree, err = legacyTreeManifest(bundle)
	}
	if err != nil {
		t.Fatal(err)
	}
	token := "11111111-1111-4111-8111-111111111111"
	oldVersion, newVersion := "0.8.1-zh-tw.3", "0.8.1-zh-tw.4"
	oldCommit, newCommit := strings.Repeat("b", 40), strings.Repeat("a", 40)
	j := Journal{Version: 1, Token: token, Phase: phase, Profile: "desktop-test", CLIEntry: bundleCLI(bundle), DaemonID: "daemon", ServerURLSHA256: fixtureServerHash(), DaemonPID: 101, DaemonStartedAt: "old-original", NewDaemonPID: 123, NewDaemonStartedAt: "previous-start", NewAppPID: 122, AppPID: 102, GatePath: filepath.Join(profile, "daemon.maintenance"), GateIdentity: "original-gate", GateReleaseRequested: true, GateReleasedAt: time.Now().Add(-time.Hour), OldBundle: bundle, StagedBundle: staged, RollbackBundle: rollbackPath, OlderBundle: filepath.Join(work, "older", "Multica.app"), OldBinarySHA256: oldHash, NewBinarySHA256: newHash, OldTreeSHA256: oldTree, CandidateTreeSHA256: newTree, OldVersion: oldVersion, NewVersion: newVersion, OldCommit: oldCommit, NewCommit: newCommit, OldDataCompatibility: "multica-profile-v1", DataCompatibility: "multica-profile-v1", RuntimeIDs: []string{"r"}, Workspaces: map[string][]string{"w": {"r"}}, ReadyFile: filepath.Join(work, "ready-"+token), HandoffFile: filepath.Join(work, "handoff-"+token)}
	if isRollback {
		j.RollbackDaemonPID, j.RollbackDaemonStartedAt = 123, "previous-start"
		j.RollbackGateReleaseRequested, j.RollbackGateReleasedAt = true, j.GateReleasedAt
	}
	if phase == "finalizing" {
		j.CleanupRequested = true
		j.PreviousRollbackSHA256 = strings.Repeat("d", 64)
	}
	version, commit, activeHash := newVersion, newCommit, newHash
	if isRollback {
		version, commit, activeHash = oldVersion, oldCommit, oldHash
	}
	beat := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := time.Now().UTC()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": 124, "os": "darwin", "profile": j.Profile, "daemon_id": j.DaemonID, "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": version, "cli_commit": commit, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}, "maintenance_gate": map[string]any{"supported": true, "blocked": false, "path": j.GatePath}, "update_attestation": map[string]any{"binary_sha256": activeHash, "process_started_at": "new-start", "runtime_heartbeat_acks": map[string]int{"r": beat}, "task_receipts": []any{map[string]any{"task_id": "task", "runtime_id": "r", "completed_at": now, "ack_at": now.Add(time.Second)}}}})
	}))
	defer srv.Close()
	_, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	j.HealthPort, _ = strconv.Atoi(portText)
	if err := os.WriteFile(filepath.Join(profile, "daemon.port"), []byte(fmt.Sprintf(`{"pid":124,"port":%d}`, j.HealthPort)), 0600); err != nil {
		t.Fatal(err)
	}
	inspect, list, signature, executable, machine := inspectLegacy, enumerateLegacyApps, legacyVerifySignature, abortAppExecutable, legacyMachineIdentity
	defer func() {
		inspectLegacy, enumerateLegacyApps, legacyVerifySignature, abortAppExecutable, legacyMachineIdentity = inspect, list, signature, executable, machine
	}()
	inspectLegacy = func(pid int) (LegacyProcessIdentity, error) {
		switch pid {
		case 123:
			return LegacyProcessIdentity{}, ErrLegacyProcessExited
		case 124:
			return LegacyProcessIdentity{PID: 124, UID: os.Getuid(), StartedAt: "native-new", ExecutablePath: bundleCLI(bundle), Device: 1, Inode: 4, SHA256: activeHash}, nil
		case 125:
			return LegacyProcessIdentity{PID: 125, UID: os.Getuid(), StartedAt: "native-app", ExecutablePath: filepath.Join(bundle, "Contents", "MacOS", "Multica"), Device: 1, Inode: 5, SHA256: "app"}, nil
		default:
			return LegacyProcessIdentity{}, ErrLegacyProcessExited
		}
	}
	enumerateLegacyApps = func(string) ([]int, error) { return []int{125}, nil }
	legacyVerifySignature = func(context.Context, string) error { return nil }
	abortAppExecutable = func(context.Context, string) (string, error) { return "Multica", nil }
	legacyMachineIdentity = func() (string, error) { return strings.Repeat("c", 64), nil }
	path := filepath.Join(work, "journal.json")
	if phase == "legacy" {
		j.Phase = "awaiting_acceptance"
		j.OldCommit = ""
		oldCommit = ""
		evidence := filepath.Join(root, "evidence")
		if err := os.WriteFile(evidence, []byte("approved"), 0600); err != nil {
			t.Fatal(err)
		}
		evidenceHash, _ := hashFile(evidence)
		manifest, _ := json.Marshal(map[string]any{"source_id": "hyc5566/multica", "version": newVersion, "commit": newCommit, "data_compatibility": "multica-profile-v1", "coordinated_update_protocol": 1, "coordinated_desktop": map[string]any{"darwin-arm64": map[string]any{"bundle_id": taiwanBundleID, "binary_sha256": newHash, "binary_version": newVersion, "binary_commit": newCommit, "data_compatibility": "multica-profile-v1"}}})
		manifestPath := filepath.Join(root, "manifest.json")
		if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
			t.Fatal(err)
		}
		manifestHash := fmt.Sprintf("%x", sha256.Sum256(manifest))
		daemon := LegacyProcessIdentity{PID: 101, UID: os.Getuid(), StartedAt: j.DaemonStartedAt, ExecutablePath: bundleCLI(bundle), Inode: 1, SHA256: oldHash}
		app := LegacyProcessIdentity{PID: 102, UID: os.Getuid(), StartedAt: "original-app", ExecutablePath: filepath.Join(bundle, "Contents", "MacOS", "Multica"), Inode: 2, SHA256: strings.Repeat("a", 64)}
		inventory := LegacyInventory{Version: 1, SourceID: "hyc5566/multica", Profile: j.Profile, Bundle: bundle, CLIEntry: j.CLIEntry, WorkDir: work, OldVersion: oldVersion, OldBinarySHA256: oldHash, OldDataCompatibility: j.OldDataCompatibility, CompatibilityEvidenceSHA256: evidenceHash, CandidateManifestSHA256: manifestHash, DaemonID: j.DaemonID, ServerURLSHA256: j.ServerURLSHA256, ControllerMachineIDSHA256: strings.Repeat("b", 64), Workspaces: j.Workspaces, Daemon: daemon, Apps: []LegacyProcessIdentity{app}}
		raw, _ := json.Marshal(inventory)
		inventoryPath := filepath.Join(root, "inventory.json")
		if err := os.WriteFile(inventoryPath, raw, 0600); err != nil {
			t.Fatal(err)
		}
		backup := filepath.Join(work, "baseline", "Multica.app")
		if got := fixtureBundle(t, backup, "old"); got != oldHash {
			t.Fatal("backup hash differs")
		}
		records, digest, err := legacyTreeManifest(backup)
		if err != nil || digest != oldTree {
			t.Fatalf("backup tree mismatch: %v", err)
		}
		if err := saveJSON(filepath.Join(work, "baseline", "manifest.json"), records); err != nil {
			t.Fatal(err)
		}
		j.Legacy = &LegacyState{InventorySHA256: fmt.Sprintf("%x", sha256.Sum256(raw)), InventoryPath: inventoryPath, Inventory: inventory, ManifestPath: manifestPath, CompatibilityEvidencePath: evidence, BackupManifestSHA256: oldTree, CoordinatorSHA256: newHash, BaselineCleanupRequested: false, CoordinatorCleanupRequested: true, CandidateProcesses: []LegacyProcessIdentity{{PID: 123, UID: os.Getuid(), StartedAt: "previous-native", ExecutablePath: bundleCLI(bundle), Inode: 3, SHA256: newHash}}}
	} else {
		current := map[string]string{"version": oldVersion, "commit": oldCommit, "binary_sha256": oldHash}
		previous := map[string]string{"binary_sha256": j.PreviousRollbackSHA256}
		if phase == "finalizing" {
			current = map[string]string{"version": newVersion, "commit": newCommit, "binary_sha256": newHash}
			previous = map[string]string{"version": oldVersion, "commit": oldCommit, "binary_sha256": oldHash}
		}
		if err := saveJSON(filepath.Join(work, "ledger.json"), map[string]any{"data_compatibility": j.DataCompatibility, "cli_entry": j.CLIEntry, "current": current, "previous": previous}); err != nil {
			t.Fatal(err)
		}
	}
	if err := SaveJournal(path, j); err != nil {
		t.Fatal(err)
	}
	run := func() (bool, error) {
		if phase == "legacy" {
			return RunLegacyJournal(context.Background(), path, token)
		}
		return RunJournal(context.Background(), path, token)
	}
	for beat = 0; beat < 3; beat++ {
		done, err := run()
		if done {
			t.Fatalf("accepted before heartbeat %d: %v", beat, err)
		}
		if err != nil && !strings.Contains(err.Error(), "task receipt no longer verifiable") {
			t.Fatalf("heartbeat %d: %v", beat, err)
		}
	}
	beat = 3
	done, err := run()
	if err != nil || (done == isRollback) {
		t.Fatalf("completion: done=%v err=%v", done, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result Journal
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	expected := "complete"
	if isRollback {
		expected = "restored"
	}
	if result.Phase != expected {
		t.Fatalf("phase=%s want=%s", result.Phase, expected)
	}
	if phase == "legacy" {
		if _, err := os.Stat(filepath.Join(work, "baseline")); !os.IsNotExist(err) {
			t.Fatalf("baseline was not retired: %v", err)
		}
		if err := verifyBundleHash(rollbackPath, oldHash); err != nil {
			t.Fatalf("rollback lost: %v", err)
		}
	}
}
