package daemonupdate

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// LegacyInventory is a separately approved snapshot; it never invents an old
// build commit. The active process is checked again against these values.
type LegacyInventory struct {
	Version                     int                     `json:"version"`
	SourceID                    string                  `json:"source_id"`
	Profile                     string                  `json:"profile"`
	Bundle                      string                  `json:"bundle"`
	CLIEntry                    string                  `json:"cli_entry"`
	WorkDir                     string                  `json:"work_dir"`
	OldVersion                  string                  `json:"old_version"`
	OldBinarySHA256             string                  `json:"old_binary_sha256"`
	OldDataCompatibility        string                  `json:"old_data_compatibility"`
	CompatibilityEvidenceSHA256 string                  `json:"compatibility_evidence_sha256"`
	CandidateManifestSHA256     string                  `json:"candidate_manifest_sha256"`
	DaemonID                    string                  `json:"daemon_id"`
	ServerURLSHA256             string                  `json:"server_url_sha256"`
	ControllerMachineIDSHA256   string                  `json:"controller_machine_id_sha256"`
	Workspaces                  map[string][]string     `json:"workspaces"`
	Daemon                      LegacyProcessIdentity   `json:"daemon"`
	Apps                        []LegacyProcessIdentity `json:"apps"`
}

var sha256Hex = regexp.MustCompile(`^[a-f0-9]{64}$`)

// ReadLegacyInventory binds the input bytes to an independently supplied hash.
func ReadLegacyInventory(path, approvedSHA256 string) (LegacyInventory, error) {
	var inventory LegacyInventory
	if !filepath.IsAbs(path) || !sha256Hex.MatchString(approvedSHA256) {
		return inventory, errors.New("approved inventory hash required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !ownedByCurrentUser(info) || info.Size() > 1<<20 {
		return inventory, errors.New("untrusted legacy inventory")
	}
	f, err := os.Open(path)
	if err != nil {
		return inventory, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return inventory, errors.New("legacy inventory changed")
	}
	data, err := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if err != nil || len(data) > 1<<20 || fmt.Sprintf("%x", sha256.Sum256(data)) != approvedSHA256 {
		return inventory, errors.New("legacy inventory hash mismatch")
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&inventory); err != nil {
		return inventory, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return inventory, errors.New("trailing legacy inventory data")
	}
	return inventory, inventory.validate()
}
func (v LegacyInventory) validate() error {
	if v.Version != 1 || v.SourceID != "hyc5566/multica" || !releaseVersion.MatchString(v.OldVersion) || !sha256Hex.MatchString(v.OldBinarySHA256) || v.OldDataCompatibility != "multica-profile-v1" || !sha256Hex.MatchString(v.CompatibilityEvidenceSHA256) || !sha256Hex.MatchString(v.CandidateManifestSHA256) || !sha256Hex.MatchString(v.ServerURLSHA256) || !sha256Hex.MatchString(v.ControllerMachineIDSHA256) || v.DaemonID == "" {
		return errors.New("invalid legacy release identity")
	}
	if !filepath.IsAbs(v.Bundle) || filepath.Clean(v.Bundle) != v.Bundle || !strings.HasSuffix(v.Bundle, ".app") || !filepath.IsAbs(v.CLIEntry) || filepath.Clean(v.CLIEntry) != v.CLIEntry || !filepath.IsAbs(v.WorkDir) || filepath.Clean(v.WorkDir) != v.WorkDir || filepath.Base(v.WorkDir) != "zh-tw-update" || !strings.HasPrefix(v.Profile, "desktop-") || filepath.Base(v.Profile) != v.Profile || strings.ContainsAny(v.Profile, `/\`) {
		return errors.New("invalid legacy paths or profile")
	}
	if len(v.Workspaces) == 0 || len(v.Apps) == 0 {
		return errors.New("legacy workspace or App inventory missing")
	}
	runtimeSet := map[string]bool{}
	for workspace, ids := range v.Workspaces {
		if workspace == "" || len(ids) == 0 {
			return errors.New("invalid legacy workspace")
		}
		for _, id := range ids {
			if id == "" || runtimeSet[id] {
				return errors.New("invalid legacy runtime identity")
			}
			runtimeSet[id] = true
		}
	}
	if v.Daemon.ExecutablePath != bundleCLI(v.Bundle) || v.Daemon.SHA256 != v.OldBinarySHA256 {
		return errors.New("legacy daemon is not the approved bundled CLI")
	}
	pids := map[int]bool{}
	for _, p := range append([]LegacyProcessIdentity{v.Daemon}, v.Apps...) {
		if p.PID <= 1 || p.UID != os.Getuid() || p.StartedAt == "" || !filepath.IsAbs(p.ExecutablePath) || p.Inode == 0 || !sha256Hex.MatchString(p.SHA256) || pids[p.PID] {
			return errors.New("invalid legacy process identity")
		}
		pids[p.PID] = true
	}
	for _, p := range v.Apps {
		if !strings.HasPrefix(p.ExecutablePath, filepath.Join(v.Bundle, "Contents")+string(os.PathSeparator)) || p.ExecutablePath == bundleCLI(v.Bundle) {
			return errors.New("legacy App process outside approved bundle")
		}
	}
	return nil
}

// validateLegacyHealth accepts only a genuine no-gate daemon. The count must
// be explicitly present: absence is not evidence that no work is running.
func validateLegacyHealth(data []byte, v LegacyInventory) (bool, error) {
	var h health
	var presence struct {
		Active *int `json:"active_task_count"`
		Gate   struct {
			Supported *bool `json:"supported"`
		} `json:"maintenance_gate"`
	}
	if json.Unmarshal(data, &h) != nil || json.Unmarshal(data, &presence) != nil || presence.Active == nil || *presence.Active < 0 {
		return false, errors.New("legacy task count unavailable")
	}
	if h.Status != "running" || h.OS != "darwin" || h.Profile != v.Profile || h.DaemonID != v.DaemonID || h.PID != v.Daemon.PID || h.LaunchedBy != "desktop" || h.LifecycleManager != "" || h.Version != v.OldVersion || h.ServerURL == "" || fmt.Sprintf("%x", sha256.Sum256([]byte(h.ServerURL))) != v.ServerURLSHA256 {
		return false, errors.New("legacy daemon identity mismatch")
	}
	if h.MaintenanceShutdown || (presence.Gate.Supported != nil && *presence.Gate.Supported) {
		return false, errors.New("legacy entry refuses a gate-capable daemon")
	}
	actual, err := workspaceSet(h.Workspaces)
	if err != nil {
		return false, err
	}
	if len(actual) != len(v.Workspaces) {
		return false, errors.New("legacy workspace identity mismatch")
	}
	for id, expected := range v.Workspaces {
		got, ok := actual[id]
		if !ok || len(got) != len(expected) {
			return false, errors.New("legacy runtime identity mismatch")
		}
		counts := make(map[string]int, len(expected))
		for _, r := range expected {
			counts[r]++
		}
		for _, r := range got {
			counts[r]--
		}
		for _, count := range counts {
			if count != 0 {
				return false, errors.New("legacy runtime identity mismatch")
			}
		}
	}
	return *presence.Active == 0, nil
}

// verifyLegacyProcesses rechecks both the daemon and every enumerated App
// process. Enumeration is separate so an unrecorded App cannot be ignored.
func verifyLegacyProcesses(v LegacyInventory, appPIDs []int, inspect func(int) (LegacyProcessIdentity, error)) error {
	if len(appPIDs) != len(v.Apps) {
		return errors.New("legacy App process set changed")
	}
	expected := make(map[int]LegacyProcessIdentity, len(v.Apps))
	for _, p := range v.Apps {
		if _, exists := expected[p.PID]; exists {
			return errors.New("duplicate legacy App process")
		}
		expected[p.PID] = p
	}
	for _, pid := range appPIDs {
		want, ok := expected[pid]
		if !ok {
			return errors.New("unapproved legacy App process")
		}
		delete(expected, pid)
		actual, err := inspect(pid)
		if err != nil || actual != want {
			return errors.New("legacy App process changed")
		}
	}
	if len(expected) != 0 {
		return errors.New("legacy App process missing")
	}
	actual, err := inspect(v.Daemon.PID)
	if err != nil || actual != v.Daemon {
		return errors.New("legacy daemon process changed")
	}
	return nil
}
