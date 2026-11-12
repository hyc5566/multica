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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestRunLegacyJournalResumesAfterCoordinatorCleanup(t *testing.T) {
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
	rollback := filepath.Join(work, "rollback", "Multica.app")
	oldHash := fixtureBundle(t, rollback, "old CLI")
	_, oldTree, err := legacyTreeManifest(rollback)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	selfHash, err := hashFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bundleCLI(bundle)), 0700); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.OpenFile(bundleCLI(bundle), os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
	src.Close()
	evidence := filepath.Join(root, "evidence")
	if err := os.WriteFile(evidence, []byte("approved"), 0600); err != nil {
		t.Fatal(err)
	}
	evidenceHash, _ := hashFile(evidence)
	version, commit := "0.8.1-zh-tw.4", strings.Repeat("a", 40)
	manifest, err := json.Marshal(map[string]any{"source_id": "hyc5566/multica", "version": version, "commit": commit, "data_compatibility": "multica-profile-v1", "coordinated_update_protocol": 1, "coordinated_desktop": map[string]any{"darwin-arm64": map[string]any{"bundle_id": taiwanBundleID, "binary_sha256": selfHash, "binary_version": version, "binary_commit": commit, "data_compatibility": "multica-profile-v1"}}})
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, "manifest.json")
	if err := os.WriteFile(manifestPath, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	manifestHash := fmt.Sprintf("%x", sha256.Sum256(manifest))
	daemon := LegacyProcessIdentity{PID: 101, UID: os.Getuid(), StartedAt: "old-start", ExecutablePath: bundleCLI(bundle), Inode: 1, SHA256: oldHash}
	app := LegacyProcessIdentity{PID: 102, UID: os.Getuid(), StartedAt: "app-start", ExecutablePath: filepath.Join(bundle, "Contents", "MacOS", "Multica"), Inode: 2, SHA256: strings.Repeat("a", 64)}
	inventory := LegacyInventory{Version: 1, SourceID: "hyc5566/multica", Profile: "desktop-test", Bundle: bundle, CLIEntry: bundleCLI(bundle), WorkDir: work, OldVersion: "0.8.1-zh-tw.3", OldBinarySHA256: oldHash, OldDataCompatibility: "multica-profile-v1", CompatibilityEvidenceSHA256: evidenceHash, CandidateManifestSHA256: manifestHash, DaemonID: "daemon", ServerURLSHA256: fixtureServerHash(), ControllerMachineIDSHA256: strings.Repeat("b", 64), Workspaces: map[string][]string{"w": {"r"}}, Daemon: daemon, Apps: []LegacyProcessIdentity{app}}
	raw, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	inventoryPath := filepath.Join(root, "inventory.json")
	if err := os.WriteFile(inventoryPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	inventoryHash := fmt.Sprintf("%x", sha256.Sum256(raw))
	token := "11111111-1111-4111-8111-111111111111"
	gate := filepath.Join(profile, "daemon.maintenance")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		now := time.Now().UTC()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": 12345, "os": "darwin", "profile": "desktop-test", "daemon_id": "daemon", "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": version, "cli_commit": commit, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}, "maintenance_gate": map[string]any{"supported": true, "blocked": false, "path": gate}, "update_attestation": map[string]any{"binary_sha256": selfHash, "process_started_at": "new-start", "runtime_heartbeat_acks": map[string]int{"r": 4}, "task_receipts": []any{map[string]any{"task_id": "task", "runtime_id": "r", "completed_at": now, "ack_at": now.Add(time.Second)}}}})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, portText, _ := net.SplitHostPort(u.Host)
	port, _ := strconv.Atoi(portText)
	if err := os.WriteFile(filepath.Join(profile, "daemon.port"), []byte(fmt.Sprintf(`{"pid":12345,"port":%d}`, port)), 0600); err != nil {
		t.Fatal(err)
	}
	j := Journal{Version: 1, Token: token, Phase: "finalizing", Profile: inventory.Profile, CLIEntry: inventory.CLIEntry, DaemonID: inventory.DaemonID, ServerURLSHA256: inventory.ServerURLSHA256, DaemonPID: daemon.PID, DaemonStartedAt: daemon.StartedAt, NewDaemonPID: 12345, NewDaemonStartedAt: "new-start", HealthPort: port, AppPID: app.PID, GatePath: gate, OldBundle: bundle, StagedBundle: filepath.Join(work, "staging", "Next.app"), RollbackBundle: rollback, OlderBundle: filepath.Join(work, "older", "Multica.app"), OldBinarySHA256: oldHash, NewBinarySHA256: selfHash, OldVersion: inventory.OldVersion, NewVersion: version, NewCommit: commit, OldDataCompatibility: inventory.OldDataCompatibility, DataCompatibility: inventory.OldDataCompatibility, RuntimeIDs: []string{"r"}, Workspaces: inventory.Workspaces, ReadyFile: filepath.Join(work, "ready-"+token), HandoffFile: filepath.Join(work, "handoff-"+token), GateReleasedAt: time.Now().UTC().Add(-time.Minute), Legacy: &LegacyState{InventorySHA256: inventoryHash, InventoryPath: inventoryPath, Inventory: inventory, ManifestPath: manifestPath, CompatibilityEvidencePath: evidence, BackupManifestSHA256: oldTree, CoordinatorSHA256: selfHash, BaselineCleanupRequested: true, CoordinatorCleanupRequested: true}}
	// Explicitly model the same authenticated daemon and App on the macOS
	// boundary; the journal still carries the real durable gate/tree evidence.
	_, candidateTree, err := legacyTreeManifest(bundle)
	if err != nil {
		t.Fatal(err)
	}
	daemonNative := LegacyProcessIdentity{PID: 12345, UID: os.Getuid(), StartedAt: "native-daemon", ExecutablePath: bundleCLI(bundle), Device: 1, Inode: 3, SHA256: selfHash}
	appNative := LegacyProcessIdentity{PID: 12346, UID: os.Getuid(), StartedAt: "native-app", ExecutablePath: filepath.Join(bundle, "Contents", "MacOS", "Multica"), Device: 1, Inode: 4, SHA256: strings.Repeat("c", 64)}
	j.GateIdentity, j.GateReleaseRequested = "fixture-gate", true
	j.OldTreeSHA256, j.CandidateTreeSHA256 = oldTree, candidateTree
	j.NewAppPID, j.AcceptanceAppIdentity = appNative.PID, &appNative
	j.Legacy.CandidateProcesses = []LegacyProcessIdentity{daemonNative, appNative}
	path := filepath.Join(work, "journal.json")
	if err := SaveJournal(path, j); err != nil {
		t.Fatal(err)
	}
	savedMachine, savedSignature, savedNative, savedInspect, savedList, savedApp := legacyMachineIdentity, legacyVerifySignature, acceptanceNativeRequired, inspectLegacy, enumerateLegacyApps, abortAppExecutable
	defer func() {
		legacyMachineIdentity, legacyVerifySignature, acceptanceNativeRequired, inspectLegacy, enumerateLegacyApps, abortAppExecutable = savedMachine, savedSignature, savedNative, savedInspect, savedList, savedApp
	}()
	legacyMachineIdentity = func() (string, error) { return strings.Repeat("c", 64), nil }
	legacyVerifySignature = func(context.Context, string) error { return nil }
	acceptanceNativeRequired = func() bool { return true }
	inspectLegacy = func(pid int) (LegacyProcessIdentity, error) {
		switch pid {
		case daemonNative.PID:
			return daemonNative, nil
		case appNative.PID:
			return appNative, nil
		default:
			return LegacyProcessIdentity{}, ErrLegacyProcessExited
		}
	}
	enumerateLegacyApps = func(string) ([]int, error) { return []int{appNative.PID}, nil }
	abortAppExecutable = func(context.Context, string) (string, error) { return "Multica", nil }
	done, err := RunLegacyJournal(context.Background(), path, token)
	if err != nil || !done {
		t.Fatalf("fallback resume: %v %v", done, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var result Journal
	if json.Unmarshal(data, &result) != nil || result.Phase != "complete" {
		t.Fatal("fallback did not complete persisted cleanup")
	}
}
