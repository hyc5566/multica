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
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestLegacyStopPersistsAllIntentsBeforeAppCanStopDaemon(t *testing.T) {
	root := t.TempDir()
	t.Setenv(cli.TaskConfigRootEnv, root)
	profile := filepath.Join(root, "profiles", "desktop-test")
	if err := os.MkdirAll(profile, 0700); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(root, "zh-tw-update")
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	app := LegacyProcessIdentity{PID: 1001}
	daemon := LegacyProcessIdentity{PID: 1002}
	v := LegacyInventory{Bundle: filepath.Join(root, "App.app"), Profile: "desktop-test", DaemonID: "daemon", Daemon: daemon, Apps: []LegacyProcessIdentity{app}, OldVersion: "0.8.1-zh-tw.3", ServerURLSHA256: fixtureServerHash(), Workspaces: map[string][]string{"w": {"r"}}}
	j := &Journal{Profile: v.Profile, Token: "token", GatePath: filepath.Join(profile, "daemon.maintenance"), Legacy: &LegacyState{Inventory: v}}
	path := filepath.Join(work, "journal.json")
	if err := acquireGate(path, j); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "running", "pid": daemon.PID, "os": "darwin", "profile": v.Profile, "daemon_id": v.DaemonID, "launched_by": "desktop", "cli_version": v.OldVersion, "server_url": fixtureServerURL, "active_task_count": 0, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}})
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)
	_, port, _ := net.SplitHostPort(u.Host)
	j.HealthPort, _ = strconv.Atoi(port)
	origInspect, origStop, origList := inspectLegacy, stopLegacy, enumerateLegacyApps
	defer func() { inspectLegacy, stopLegacy, enumerateLegacyApps = origInspect, origStop, origList }()
	alive := map[int]LegacyProcessIdentity{app.PID: app, daemon.PID: daemon}
	inspectLegacy = func(pid int) (LegacyProcessIdentity, error) {
		p, ok := alive[pid]
		if !ok {
			return LegacyProcessIdentity{}, ErrLegacyProcessExited
		}
		return p, nil
	}
	enumerateLegacyApps = func(string) ([]int, error) {
		if _, ok := alive[app.PID]; ok {
			return []int{app.PID}, nil
		}
		return nil, nil
	}
	stopLegacy = func(p LegacyProcessIdentity) error {
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var persisted Journal
		if json.Unmarshal(data, &persisted) != nil || !persisted.Legacy.StopIntents[app.PID] || !persisted.Legacy.StopIntents[daemon.PID] {
			return errors.New("stop before durable intents")
		}
		delete(alive, app.PID)
		delete(alive, daemon.PID)
		return nil
	}
	if err := (&legacyLocalAdapter{newLocalAdapter(path)}).StopOld(context.Background(), j); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyCandidateExitedBeforeNativeInspection(t *testing.T) {
	savedInspect, savedList, savedStop := inspectLegacy, enumerateLegacyApps, stopLegacy
	defer func() { inspectLegacy, enumerateLegacyApps, stopLegacy = savedInspect, savedList, savedStop }()
	enumerateLegacyApps = func(string) ([]int, error) { return nil, nil }
	stops := 0
	stopLegacy = func(LegacyProcessIdentity) error { stops++; return nil }
	inspectLegacy = func(int) (LegacyProcessIdentity, error) { return LegacyProcessIdentity{}, ErrLegacyProcessExited }
	j := &Journal{StartRequested: true, NewDaemonPID: 123, Legacy: &LegacyState{Inventory: LegacyInventory{Bundle: "/fixture/Multica.app"}}}
	a := &legacyLocalAdapter{newLocalAdapter(filepath.Join(t.TempDir(), "journal.json"))}
	if err := a.stopCandidates(j); err != nil || stops != 0 {
		t.Fatalf("already exited: %v stops=%d", err, stops)
	}
	j.Legacy.CandidateStopIntents = nil
	inspectLegacy = func(pid int) (LegacyProcessIdentity, error) {
		return LegacyProcessIdentity{PID: pid, StartedAt: "reused"}, nil
	}
	if err := a.stopCandidates(j); err == nil || stops != 0 {
		t.Fatal("unidentified live or reused PID accepted")
	}
	j.StartRequested = false
	j.Legacy.CandidateStopIntents = nil
	inspectLegacy = func(int) (LegacyProcessIdentity, error) { return LegacyProcessIdentity{}, ErrLegacyProcessExited }
	if err := a.stopCandidates(j); err == nil {
		t.Fatal("missing launch intent accepted")
	}
}

func TestLegacyArtifactCleanupIsAttestedAndResumable(t *testing.T) {
	root := t.TempDir()
	work := filepath.Join(root, "zh-tw-update")
	baseline := filepath.Join(work, "baseline")
	if err := os.MkdirAll(baseline, 0700); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(baseline, "Multica.app")
	fixtureBundle(t, backup, "old CLI")
	records, digest, err := legacyTreeManifest(backup)
	if err != nil {
		t.Fatal(err)
	}
	if err := saveJSON(filepath.Join(baseline, "manifest.json"), records); err != nil {
		t.Fatal(err)
	}
	coordinator := filepath.Join(work, "legacy-coordinator")
	if err := os.WriteFile(coordinator, []byte("new CLI"), 0700); err != nil {
		t.Fatal(err)
	}
	newHash, err := hashFile(coordinator)
	if err != nil {
		t.Fatal(err)
	}
	j := &Journal{OldBundle: filepath.Join(root, "Multica.app"), NewBinarySHA256: newHash, Legacy: &LegacyState{BackupManifestSHA256: digest, CoordinatorSHA256: newHash}}
	path := filepath.Join(work, "journal.json")
	a := &legacyLocalAdapter{newLocalAdapter(path)}
	saved := legacyVerifySignature
	defer func() { legacyVerifySignature = saved }()
	legacyVerifySignature = func(context.Context, string) error { return nil }
	if err := os.WriteFile(bundleCLI(backup), []byte("tampered"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupLegacyArtifacts(context.Background(), j); err == nil {
		t.Fatal("tampered baseline accepted for removal")
	}
	if err := os.WriteFile(bundleCLI(backup), []byte("old CLI"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(baseline, "foreign"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupLegacyArtifacts(context.Background(), j); err == nil {
		t.Fatal("foreign file accepted for removal")
	}
	if err := os.Remove(filepath.Join(baseline, "foreign")); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupLegacyArtifacts(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(baseline); !os.IsNotExist(err) {
		t.Fatal("baseline not removed")
	}
	if _, err := os.Stat(coordinator); !os.IsNotExist(err) {
		t.Fatal("coordinator not removed")
	}
	if err := a.cleanupLegacyArtifacts(context.Background(), j); err != nil {
		t.Fatalf("cleanup was not resumable: %v", err)
	}
}
