package daemonupdate

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

type timedOutDrainAdapter struct{ *localAdapter }

func (a timedOutDrainAdapter) DrainAndStopOld(context.Context, *Journal) error {
	return errors.New("drain timed out")
}

func TestDrainTimeoutAfterHandoffReopensAndRemainsPending(t *testing.T) {
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
	j := fixtureJournal("stopping_old")
	j.ServerURLSHA256 = fixtureServerHash()
	j.Profile, j.Token, j.DaemonID, j.DaemonPID, j.DaemonStartedAt, j.AppPID = "test", "token", "daemon", os.Getpid(), "start", 99999999
	j.GatePath = filepath.Join(profile, "daemon.maintenance")
	j.OldBundle = filepath.Join(root, "Applications", "Multica.app")
	j.StagedBundle = filepath.Join(work, "staging", "Multica.app")
	j.RollbackBundle = filepath.Join(work, "rollback", "Multica.app")
	j.OlderBundle = filepath.Join(work, "older", "Multica.app")
	j.OldBinarySHA256 = fixtureBundle(t, j.OldBundle, "old")
	j.OldCommit = "commit"
	j.Workspaces, j.RuntimeIDs = map[string][]string{"w": {"r"}}, []string{"r"}
	path := filepath.Join(work, "journal.json")
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	var beats atomic.Uint64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": j.DaemonPID, "os": "darwin", "profile": j.Profile, "daemon_id": j.DaemonID, "server_url": fixtureServerURL, "launched_by": "desktop", "cli_version": j.OldVersion, "cli_commit": j.OldCommit, "maintenance_shutdown": true,
			"workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}, "maintenance_gate": map[string]any{"supported": true, "blocked": true, "ready": false, "path": j.GatePath},
			"update_attestation": map[string]any{"binary_sha256": j.OldBinarySHA256, "process_started_at": "start", "runtime_heartbeat_acks": map[string]uint64{"r": beats.Add(1)}}})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	j.HealthPort, _ = strconv.Atoi(port)
	calls := 0
	original := reopenBundle
	defer func() { reopenBundle = original }()
	reopenBundle = func(string) error { calls++; return nil }
	if done, err := Advance(context.Background(), path, j, timedOutDrainAdapter{newLocalAdapter(path)}); err == nil || done {
		t.Fatalf("timeout returned success: %v %v", done, err)
	}
	if calls != 1 || j.Phase != "rollback_awaiting_acceptance" || j.RollbackGateReleasedAt.IsZero() {
		t.Fatalf("rollback state=%s reopen=%d", j.Phase, calls)
	}
}

func TestRollbackReopensOnlyAfterOldAppExited(t *testing.T) {
	for _, live := range []bool{true, false} {
		t.Run(map[bool]string{true: "cancelled quit", false: "handoff exited"}[live], func(t *testing.T) {
			root := t.TempDir()
			t.Setenv(cli.TaskConfigRootEnv, root)
			profile := filepath.Join(root, "profiles", "test")
			if err := os.MkdirAll(profile, 0700); err != nil {
				t.Fatal(err)
			}
			j := &Journal{Profile: "test", Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance"), OldBundle: filepath.Join(root, "Multica.app"), AppPID: 99999999}
			if live {
				j.AppPID = os.Getpid()
			}
			path := filepath.Join(root, "journal")
			if err := acquireGate(path, j); err != nil {
				t.Fatal(err)
			}
			calls := 0
			original := reopenBundle
			defer func() { reopenBundle = original }()
			reopenBundle = func(path string) error {
				calls++
				if path != j.OldBundle {
					t.Fatalf("wrong bundle: %s", path)
				}
				return nil
			}
			if err := newLocalAdapter(path).finishRollback(context.Background(), j, 123, "epoch"); err != nil {
				t.Fatal(err)
			}
			if live && calls != 0 {
				t.Fatal("reopened while old App was live")
			}
			if !live && (calls != 1 || !j.RollbackReopenRequested) {
				t.Fatal("exited App was not reopened with durable intent")
			}
			if _, err := os.Stat(j.GatePath); !os.IsNotExist(err) {
				t.Fatalf("gate remains: %v", err)
			}
			if j.RollbackGateReleasedAt.IsZero() {
				t.Fatal("rollback release time not saved")
			}
		})
	}
}

func TestCandidateIdentityFailureUsesOnlyJournalledNativeProcess(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	j := &Journal{Profile: "test", Token: "token", DaemonID: "daemon", GatePath: filepath.Join(profile, "daemon.maintenance"), NewDaemonPID: 123, CandidateDaemonIdentity: &LegacyProcessIdentity{PID: 123, StartedAt: "native", SHA256: "approved"}}
	path := filepath.Join(root, "journal.json")
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"pid": 123, "daemon_id": "unexpected"})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	j.HealthPort, _ = strconv.Atoi(port)
	savedInspect, savedStop := inspectLegacy, stopLegacy
	defer func() { inspectLegacy, stopLegacy = savedInspect, savedStop }()
	calls := 0
	inspectLegacy = func(int) (LegacyProcessIdentity, error) { return *j.CandidateDaemonIdentity, nil }
	stopLegacy = func(LegacyProcessIdentity) error { calls++; return nil }
	a := newLocalAdapter(path)
	if err := a.stopCandidateSafely(context.Background(), j); err != nil || calls != 1 {
		t.Fatalf("verified target not stopped: %v calls=%d", err, calls)
	}
	inspectLegacy = func(int) (LegacyProcessIdentity, error) {
		return LegacyProcessIdentity{PID: 123, StartedAt: "reused"}, nil
	}
	if err := a.stopCandidateSafely(context.Background(), j); err == nil || calls != 1 {
		t.Fatal("changed process was stopped")
	}
}
