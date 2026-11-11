package daemonupdate

import (
	"context"
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

// Real journal entry, gate, ledger and HTTP, with explicit Darwin native,
// signature, and process-liveness boundaries replaced by isolated fixtures.
func TestRunJournalPreSwapFailureCanAcceptRollback(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(fmt.Sprintf("restart_%v", restart), func(t *testing.T) { testPreSwapRollback(t, restart) })
	}
}
func testPreSwapRollback(t *testing.T, restart bool) {
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
	staged := filepath.Join(work, "staging", "Next.app")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	oldHash, err := hashFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(bundleCLI(bundle)), 0700); err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	dst, err := os.OpenFile(bundleCLI(bundle), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err = dst.Close(); err != nil {
		t.Fatal(err)
	}
	src.Close()
	newHash := fixtureBundle(t, staged, "candidate")
	token := "11111111-1111-4111-8111-111111111111"
	oldVersion, newVersion := "0.8.1-zh-tw.3", "0.8.1-zh-tw.4"
	oldCommit, newCommit := strings.Repeat("b", 40), strings.Repeat("a", 40)
	j := Journal{Version: 1, Token: token, Phase: "acquiring_gate", Profile: "desktop-test", CLIEntry: bundleCLI(bundle), DaemonID: "daemon", ServerURLSHA256: fixtureServerHash(), DaemonPID: 123, DaemonStartedAt: "old-start", AppPID: 125, GatePath: filepath.Join(profile, "daemon.maintenance"), OldBundle: bundle, StagedBundle: staged, RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app"), OldBinarySHA256: oldHash, NewBinarySHA256: newHash, OldVersion: oldVersion, NewVersion: newVersion, OldCommit: oldCommit, NewCommit: newCommit, OldDataCompatibility: "multica-profile-v1", DataCompatibility: "multica-profile-v1", RuntimeIDs: []string{"r"}, Workspaces: map[string][]string{"w": {"r"}}, ReadyFile: filepath.Join(work, "ready-"+token), HandoffFile: filepath.Join(work, "handoff-"+token)}
	if err := os.Mkdir(j.HandoffFile, 0700); err != nil {
		t.Fatal(err)
	} // fail after gate acquisition
	beat := 0
	pid, started := 123, "old-start"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		beat++
		_, e := os.Stat(j.GatePath)
		blocked := e == nil
		now := time.Now().UTC()
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": pid, "os": "darwin", "profile": j.Profile, "daemon_id": j.DaemonID, "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": oldVersion, "cli_commit": oldCommit, "active_task_count": 0, "maintenance_shutdown": true, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}, "maintenance_gate": map[string]any{"supported": true, "blocked": blocked, "ready": blocked, "path": j.GatePath}, "update_attestation": map[string]any{"binary_sha256": oldHash, "process_started_at": started, "runtime_heartbeat_acks": map[string]int{"r": beat}, "task_receipts": []any{map[string]any{"task_id": "task", "runtime_id": "r", "completed_at": now, "ack_at": now.Add(time.Second)}}}})
	}))
	defer srv.Close()
	_, portText, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	j.HealthPort, _ = strconv.Atoi(portText)
	writePort := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(profile, "daemon.port"), []byte(fmt.Sprintf(`{"pid":%d,"port":%d}`, pid, j.HealthPort)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	writePort()
	native, inspect, list, signature, executable, exists, shared := acceptanceNativeRequired, inspectLegacy, enumerateLegacyApps, legacyVerifySignature, abortAppExecutable, rollbackProcessExists, checkSharedBundle
	defer func() {
		acceptanceNativeRequired, inspectLegacy, enumerateLegacyApps, legacyVerifySignature, abortAppExecutable, rollbackProcessExists, checkSharedBundle = native, inspect, list, signature, executable, exists, shared
	}()
	acceptanceNativeRequired = func() bool { return true }
	legacyVerifySignature = func(context.Context, string) error { return nil }
	checkSharedBundle = func(string) error { return nil }
	rollbackProcessExists = func(pid int) (bool, error) { return pid == 123 || pid == 125, nil }
	enumerateLegacyApps = func(string) ([]int, error) { return []int{125}, nil }
	abortAppExecutable = func(context.Context, string) (string, error) { return "Multica", nil }
	inspectLegacy = func(targetPID int) (LegacyProcessIdentity, error) {
		switch targetPID {
		case 123, 124:
			if targetPID != pid {
				return LegacyProcessIdentity{}, ErrLegacyProcessExited
			}
			return LegacyProcessIdentity{PID: targetPID, UID: os.Getuid(), StartedAt: "native-" + started, ExecutablePath: bundleCLI(bundle), Device: 1, Inode: 2, SHA256: oldHash}, nil
		case 125:
			return LegacyProcessIdentity{PID: 125, UID: os.Getuid(), StartedAt: "native-app", ExecutablePath: filepath.Join(bundle, "Contents", "MacOS", "Multica"), Device: 1, Inode: 3, SHA256: "app"}, nil
		default:
			return LegacyProcessIdentity{}, ErrLegacyProcessExited
		}
	}
	path := filepath.Join(work, "journal.json")
	if err := saveJSON(filepath.Join(work, "ledger.json"), map[string]any{"data_compatibility": j.DataCompatibility, "cli_entry": j.CLIEntry, "current": map[string]string{"version": oldVersion, "commit": oldCommit, "binary_sha256": oldHash}, "previous": map[string]string{"binary_sha256": ""}}); err != nil {
		t.Fatal(err)
	}
	if err := SaveJournal(path, j); err != nil {
		t.Fatal(err)
	}
	done, err := RunJournal(context.Background(), path, token)
	if err == nil || done {
		t.Fatalf("expected handoff failure: %v %v", done, err)
	}
	read := func() Journal {
		t.Helper()
		data, e := os.ReadFile(path)
		if e != nil {
			t.Fatal(e)
		}
		var out Journal
		if e = json.Unmarshal(data, &out); e != nil {
			t.Fatal(e)
		}
		return out
	}
	after := read()
	if after.Phase != "rollback_awaiting_acceptance" || after.OldTreeSHA256 == "" {
		t.Fatalf("rollback lacks trusted pre-gate tree: phase=%s tree=%s; initial error=%v", after.Phase, after.OldTreeSHA256, err)
	}
	if restart {
		pid, started = 124, "restarted"
		writePort()
	}
	for i := 0; i < 4; i++ {
		done, err = RunJournal(context.Background(), path, token)
		if err != nil {
			t.Fatalf("rollback acceptance %d: %v", i, err)
		}
		if done {
			t.Fatal("rollback incorrectly reported new candidate complete")
		}
	}
	after = read()
	if after.Phase != "restored" {
		t.Fatalf("rollback did not finalize: %s", after.Phase)
	}
}

func TestTrustedOriginalTreeCannotChangeBeforeSwapOrRetry(t *testing.T) {
	for _, step := range []int{0, 1} {
		t.Run(fmt.Sprintf("swap_step_%d", step), func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(cli.TaskConfigRootEnv, root)
			profile := filepath.Join(root, "profiles", "desktop-test")
			work := filepath.Join(root, "update")
			for _, p := range []string{profile, work} {
				if err := os.MkdirAll(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			j := &Journal{Profile: "desktop-test", SwapStep: step, OldBundle: filepath.Join(root, "Multica.app"), StagedBundle: filepath.Join(work, "staging", "Next.app"), RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app")}
			j.OldBinarySHA256 = fixtureBundle(t, j.OldBundle, "old")
			j.NewBinarySHA256 = fixtureBundle(t, j.StagedBundle, "new")
			_, j.OldTreeSHA256, _ = legacyTreeManifest(j.OldBundle)
			_, j.CandidateTreeSHA256, _ = legacyTreeManifest(j.StagedBundle)
			if err := os.WriteFile(filepath.Join(j.OldBundle, "changed"), []byte("tamper"), 0600); err != nil {
				t.Fatal(err)
			}
			a := newLocalAdapter(filepath.Join(work, "journal.json"))
			if err := a.SwapBundles(context.Background(), j); err == nil || !strings.Contains(err.Error(), "tree changed") {
				t.Fatalf("changed tree accepted: %v", err)
			}
			if err := verifyBundleHash(j.OldBundle, j.OldBinarySHA256); err != nil {
				t.Fatalf("original bundle moved: %v", err)
			}
			saved := checkSharedBundle
			defer func() { checkSharedBundle = saved }()
			checkSharedBundle = func(string) error { return nil }
			if err := a.AcquireGate(context.Background(), j); err == nil || !strings.Contains(err.Error(), "tree changed") {
				t.Fatalf("retry accepted changed tree: %v", err)
			}
		})
	}
}
