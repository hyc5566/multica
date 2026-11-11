package daemonupdate

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestAbortBeforeHandoffOnlyReleasesOwnedIdleOriginal(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "desktop-test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	bundle := filepath.Join(root, "Multica.app")
	hash := fixtureBundle(t, bundle, "old")
	j := &Journal{Profile: "desktop-test", Token: "token", Phase: "prepared", AppPID: 123, DaemonPID: 456, DaemonStartedAt: "start", DaemonID: "daemon", ServerURLSHA256: fixtureServerHash(), OldBundle: bundle, OldBinarySHA256: hash, OldVersion: "0.8.1-zh-tw.4", OldCommit: "commit", GatePath: filepath.Join(profile, "daemon.maintenance"), RuntimeIDs: []string{"r"}, Workspaces: map[string][]string{"w": {"r"}}}
	path := filepath.Join(root, "journal.json")
	savedInspect, savedExecutable := inspectLegacy, abortAppExecutable
	defer func() { inspectLegacy, abortAppExecutable = savedInspect, savedExecutable }()
	inspectLegacy = func(pid int) (LegacyProcessIdentity, error) {
		return LegacyProcessIdentity{PID: pid, ExecutablePath: filepath.Join(bundle, "Contents", "MacOS", "Multica")}, nil
	}
	abortAppExecutable = func(context.Context, string) (string, error) { return "Multica", nil }
	active := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, e := os.Stat(j.GatePath)
		blocked := e == nil
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": 456, "os": "darwin", "profile": j.Profile, "daemon_id": j.DaemonID, "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": j.OldVersion, "cli_commit": j.OldCommit, "active_task_count": active, "maintenance_shutdown": true, "maintenance_gate": map[string]any{"supported": true, "blocked": blocked, "ready": blocked, "path": j.GatePath}, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}, "update_attestation": map[string]any{"binary_sha256": hash, "process_started_at": "start", "runtime_heartbeat_acks": map[string]int{"r": 1}}})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	j.HealthPort, _ = strconv.Atoi(port)
	a := newLocalAdapter(path)
	if err := prepareAbortBeforeHandoff(context.Background(), path, j, 123, a); err != nil || j.Phase != "no_change" {
		t.Fatalf("no gate: %s %v", j.Phase, err)
	}
	j.Phase = "acquiring_gate"
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	active = 1
	if err := prepareAbortBeforeHandoff(context.Background(), path, j, 123, a); err == nil {
		t.Fatal("released gate while work was active")
	}
	if err := verifyGate(j); err != nil {
		t.Fatal(err)
	}
	active = 0
	j.StopRequested = true
	if err := prepareAbortBeforeHandoff(context.Background(), path, j, 123, a); err == nil {
		t.Fatal("allowed abort after stop intent")
	}
	j.StopRequested = false
	if err := prepareAbortBeforeHandoff(context.Background(), path, j, 123, a); err != nil || j.Phase != "restored" {
		t.Fatalf("idle owned gate: %s %v", j.Phase, err)
	}
	if _, err := os.Stat(j.GatePath); !os.IsNotExist(err) {
		t.Fatal("owned gate remains")
	}
	j.Phase = "releasing_gate"
	if err := prepareAbortBeforeHandoff(context.Background(), path, j, 123, a); err == nil {
		t.Fatal("allowed abort after handoff")
	}
	j.Phase = "stopping_old"
	if err := prepareAbortBeforeHandoff(context.Background(), path, j, 999, a); err == nil {
		t.Fatal("accepted different App")
	}
}
