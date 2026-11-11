//go:build linux

package daemonupdate

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// This helper runs in a separate test subprocess, so the transaction fixture
// can assert that its Server remains alive across update and recovery.
func TestLegacyFixtureServerProcess(t *testing.T) {
	root := os.Getenv("MULTICA_LEGACY_FIXTURE_ROOT")
	if root == "" {
		t.Skip("fixture subprocess only")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(root, "fixture-server-port"), []byte(strconv.Itoa(port)), 0600); err != nil {
		t.Fatal(err)
	}
	var beats atomic.Uint64
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var j Journal
		data, err := os.ReadFile(filepath.Join(root, "zh-tw-update", "journal.json"))
		if err != nil || json.Unmarshal(data, &j) != nil {
			http.Error(w, "no journal", http.StatusServiceUnavailable)
			return
		}
		raw, _ := os.ReadFile(filepath.Join(root, "profiles", "desktop-test", "daemon.pid"))
		pid, _ := strconv.Atoi(strings.TrimSpace(string(raw)))
		if pid == 0 {
			pid = j.DaemonPID
		}
		if r.URL.Path == "/shutdown/maintenance" {
			if _, e := os.Stat(filepath.Join(root, "normal")); e == nil {
				start, e := fixtureProcessStart(pid)
				if e != nil || j.CandidateDaemonIdentity == nil || j.CandidateDaemonIdentity.PID != pid || j.CandidateDaemonIdentity.StartedAt != start {
					http.Error(w, "unknown target", http.StatusConflict)
					return
				}
				if e := syscall.Kill(pid, syscall.SIGTERM); e != nil {
					http.Error(w, "stop failed", http.StatusConflict)
					return
				}
			}
			w.WriteHeader(http.StatusAccepted)
			return
		}
		_, gateErr := os.Stat(j.GatePath)
		blocked := gateErr == nil
		candidate := pid == j.NewDaemonPID && pid > 0
		version, commit, hash := j.OldVersion, j.OldCommit, j.OldBinarySHA256
		_, normalErr := os.Stat(filepath.Join(root, "normal"))
		if candidate {
			version, commit, hash = j.NewVersion, j.NewCommit, j.NewBinarySHA256
			if _, e := os.Stat(filepath.Join(root, "fault-hash")); e == nil {
				hash = strings.Repeat("f", 64)
			}
		}
		payload := map[string]any{"status": "running", "pid": pid, "os": "darwin", "profile": j.Profile, "daemon_id": j.DaemonID, "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": version, "active_task_count": 0, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}}
		if candidate || normalErr == nil {
			if _, e := os.Stat(filepath.Join(root, "fault-identity")); candidate && e == nil {
				payload["daemon_id"] = "other"
			}
			receipts := []any{}
			if _, e := os.Stat(filepath.Join(root, "receipt")); e == nil {
				receipts = append(receipts, map[string]any{"task_id": "task", "runtime_id": "r", "completed_at": time.Now().UTC(), "ack_at": time.Now().UTC().Add(time.Second)})
			}
			payload["cli_commit"] = commit
			payload["maintenance_shutdown"] = true
			payload["maintenance_gate"] = map[string]any{"supported": true, "blocked": blocked, "ready": blocked, "path": j.GatePath}
			beat := beats.Add(1)
			if _, e := os.Stat(filepath.Join(root, "fault-heartbeat")); candidate && e == nil {
				beat = 1
			}
			payload["update_attestation"] = map[string]any{"binary_sha256": hash, "process_started_at": fmt.Sprintf("start-%d", pid), "runtime_heartbeat_acks": map[string]uint64{"r": beat}, "task_receipts": receipts}
		}
		_ = json.NewEncoder(w).Encode(payload)
	})
	_ = http.Serve(listener, handler)
}
