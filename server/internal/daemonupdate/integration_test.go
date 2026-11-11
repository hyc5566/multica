//go:build linux

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
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestLocalTransactionRunsGateSwapHeartbeatAndReceipt(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "zh-tw-update")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	oldProcess := exec.Command("/bin/sleep", "60")
	if err := oldProcess.Start(); err != nil {
		t.Fatal(err)
	}
	oldDone := make(chan struct{})
	go func() { _ = oldProcess.Wait(); close(oldDone) }()
	t.Cleanup(func() {
		if oldProcess.ProcessState == nil {
			_ = oldProcess.Process.Kill()
			<-oldDone
		}
	})
	j := fixtureJournal("prepared")
	j.ServerURLSHA256 = fixtureServerHash()
	j.Token, j.Profile, j.DaemonID, j.DaemonPID, j.DaemonStartedAt, j.AppPID = "fixture", "test", "daemon", oldProcess.Process.Pid, fmt.Sprintf("start-%d", oldProcess.Process.Pid), 99999999
	j.GatePath = filepath.Join(profile, "daemon.maintenance")
	j.OldBundle = filepath.Join(root, "Applications", "Multica.app")
	j.StagedBundle = filepath.Join(work, "staging", "Multica.app")
	j.RollbackBundle = filepath.Join(work, "rollback", "Multica.app")
	j.OlderBundle = filepath.Join(work, "older", "Multica.app")
	j.OldCommit, j.NewCommit, j.CLIEntry = "oldcommit", "newcommit", bundleCLI(j.OldBundle)
	j.Workspaces, j.RuntimeIDs = map[string][]string{"w": {"r"}}, []string{"r"}
	j.NewAppExecutable, j.ReadyFile, j.HandoffFile = "Multica", filepath.Join(work, "ready-fixture"), filepath.Join(work, "handoff-fixture")
	oldCLI := "#!/bin/sh\nexit 1\n"
	j.OldBinarySHA256 = fixtureBundle(t, j.OldBundle, oldCLI)
	var beats atomic.Uint64
	var receipt atomic.Bool
	var port int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pid := oldProcess.Process.Pid
		if data, err := os.ReadFile(filepath.Join(profile, "daemon.pid")); err == nil {
			if parsed, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
				pid = parsed
			}
		}
		if r.URL.Path == "/shutdown/maintenance" {
			var request struct {
				ExpectedPID int    `json:"expected_pid"`
				Token       string `json:"gate_token"`
			}
			if json.NewDecoder(r.Body).Decode(&request) != nil || request.ExpectedPID != pid || request.Token != j.Token {
				w.WriteHeader(http.StatusConflict)
				return
			}
			_ = syscall.Kill(pid, syscall.SIGTERM)
			w.WriteHeader(http.StatusAccepted)
			return
		}
		version, commit, hash := j.OldVersion, j.OldCommit, j.OldBinarySHA256
		if pid != oldProcess.Process.Pid {
			version, commit, hash = j.NewVersion, j.NewCommit, j.NewBinarySHA256
		}
		_, gateErr := os.Stat(j.GatePath)
		blocked := gateErr == nil
		receipts := []any{}
		if receipt.Load() {
			receipts = append(receipts, map[string]any{"task_id": "fixture-task", "runtime_id": "r", "completed_at": time.Now().UTC(), "ack_at": time.Now().UTC().Add(time.Second)})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": pid, "os": "darwin", "profile": j.Profile, "daemon_id": j.DaemonID, "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": version, "cli_commit": commit, "maintenance_shutdown": true,
			"workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}, "maintenance_gate": map[string]any{"supported": true, "blocked": blocked, "ready": blocked, "path": j.GatePath},
			"update_attestation": map[string]any{"binary_sha256": hash, "process_started_at": fmt.Sprintf("start-%d", pid), "runtime_heartbeat_acks": map[string]uint64{"r": beats.Add(1)}, "task_receipts": receipts}})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, portText, _ := net.SplitHostPort(u.Host)
	port, _ = strconv.Atoi(portText)
	j.HealthPort = port
	if err := os.WriteFile(filepath.Join(profile, "daemon.port"), []byte(fmt.Sprintf(`{"pid":%d,"port":%d}`, oldProcess.Process.Pid, port)), 0600); err != nil {
		t.Fatal(err)
	}
	newCLI := fmt.Sprintf("#!/bin/sh\nprintf '%%s' \"$$\" > %q\nprintf '{\"pid\":%%s,\"port\":%d}' \"$$\" > %q\nexec /bin/sleep 60\n", filepath.Join(profile, "daemon.pid"), port, filepath.Join(profile, "daemon.port"))
	j.NewBinarySHA256 = fixtureBundle(t, j.StagedBundle, newCLI)
	appExec := filepath.Join(j.StagedBundle, "Contents", "MacOS", j.NewAppExecutable)
	if err := os.MkdirAll(filepath.Dir(appExec), 0700); err != nil {
		t.Fatal(err)
	}
	appScript := fmt.Sprintf("#!/bin/sh\nprintf '{\"token\":\"fixture\",\"pid\":%%s}' \"$$\" > %q\nexec /bin/sleep 60\n", j.ReadyFile)
	if err := os.WriteFile(appExec, []byte(appScript), 0700); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(work, "journal.json")
	a := newLocalAdapter(journalPath)
	t.Cleanup(func() {
		for _, pid := range []int{j.NewAppPID, j.NewDaemonPID} {
			if pid > 0 {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done, err := Advance(ctx, journalPath, j, a)
	if err != nil || done || j.Phase != "awaiting_acceptance" {
		t.Fatalf("pending transaction: %v %v phase=%s", done, err, j.Phase)
	}
	if err := verifyBundleHash(j.RollbackBundle, j.OldBinarySHA256); err != nil {
		t.Fatal(err)
	}
	receipt.Store(true)
	done, err = Advance(ctx, journalPath, j, a)
	if err != nil || !done || j.Phase != "complete" {
		t.Fatalf("accepted transaction: %v %v phase=%s", done, err, j.Phase)
	}
	if _, err := os.Stat(filepath.Join(work, "ledger.json")); err != nil {
		t.Fatal(err)
	}
}
