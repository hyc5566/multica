package daemonupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestCandidateHealthWaitsForVerifiedDaemonStarting(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{
		Profile: "test", DaemonID: "daemon", DaemonPID: 100, NewDaemonPID: 123,
		NewVersion: "0.11.1-zh-tw.2+up.5fbd8da", NewCommit: "new",
		NewBinarySHA256: "fixture-hash", ServerURLSHA256: fixtureServerHash(),
		Workspaces: map[string][]string{"w": {"r"}}, RuntimeIDs: []string{"r"},
	}
	var ready atomic.Bool
	var wrongImage atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		status := "starting"
		if ready.Load() {
			status = "running"
		}
		image := j.NewBinarySHA256
		if wrongImage.Load() {
			image = "unexpected-image"
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": status, "pid": 123, "os": "darwin", "profile": j.Profile,
			"daemon_id": j.DaemonID, "server_url": fixtureServerURL, "launched_by": "desktop",
			"cli_version": j.NewVersion, "cli_commit": j.NewCommit,
			"update_attestation": map[string]any{"binary_sha256": image,
				"process_started_at": "start", "runtime_heartbeat_acks": map[string]int{"r": 4}},
			"workspaces": []map[string]any{{"id": "w", "runtimes": []string{"r"}}},
		})
	}))
	defer srv.Close()
	if err := os.WriteFile(filepath.Join(profile, "daemon.port"),
		[]byte(fmt.Sprintf(`{"port":%d,"pid":123}`, srv.Listener.Addr().(*net.TCPAddr).Port)), 0600); err != nil {
		t.Fatal(err)
	}
	a := newLocalAdapter(filepath.Join(root, "journal.json"))
	if _, err := a.candidateHealth(context.Background(), j); !errors.Is(err, errCandidateStarting) {
		t.Fatalf("verified starting daemon should be retried: %v", err)
	}
	wrongImage.Store(true)
	if _, err := a.candidateHealth(context.Background(), j); !errors.Is(err, errCandidateAttestation) {
		t.Fatalf("wrong binary still starting should be rejected: %v", err)
	}
	wrongImage.Store(false)
	started := *j
	started.NewDaemonStartedAt = "start"
	if _, err := a.candidateHealth(context.Background(), &started); !errors.Is(err, errCandidateAttestation) {
		t.Fatalf("daemon returning to starting after readiness should be rejected: %v", err)
	}
	ready.Store(true)
	if _, err := a.candidateHealth(context.Background(), j); err != nil {
		t.Fatalf("same candidate in running: %v", err)
	}
}
