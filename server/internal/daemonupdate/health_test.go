package daemonupdate

import (
	"crypto/sha256"
	"fmt"
	"testing"
)

const fixtureServerURL = "https://server.example"

func fixtureServerHash() string { return fmt.Sprintf("%x", sha256.Sum256([]byte(fixtureServerURL))) }

func TestObserverRequiresThreeObservedProgressionsPerRuntime(t *testing.T) {
	o, err := newHeartbeatObserver([]string{"a", "b"}, map[string]uint64{"a": 0, "b": 0})
	if err != nil {
		t.Fatal(err)
	}
	for _, counts := range []map[string]uint64{{"a": 3, "b": 3}, {"a": 4, "b": 3}, {"a": 5, "b": 4}} {
		done, err := o.observe(counts)
		if err != nil || done {
			t.Fatalf("premature acceptance: %v %v", done, err)
		}
	}
	done, err := o.observe(map[string]uint64{"a": 5, "b": 5})
	if err != nil || !done {
		t.Fatalf("missing acceptance: %v %v", done, err)
	}
	if _, err := o.observe(map[string]uint64{"a": 4, "b": 5}); err == nil {
		t.Fatal("counter regression accepted")
	}
}

func TestNewHealthRequiresOriginalIdentity(t *testing.T) {
	j := &Journal{Profile: "desktop-test", DaemonID: "daemon", ServerURLSHA256: fixtureServerHash(), NewVersion: "v", NewCommit: "commit", NewBinarySHA256: "hash", RuntimeIDs: []string{"r"}, Workspaces: map[string][]string{"w": {"r"}}}
	h := health{Status: "running", PID: 123, OS: "darwin", LaunchedBy: "desktop", Profile: j.Profile, DaemonID: j.DaemonID, ServerURL: fixtureServerURL, Version: j.NewVersion, Commit: j.NewCommit, Workspaces: []healthWorkspace{{ID: "w", Runtimes: []string{"r"}}}, Attestation: healthAttestation{BinarySHA256: "hash", ProcessStartedAt: "start", RuntimeHeartbeatAcks: map[string]uint64{"r": 1}}}
	if err := validateNewHealth(j, h, 123, "start"); err != nil {
		t.Fatal(err)
	}
	h.ServerURL = "https://other.example"
	if err := validateNewHealth(j, h, 123, "start"); err == nil {
		t.Fatal("different server accepted")
	}
	h.ServerURL = fixtureServerURL
	h.PID++
	if err := validateNewHealth(j, h, 123, "start"); err == nil {
		t.Fatal("different PID accepted")
	}
	h.PID--
	h.Attestation.BinarySHA256 = "wrong"
	if err := validateNewHealth(j, h, 123, "start"); err == nil {
		t.Fatal("different binary accepted")
	}
}

func TestCandidateRequiresItsOwnHeldIdleGate(t *testing.T) {
	j := &Journal{GatePath: "/profile/daemon.maintenance"}
	var h health
	h.MaintenanceShutdown = true
	h.MaintenanceGate.Supported = true
	h.MaintenanceGate.Blocked = true
	h.MaintenanceGate.Ready = true
	h.MaintenanceGate.Path = j.GatePath
	if err := validateHeldGate(j, h); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*health){
		func(h *health) { h.MaintenanceGate.Blocked = false },
		func(h *health) { h.MaintenanceGate.Path = "/other" },
		func(h *health) { h.ActiveTasks = 1 },
		func(h *health) { h.MaintenanceGate.ClaimsInFlight = 1 },
	} {
		bad := h
		mutate(&bad)
		if err := validateHeldGate(j, bad); err == nil {
			t.Fatal("invalid gate accepted")
		}
	}
}
