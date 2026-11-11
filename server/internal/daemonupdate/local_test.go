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

func TestRequestStopRequiresDrainedMatchingDaemon(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{Profile: "test", Token: "owner", DaemonID: "daemon", DaemonPID: 123, DaemonStartedAt: "start", GatePath: filepath.Join(profile, "daemon.maintenance"), OldVersion: "v", OldCommit: "commit", OldBinarySHA256: "hash", Workspaces: map[string][]string{"w": {"r"}}, RuntimeIDs: []string{"r"}}
	j.ServerURLSHA256 = fixtureServerHash()
	if err := acquireGate(filepath.Join(root, "journal"), j); err != nil {
		t.Fatal(err)
	}
	h := health{Status: "running", PID: 123, OS: "darwin", LaunchedBy: "desktop", Profile: "test", DaemonID: "daemon", Version: "v", Commit: "commit", Workspaces: []healthWorkspace{{ID: "w", Runtimes: []string{"r"}}}, Attestation: healthAttestation{BinarySHA256: "hash", ProcessStartedAt: "start", RuntimeHeartbeatAcks: map[string]uint64{"r": 2}}, MaintenanceShutdown: true}
	h.ServerURL = fixtureServerURL
	h.MaintenanceGate.Supported = true
	h.MaintenanceGate.Blocked = true
	h.MaintenanceGate.Path = j.GatePath
	stops := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			_ = json.NewEncoder(w).Encode(h)
			return
		}
		stops++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	_, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	j.HealthPort, err = strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	a := newLocalAdapter(filepath.Join(root, "journal"))
	if err := a.requestStop(context.Background(), j); err == nil || stops != 0 {
		t.Fatal("busy daemon was stopped")
	}
	h.MaintenanceGate.Ready = true
	h.PID = 124
	if err := a.requestStop(context.Background(), j); err == nil || stops != 0 {
		t.Fatal("changed PID was stopped")
	}
	h.PID = 123
	if err := a.requestStop(context.Background(), j); err != nil || stops != 1 {
		t.Fatalf("valid stop: %v (%d requests)", err, stops)
	}
}
