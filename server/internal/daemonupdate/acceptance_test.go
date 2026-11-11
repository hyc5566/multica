package daemonupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestAcceptanceRequiresLiveMatchingRuntimeReceiptBeforeCleanup(t *testing.T) {
	// This test exercises receipt filtering and retention, not OS process inspection.
	savedNative := acceptanceNativeRequired
	defer func() { acceptanceNativeRequired = savedNative }()
	acceptanceNativeRequired = func() bool { return false }
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
	j := &Journal{Profile: "test", DaemonID: "daemon", DaemonPID: 100, NewDaemonPID: 123, NewDaemonStartedAt: "start", GatePath: filepath.Join(profile, "daemon.maintenance"),
		OldBundle: filepath.Join(root, "Applications", "Multica.app"), StagedBundle: filepath.Join(work, "staging", "Multica.app"), RollbackBundle: filepath.Join(work, "rollback", "Multica.app"), OlderBundle: filepath.Join(work, "older", "Multica.app"),
		OldVersion: "0.8.1-zh-tw.4", NewVersion: "0.8.1-zh-tw.5", OldCommit: "old", NewCommit: "new", DataCompatibility: "multica-profile-v1", OldDataCompatibility: "multica-profile-v1", RuntimeIDs: []string{"r"}, Workspaces: map[string][]string{"w": {"r"}}, GateReleasedAt: time.Now().UTC().Add(-time.Minute)}
	j.ServerURLSHA256 = fixtureServerHash()
	j.NewBinarySHA256 = fixtureBundle(t, j.OldBundle, "new")
	j.OldBinarySHA256 = fixtureBundle(t, j.RollbackBundle, "old")
	j.PreviousRollbackSHA256 = fixtureBundle(t, j.OlderBundle, "older")
	_, j.PreviousRollbackTreeSHA256, _ = legacyTreeManifest(j.OlderBundle)
	receiptRuntime := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		receipts := []any{}
		if receiptRuntime != "" {
			receipts = append(receipts, map[string]any{"task_id": "task", "runtime_id": receiptRuntime, "completed_at": time.Now().UTC(), "ack_at": time.Now().UTC().Add(time.Second)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": 123, "os": "darwin", "profile": "test", "daemon_id": "daemon", "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": j.NewVersion, "cli_commit": j.NewCommit,
			"workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}, "maintenance_gate": map[string]any{"supported": true, "blocked": false, "path": j.GatePath},
			"update_attestation": map[string]any{"binary_sha256": j.NewBinarySHA256, "process_started_at": "start", "runtime_heartbeat_acks": map[string]int{"r": 4}, "task_receipts": receipts}})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	j.HealthPort, _ = strconv.Atoi(port)
	if err := os.WriteFile(filepath.Join(profile, "daemon.port"), []byte(fmt.Sprintf(`{"port":%d,"pid":123}`, j.HealthPort)), 0600); err != nil {
		t.Fatal(err)
	}
	a := newLocalAdapter(filepath.Join(work, "journal.json"))
	if ok, err := a.ReceiptAccepted(context.Background(), j); err != nil || ok {
		t.Fatalf("missing receipt: %v %v", ok, err)
	}
	receiptRuntime = "other"
	if ok, err := a.ReceiptAccepted(context.Background(), j); err != nil || ok {
		t.Fatalf("foreign receipt: %v %v", ok, err)
	}
	if err := a.FinalizeRetention(context.Background(), j); err == nil {
		t.Fatal("cleanup accepted foreign receipt")
	}
	if err := verifyBundleHash(j.OlderBundle, j.PreviousRollbackSHA256); err != nil {
		t.Fatal(err)
	}
	receiptRuntime = "r"
	rolledBack := *j
	rolledBack.OldVersion, rolledBack.OldCommit, rolledBack.OldBinarySHA256 = j.NewVersion, j.NewCommit, j.NewBinarySHA256
	rolledBack.RollbackDaemonPID, rolledBack.RollbackDaemonStartedAt, rolledBack.RollbackGateReleasedAt = 123, "start", j.GateReleasedAt
	if ok, err := a.RollbackReceiptAccepted(context.Background(), &rolledBack); err != nil || !ok {
		t.Fatalf("rollback receipt: %v %v", ok, err)
	}
	receiptRuntime = "other"
	if ok, err := a.RollbackReceiptAccepted(context.Background(), &rolledBack); err != nil || ok {
		t.Fatalf("foreign rollback receipt: %v %v", ok, err)
	}
	receiptRuntime = "r"
	actualPrevious := j.PreviousRollbackSHA256
	j.PreviousRollbackSHA256 = fmt.Sprintf("%064d", 0)
	if err := a.FinalizeRetention(context.Background(), j); err == nil {
		t.Fatal("cleanup accepted a mismatched older bundle")
	}
	if err := verifyBundleHash(j.OlderBundle, actualPrevious); err != nil {
		t.Fatal(err)
	}
	j.PreviousRollbackSHA256 = actualPrevious
	if err := a.FinalizeRetention(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(j.OlderBundle); !os.IsNotExist(err) {
		t.Fatalf("older bundle still exists: %v", err)
	}
	if err := verifyBundleHash(j.RollbackBundle, j.OldBinarySHA256); err != nil {
		t.Fatal(err)
	}
}
