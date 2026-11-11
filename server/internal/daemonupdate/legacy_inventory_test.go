package daemonupdate

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyInventoryRequiresApprovedExactProcesses(t *testing.T) {
	root := t.TempDir()
	bundle := filepath.Join(root, "Multica.app")
	digest := strings.Repeat("a", 64)
	proc := func(pid int, executable string) LegacyProcessIdentity {
		return LegacyProcessIdentity{PID: pid, UID: os.Getuid(), StartedAt: "start", ExecutablePath: executable, Inode: 10, SHA256: digest}
	}
	inventory := LegacyInventory{Version: 1, SourceID: "hyc5566/multica", Profile: "desktop-test", Bundle: bundle, CLIEntry: bundleCLI(bundle), WorkDir: filepath.Join(root, "zh-tw-update"), OldVersion: "0.8.1-zh-tw.3", OldBinarySHA256: digest, OldDataCompatibility: "multica-profile-v1", CompatibilityEvidenceSHA256: digest, ControllerMachineIDSHA256: digest, CandidateManifestSHA256: digest, ServerURLSHA256: digest, DaemonID: "daemon", Workspaces: map[string][]string{"w": {"r"}}, Daemon: proc(101, bundleCLI(bundle)), Apps: []LegacyProcessIdentity{proc(102, filepath.Join(bundle, "Contents", "MacOS", "Multica"))}}
	data, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "inventory.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(data))
	if _, err := ReadLegacyInventory(path, hash); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadLegacyInventory(path, strings.Repeat("0", 64)); err == nil {
		t.Fatal("unapproved inventory accepted")
	}
	for _, mutate := range []func(*LegacyInventory){
		func(v *LegacyInventory) { v.OldDataCompatibility = "" },
		func(v *LegacyInventory) { v.CompatibilityEvidenceSHA256 = "" },
		func(v *LegacyInventory) { v.ControllerMachineIDSHA256 = "" },
	} {
		bad := inventory
		mutate(&bad)
		if err := bad.validate(); err == nil {
			t.Fatal("missing independent approval evidence accepted")
		}
	}
	inventory.Daemon.ExecutablePath = "/other/multica"
	if err := inventory.validate(); err == nil {
		t.Fatal("unbundled daemon accepted")
	}
	inventory.Daemon.ExecutablePath = bundleCLI(bundle)
	inventory.Apps[0].PID = inventory.Daemon.PID
	if err := inventory.validate(); err == nil {
		t.Fatal("duplicate PID accepted")
	}
}

func TestLegacyHealthRequiresKnownTasksAndOriginalIdentity(t *testing.T) {
	url := "https://server.example"
	hash := fmt.Sprintf("%x", sha256.Sum256([]byte(url)))
	v := LegacyInventory{Profile: "desktop-test", DaemonID: "daemon", OldVersion: "0.8.1-zh-tw.3", ServerURLSHA256: hash, Daemon: LegacyProcessIdentity{PID: 123}, Workspaces: map[string][]string{"w": {"r"}}}
	h := map[string]any{"status": "running", "os": "darwin", "profile": v.Profile, "daemon_id": v.DaemonID, "pid": 123, "launched_by": "desktop", "cli_version": v.OldVersion, "server_url": url, "active_task_count": 1, "workspaces": []any{map[string]any{"id": "w", "runtimes": []string{"r"}}}}
	check := func() (bool, error) { data, _ := json.Marshal(h); return validateLegacyHealth(data, v) }
	if idle, err := check(); err != nil || idle {
		t.Fatalf("busy accepted: %v %v", idle, err)
	}
	h["active_task_count"] = 0
	if idle, err := check(); err != nil || !idle {
		t.Fatalf("idle refused: %v %v", idle, err)
	}
	delete(h, "active_task_count")
	if _, err := check(); err == nil {
		t.Fatal("missing task count accepted")
	}
	h["active_task_count"] = 0
	h["maintenance_gate"] = map[string]any{"supported": true}
	if _, err := check(); err == nil {
		t.Fatal("gate-capable daemon accepted by legacy entry")
	}
	delete(h, "maintenance_gate")
	h["server_url"] = "https://other.example"
	if _, err := check(); err == nil {
		t.Fatal("different server accepted")
	}
}

func TestLegacyProcessSetRejectsMissingAndUnapprovedApps(t *testing.T) {
	daemon := LegacyProcessIdentity{PID: 101, StartedAt: "daemon"}
	app := LegacyProcessIdentity{PID: 102, StartedAt: "app"}
	v := LegacyInventory{Daemon: daemon, Apps: []LegacyProcessIdentity{app}}
	inspect := func(pid int) (LegacyProcessIdentity, error) {
		if pid == 101 {
			return daemon, nil
		}
		return app, nil
	}
	if err := verifyLegacyProcesses(v, []int{102}, inspect); err != nil {
		t.Fatal(err)
	}
	for _, pids := range [][]int{nil, {102, 103}, {103}} {
		if err := verifyLegacyProcesses(v, pids, inspect); err == nil {
			t.Fatalf("accepted app set %v", pids)
		}
	}
	inspectChanged := func(pid int) (LegacyProcessIdentity, error) {
		p, _ := inspect(pid)
		p.StartedAt = "replacement"
		return p, nil
	}
	if err := verifyLegacyProcesses(v, []int{102}, inspectChanged); err == nil {
		t.Fatal("reused process accepted")
	}
}

func TestLegacyAppProcessEnumerationKeepsPathsWithSpaces(t *testing.T) {
	bundle := "/Applications/Multica App.app"
	output := "501 10 /Applications/Multica App.app/Contents/MacOS/Multica\n501 11 /Applications/Multica App.app/Contents/Frameworks/Helper.app/Contents/MacOS/Helper\n501 12 " + bundleCLI(bundle) + "\n502 13 /Applications/Other.app/Contents/MacOS/Other\n"
	pids, err := parseLegacyAppPIDs(output, bundle, 501)
	if err != nil || len(pids) != 2 || pids[0] != 10 || pids[1] != 11 {
		t.Fatalf("%v %v", pids, err)
	}
}

func TestLegacyAppProcessEnumerationRejectsOtherUserOfBundle(t *testing.T) {
	bundle := "/Applications/Multica App.app"
	for _, target := range []string{filepath.Join(bundle, "Contents", "MacOS", "Multica"), bundleCLI(bundle)} {
		output := "501 10 " + filepath.Join(bundle, "Contents", "MacOS", "Multica") + "\n502 13 " + target + "\n"
		if _, err := parseLegacyAppPIDs(output, bundle, 501); err == nil {
			t.Fatalf("accepted another user running %s", target)
		}
	}
}
